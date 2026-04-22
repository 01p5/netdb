package store

import (
	"context"
	"database/sql"
	"errors"

	"netdb/internal/model"
)

func (s *Store) ListSubnets(ctx context.Context) ([]model.Subnet, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, cidr, vlan, gateway, description,
		        dhcp_range_start, dhcp_range_end, dhcp_options_json, dns_servers,
		        created_at, updated_at
		 FROM subnets ORDER BY cidr`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Subnet
	for rows.Next() {
		var sb model.Subnet
		var vlan sql.NullInt64
		if err := rows.Scan(&sb.ID, &sb.CIDR, &vlan, &sb.Gateway, &sb.Description,
			&sb.DHCPRangeStart, &sb.DHCPRangeEnd, &sb.DHCPOptionsJSON, &sb.DNSServers,
			&sb.CreatedAt, &sb.UpdatedAt); err != nil {
			return nil, err
		}
		if vlan.Valid {
			v := int(vlan.Int64)
			sb.VLAN = &v
		}
		out = append(out, sb)
	}
	return out, rows.Err()
}

func (s *Store) GetSubnet(ctx context.Context, id int64) (*model.Subnet, error) {
	var sb model.Subnet
	var vlan sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, cidr, vlan, gateway, description,
		        dhcp_range_start, dhcp_range_end, dhcp_options_json, dns_servers,
		        created_at, updated_at
		 FROM subnets WHERE id=?`, id).
		Scan(&sb.ID, &sb.CIDR, &vlan, &sb.Gateway, &sb.Description,
			&sb.DHCPRangeStart, &sb.DHCPRangeEnd, &sb.DHCPOptionsJSON, &sb.DNSServers,
			&sb.CreatedAt, &sb.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if vlan.Valid {
		v := int(vlan.Int64)
		sb.VLAN = &v
	}
	return &sb, nil
}

func (s *Store) CreateSubnet(ctx context.Context, sb model.Subnet) (*model.Subnet, error) {
	var vlan sql.NullInt64
	if sb.VLAN != nil {
		vlan = sql.NullInt64{Int64: int64(*sb.VLAN), Valid: true}
	}
	if sb.DHCPOptionsJSON == "" {
		sb.DHCPOptionsJSON = "{}"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO subnets(cidr, vlan, gateway, description,
		                     dhcp_range_start, dhcp_range_end, dhcp_options_json,
		                     dns_servers)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		sb.CIDR, vlan, sb.Gateway, sb.Description,
		sb.DHCPRangeStart, sb.DHCPRangeEnd, sb.DHCPOptionsJSON, sb.DNSServers)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetSubnet(ctx, id)
}

func (s *Store) UpdateSubnet(ctx context.Context, id int64, sb model.Subnet) error {
	var vlan sql.NullInt64
	if sb.VLAN != nil {
		vlan = sql.NullInt64{Int64: int64(*sb.VLAN), Valid: true}
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE subnets
		 SET cidr=?, vlan=?, gateway=?, description=?,
		     dhcp_range_start=?, dhcp_range_end=?, dns_servers=?,
		     updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		sb.CIDR, vlan, sb.Gateway, sb.Description,
		sb.DHCPRangeStart, sb.DHCPRangeEnd, sb.DNSServers, id)
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

func (s *Store) DeleteSubnet(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM subnets WHERE id=?`, id)
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
