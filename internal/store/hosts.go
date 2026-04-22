package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"netdb/internal/model"
)

var ErrNotFound = errors.New("not found")

func (s *Store) ListHosts(ctx context.Context) ([]model.Host, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, tags, dns_name, dns_enabled, created_at, updated_at
		 FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Host
	for rows.Next() {
		var h model.Host
		if err := rows.Scan(&h.ID, &h.Name, &h.Description, &h.Tags, &h.DNSName, &h.DNSEnabled, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) GetHost(ctx context.Context, id int64) (*model.Host, error) {
	var h model.Host
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, tags, dns_name, dns_enabled, created_at, updated_at
		 FROM hosts WHERE id = ?`, id).
		Scan(&h.ID, &h.Name, &h.Description, &h.Tags, &h.DNSName, &h.DNSEnabled, &h.CreatedAt, &h.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (s *Store) CreateHost(ctx context.Context, name, description, tags string) (*model.Host, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO hosts(name, description, tags) VALUES(?, ?, ?)`,
		name, description, tags)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetHost(ctx, id)
}

func (s *Store) UpdateHost(ctx context.Context, id int64, name, description, tags, dnsName string, dnsEnabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE hosts SET name=?, description=?, tags=?, dns_name=?, dns_enabled=?,
		                  updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		name, description, tags, dnsName, dnsEnabled, id)
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

func (s *Store) DeleteHost(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM hosts WHERE id=?`, id)
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

// GetHostDetail returns the host plus its NICs and each NIC's IP assignments.
func (s *Store) GetHostDetail(ctx context.Context, id int64) (*model.HostDetail, error) {
	h, err := s.GetHost(ctx, id)
	if err != nil {
		return nil, err
	}
	nics, err := s.ListNICsByHost(ctx, id)
	if err != nil {
		return nil, err
	}
	detail := &model.HostDetail{Host: *h}
	for _, n := range nics {
		ips, err := s.ListIPsByNIC(ctx, n.ID)
		if err != nil {
			return nil, fmt.Errorf("load ips for nic %d: %w", n.ID, err)
		}
		detail.NICs = append(detail.NICs, model.NICWithIPs{NIC: n, IPs: ips})
	}
	return detail, nil
}
