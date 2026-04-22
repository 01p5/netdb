package store

import (
	"context"
	"errors"

	"netdb/internal/model"
)

func (s *Store) ListZonesForSubnet(ctx context.Context, subnetID int64) ([]model.SubnetZoneLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT z.id, z.name, z.kind, z.default_ttl, z.description, z.created_at, z.updated_at, sz.role
		 FROM zones z
		 JOIN subnet_zones sz ON sz.zone_id = z.id
		 WHERE sz.subnet_id = ?
		 ORDER BY sz.role, z.name`, subnetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.SubnetZoneLink
	for rows.Next() {
		var l model.SubnetZoneLink
		if err := rows.Scan(&l.Zone.ID, &l.Zone.Name, &l.Zone.Kind, &l.Zone.DefaultTTL,
			&l.Zone.Description, &l.Zone.CreatedAt, &l.Zone.UpdatedAt, &l.Role); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) LinkSubnetZone(ctx context.Context, subnetID, zoneID int64, role string) error {
	if role != "forward" && role != "reverse" {
		return errors.New("role must be forward or reverse")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO subnet_zones(subnet_id, zone_id, role) VALUES(?, ?, ?)`,
		subnetID, zoneID, role)
	return err
}

func (s *Store) UnlinkSubnetZone(ctx context.Context, subnetID, zoneID int64, role string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM subnet_zones WHERE subnet_id=? AND zone_id=? AND role=?`,
		subnetID, zoneID, role)
	return err
}
