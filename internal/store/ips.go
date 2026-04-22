package store

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"

	"netdb/internal/model"
)

var validIPKinds = map[string]bool{"static": true, "reserved": true, "dynamic": true}

// NormalizeIP returns the canonical string form of v4 or v6 addresses.
func NormalizeIP(in string) (string, error) {
	a, err := netip.ParseAddr(in)
	if err != nil {
		return "", err
	}
	return a.String(), nil
}

func (s *Store) ListIPsByNIC(ctx context.Context, nicID int64) ([]model.IPAssignment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, nic_id, subnet_id, ip, kind, created_at, updated_at
		 FROM ip_assignments WHERE nic_id=? ORDER BY ip`, nicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.IPAssignment
	for rows.Next() {
		var a model.IPAssignment
		var subnet sql.NullInt64
		if err := rows.Scan(&a.ID, &a.NICID, &subnet, &a.IP, &a.Kind, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		if subnet.Valid {
			v := subnet.Int64
			a.SubnetID = &v
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CreateIPAssignment(ctx context.Context, nicID int64, subnetID *int64, ip, kind string) (*model.IPAssignment, error) {
	if !validIPKinds[kind] {
		return nil, errors.New("kind must be static, reserved, or dynamic")
	}
	norm, err := NormalizeIP(ip)
	if err != nil {
		return nil, err
	}
	var sn sql.NullInt64
	if subnetID != nil {
		sn = sql.NullInt64{Int64: *subnetID, Valid: true}
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO ip_assignments(nic_id, subnet_id, ip, kind) VALUES(?, ?, ?, ?)`,
		nicID, sn, norm, kind)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	var a model.IPAssignment
	var sub sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		`SELECT id, nic_id, subnet_id, ip, kind, created_at, updated_at
		 FROM ip_assignments WHERE id=?`, id).
		Scan(&a.ID, &a.NICID, &sub, &a.IP, &a.Kind, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if sub.Valid {
		v := sub.Int64
		a.SubnetID = &v
	}
	return &a, nil
}

func (s *Store) DeleteIPAssignment(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ip_assignments WHERE id=?`, id)
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
