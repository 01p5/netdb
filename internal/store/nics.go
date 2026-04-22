package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"netdb/internal/model"
)

// NormalizeMAC lowercases and separates a MAC as aa:bb:cc:dd:ee:ff.
// Accepts input with colons, hyphens, or periods; rejects anything else.
func NormalizeMAC(in string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(in))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, ":", "")
	s = strings.ReplaceAll(s, ".", "")
	if len(s) != 12 {
		return "", errors.New("mac must have 12 hex digits")
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", errors.New("mac contains non-hex character")
		}
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(s[i : i+2])
	}
	return b.String(), nil
}

func (s *Store) ListNICsByHost(ctx context.Context, hostID int64) ([]model.NIC, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, host_id, mac, label, created_at, updated_at
		 FROM nics WHERE host_id=? ORDER BY mac`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.NIC
	for rows.Next() {
		var n model.NIC
		if err := rows.Scan(&n.ID, &n.HostID, &n.MAC, &n.Label, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetNIC(ctx context.Context, id int64) (*model.NIC, error) {
	var n model.NIC
	err := s.db.QueryRowContext(ctx,
		`SELECT id, host_id, mac, label, created_at, updated_at
		 FROM nics WHERE id=?`, id).
		Scan(&n.ID, &n.HostID, &n.MAC, &n.Label, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *Store) CreateNIC(ctx context.Context, hostID int64, mac, label string) (*model.NIC, error) {
	norm, err := NormalizeMAC(mac)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO nics(host_id, mac, label) VALUES(?, ?, ?)`,
		hostID, norm, label)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetNIC(ctx, id)
}

func (s *Store) DeleteNIC(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nics WHERE id=?`, id)
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
