package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"netdb/internal/model"
)

// DesiredRecord is a single (name, type, value) tuple that should exist at
// the DNS provider. A/AAAA records with multiple IP bindings flatten into
// multiple DesiredRecords. Callers convert this to dnsprov.Record at the
// reconciler layer (kept here to avoid store->dnsprov import).
type DesiredRecord struct {
	Name  string
	Type  string
	Value string
	TTL   int
}

// validRecordTypes gate manual record creation to the types we know how to
// reconcile against providers. SOA/NS are provider-managed; others (SRV,
// CAA) we can add later as the UI grows.
var validRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "TXT": true, "MX": true, "PTR": true,
}

// CreateManualDNSRecord inserts a dns_record with source='manual'. The
// zone is resolved by name to keep the store API stable even if the caller
// doesn't know the zone's ID. TTL falls back to the zone's default.
func (s *Store) CreateManualDNSRecord(ctx context.Context, zoneName, fqdn, rtype, target string, ttl int) (int64, error) {
	if !validRecordTypes[rtype] {
		return 0, fmt.Errorf("unsupported record type: %s", rtype)
	}
	var zoneID int64
	var defaultTTL int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, default_ttl FROM zones WHERE name = ?`, zoneName).
		Scan(&zoneID, &defaultTTL)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("zone %q not found — create it at /zones first", zoneName)
	}
	if err != nil {
		return 0, err
	}
	if ttl <= 0 {
		ttl = defaultTTL
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO dns_records(zone_id, fqdn, type, target, ttl, enabled, source)
		 VALUES(?, ?, ?, ?, ?, 1, 'manual')`,
		zoneID, fqdn, rtype, target, ttl)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteDNSRecord removes a record regardless of source. Callers should
// ensure it isn't source='auto' or the synthesizer will re-create it on the
// next run.
func (s *Store) DeleteDNSRecord(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM dns_records WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListDesiredRecordsByZone returns the flattened records the provider should
// hold for the given zone. A/AAAA records expand through dns_bindings; other
// types take their value from dns_records.target. Disabled records and A/AAAA
// with no bindings are skipped (a bindingless A has nothing to push).
func (s *Store) ListDesiredRecordsByZone(ctx context.Context, zoneID int64) ([]DesiredRecord, error) {
	// Auto A/AAAA records expand through dns_bindings (one row per IP).
	// Manual A/AAAA records and all non-IP types use r.target. COALESCE does
	// both: LEFT JOIN gives NULL ip.ip when there are no bindings, so we
	// fall back to r.target. The final != '' strips auto rows that don't
	// resolve to anything (shouldn't happen, but a cheap guard).
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.fqdn, r.type, r.ttl,
		       COALESCE(ip.ip, r.target) AS value
		FROM dns_records r
		LEFT JOIN dns_bindings b     ON b.dns_record_id = r.id
		LEFT JOIN ip_assignments ip  ON ip.id = b.ip_assignment_id
		WHERE r.zone_id = ?
		  AND r.enabled = 1
		  AND COALESCE(ip.ip, r.target) != ''
		ORDER BY r.fqdn, r.type, value`, zoneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DesiredRecord
	for rows.Next() {
		var d DesiredRecord
		if err := rows.Scan(&d.Name, &d.Type, &d.TTL, &d.Value); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListDNSRecords returns every dns_record joined with its zone and the IPs
// it currently binds to, sorted by zone then fqdn. Used by the read-only
// /dns page so operators can see what the synthesizer has materialized.
func (s *Store) ListDNSRecords(ctx context.Context) ([]model.DNSRecordView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			r.id,
			COALESCE(z.name, '')             AS zone_name,
			r.fqdn,
			r.type,
			r.target,
			r.ttl,
			r.source,
			COALESCE(GROUP_CONCAT(ip.ip, '|'), '') AS ips
		FROM dns_records r
		LEFT JOIN zones z           ON r.zone_id = z.id
		LEFT JOIN dns_bindings b    ON b.dns_record_id = r.id
		LEFT JOIN ip_assignments ip ON ip.id = b.ip_assignment_id
		GROUP BY r.id
		ORDER BY COALESCE(z.name, ''), r.fqdn, r.type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.DNSRecordView
	for rows.Next() {
		var v model.DNSRecordView
		var ips sql.NullString
		if err := rows.Scan(&v.ID, &v.ZoneName, &v.FQDN, &v.Type, &v.Target,
			&v.TTL, &v.Source, &ips); err != nil {
			return nil, err
		}
		if ips.Valid && ips.String != "" {
			v.IPs = strings.Split(ips.String, "|")
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
