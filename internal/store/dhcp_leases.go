package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// LeaseObserved represents one active DHCPv4 lease we want to reflect as a
// (nic, ip_assignment) row with kind='dynamic'.
type LeaseObserved struct {
	MAC      string
	IP       string
	Hostname string // client-provided; optional
}

// UpsertDynamicLease attaches the lease to an existing NIC (matched by MAC)
// or creates a new host+NIC pair tagged "dhcp-discovered" with dns_enabled=0.
// Idempotent: repeated calls with the same (MAC, IP) don't create duplicates.
func (s *Store) UpsertDynamicLease(ctx context.Context, lease LeaseObserved) error {
	mac, err := NormalizeMAC(lease.MAC)
	if err != nil {
		return fmt.Errorf("bad mac %q: %w", lease.MAC, err)
	}
	ip, err := NormalizeIP(lease.IP)
	if err != nil {
		return fmt.Errorf("bad ip %q: %w", lease.IP, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var nicID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM nics WHERE mac = ?`, mac).Scan(&nicID)
	if errors.Is(err, sql.ErrNoRows) {
		nicID, err = createDiscoveredHost(ctx, tx, mac, lease.Hostname)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// ip_assignments.UNIQUE is on (nic_id, ip), so INSERT OR IGNORE keeps us
	// idempotent without touching the existing row's kind (so a reserved
	// upgrade made by a user survives polling).
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO ip_assignments(nic_id, ip, kind) VALUES(?, ?, 'dynamic')`,
		nicID, ip); err != nil {
		return err
	}
	return tx.Commit()
}

// createDiscoveredHost inserts a (host, nic) pair for a previously unknown
// MAC. Name priority: client-provided hostname, then a synthetic dhcp-XXXX.
// Names must be unique; we suffix with a bit of the MAC if collision.
func createDiscoveredHost(ctx context.Context, tx *sql.Tx, mac, hostname string) (int64, error) {
	macFlat := strings.ReplaceAll(mac, ":", "")
	name := sanitizeHostname(hostname)
	if name == "" {
		name = "dhcp-" + macFlat
	}

	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hosts WHERE name = ?`, name).Scan(&exists); err != nil {
		return 0, err
	}
	if exists > 0 {
		name = fmt.Sprintf("%s-%s", name, macFlat[:6])
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO hosts(name, description, tags, dns_enabled)
		 VALUES(?, ?, 'dhcp-discovered', 0)`,
		name, "auto-discovered via DHCP")
	if err != nil {
		return 0, err
	}
	hostID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	res, err = tx.ExecContext(ctx,
		`INSERT INTO nics(host_id, mac, label) VALUES(?, ?, 'dhcp')`,
		hostID, mac)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// sanitizeHostname lowercases and strips characters that'd be awkward in
// a host name. DHCP-supplied hostnames are untrusted user input.
func sanitizeHostname(in string) string {
	s := strings.ToLower(strings.TrimSpace(in))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// DeleteStaleDynamic drops dynamic ip_assignments whose (mac, ip) pair is
// not in seen. Used after a lease poll to reflect expirations.
func (s *Store) DeleteStaleDynamic(ctx context.Context, seen map[string]bool) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ip.id, n.mac, ip.ip
		FROM ip_assignments ip
		JOIN nics n ON n.id = ip.nic_id
		WHERE ip.kind = 'dynamic'`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var mac, addr string
		if err := rows.Scan(&id, &mac, &addr); err != nil {
			rows.Close()
			return 0, err
		}
		if !seen[mac+"|"+addr] {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM ip_assignments WHERE id = ?`, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
