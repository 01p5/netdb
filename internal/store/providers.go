package store

import (
	"context"
	"database/sql"
	"errors"

	"netdb/internal/model"
)

var validProviderKinds = map[string]bool{"cloudflare": true, "technitium": true}

func (s *Store) ListProviders(ctx context.Context) ([]model.Provider, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, kind, config_json, enabled, created_at, updated_at
		 FROM dns_providers ORDER BY name`)
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

func (s *Store) GetProvider(ctx context.Context, id int64) (*model.Provider, error) {
	var p model.Provider
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, kind, config_json, enabled, created_at, updated_at
		 FROM dns_providers WHERE id=?`, id).
		Scan(&p.ID, &p.Name, &p.Kind, &p.ConfigJSON, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) CreateProvider(ctx context.Context, name, kind, configJSON string, enabled bool) (*model.Provider, error) {
	if !validProviderKinds[kind] {
		return nil, errors.New("provider kind must be cloudflare or technitium")
	}
	if configJSON == "" {
		configJSON = "{}"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO dns_providers(name, kind, config_json, enabled) VALUES(?, ?, ?, ?)`,
		name, kind, configJSON, enabled)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetProvider(ctx, id)
}

func (s *Store) DeleteProvider(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM dns_providers WHERE id=?`, id)
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
