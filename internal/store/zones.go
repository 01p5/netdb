package store

import (
	"context"
	"database/sql"
	"errors"

	"netdb/internal/model"
)

func (s *Store) ListZones(ctx context.Context) ([]model.Zone, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, kind, default_ttl, description, created_at, updated_at
		 FROM zones ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Zone
	for rows.Next() {
		var z model.Zone
		if err := rows.Scan(&z.ID, &z.Name, &z.Kind, &z.DefaultTTL, &z.Description,
			&z.CreatedAt, &z.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

func (s *Store) GetZone(ctx context.Context, id int64) (*model.Zone, error) {
	var z model.Zone
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, kind, default_ttl, description, created_at, updated_at
		 FROM zones WHERE id=?`, id).
		Scan(&z.ID, &z.Name, &z.Kind, &z.DefaultTTL, &z.Description, &z.CreatedAt, &z.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &z, nil
}

func (s *Store) CreateZone(ctx context.Context, name, kind, description string, defaultTTL int) (*model.Zone, error) {
	if kind != "forward" && kind != "reverse" {
		return nil, errors.New("zone kind must be forward or reverse")
	}
	if defaultTTL <= 0 {
		defaultTTL = 300
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO zones(name, kind, default_ttl, description) VALUES(?, ?, ?, ?)`,
		name, kind, defaultTTL, description)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetZone(ctx, id)
}

func (s *Store) DeleteZone(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM zones WHERE id=?`, id)
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

// ListZonesForProvider returns zones that push to a given provider.
func (s *Store) ListZonesForProvider(ctx context.Context, providerID int64) ([]model.Zone, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT z.id, z.name, z.kind, z.default_ttl, z.description, z.created_at, z.updated_at
		 FROM zones z
		 JOIN zone_providers zp ON zp.zone_id = z.id
		 WHERE zp.provider_id = ?
		 ORDER BY z.name`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Zone
	for rows.Next() {
		var z model.Zone
		if err := rows.Scan(&z.ID, &z.Name, &z.Kind, &z.DefaultTTL, &z.Description,
			&z.CreatedAt, &z.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

// ListProvidersForZone returns providers this zone is linked to.
func (s *Store) ListProvidersForZone(ctx context.Context, zoneID int64) ([]model.Provider, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, p.name, p.kind, p.config_json, p.enabled, p.created_at, p.updated_at
		 FROM dns_providers p
		 JOIN zone_providers zp ON zp.provider_id = p.id
		 WHERE zp.zone_id = ?
		 ORDER BY p.name`, zoneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Provider
	for rows.Next() {
		var p model.Provider
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.ConfigJSON, &p.Enabled,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) LinkZoneProvider(ctx context.Context, zoneID, providerID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO zone_providers(zone_id, provider_id) VALUES(?, ?)`,
		zoneID, providerID)
	return err
}

func (s *Store) UnlinkZoneProvider(ctx context.Context, zoneID, providerID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM zone_providers WHERE zone_id=? AND provider_id=?`,
		zoneID, providerID)
	return err
}
