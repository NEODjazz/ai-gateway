package modules

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresVirtualKeyStore) ListSSOConnections(ctx context.Context) ([]SSOConnection, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,provider,COALESCE(organization_id,'') FROM auth_sso_connections ORDER BY id LIMIT 17`)
	if err != nil {
		return nil, ErrSSOUnavailable
	}
	defer rows.Close()
	result := []SSOConnection{}
	for rows.Next() {
		var connection SSOConnection
		if err := rows.Scan(&connection.ID, &connection.Name, &connection.Provider, &connection.OrganizationID); err != nil {
			return nil, ErrSSOUnavailable
		}
		result = append(result, connection)
	}
	if rows.Err() != nil || len(result) > maxSSOConnections {
		return nil, ErrSSOUnavailable
	}
	return result, nil
}

func (s *PostgresVirtualKeyStore) CreateSSOConnection(ctx context.Context, connection SSOConnection) error {
	if !validSSOConnection(connection) {
		return ErrSSOConfiguration
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ErrSSOUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(731503153)`); err != nil {
		return ErrSSOUnavailable
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_sso_connections`).Scan(&count); err != nil {
		return ErrSSOUnavailable
	}
	if count >= maxSSOConnections {
		return ErrSSOConfiguration
	}
	if connection.OrganizationID != "" {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_organizations WHERE id=$1 AND status='active')`, connection.OrganizationID).Scan(&active); err != nil {
			return ErrSSOUnavailable
		}
		if !active {
			return ErrSSOConfiguration
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO auth_sso_connections(id,name,provider,organization_id) VALUES($1,$2,$3,NULLIF($4,'')) ON CONFLICT DO NOTHING`, connection.ID, connection.Name, connection.Provider, connection.OrganizationID)
	if err != nil {
		return ErrSSOUnavailable
	}
	if tag.RowsAffected() != 1 {
		return ErrSSOConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return ErrSSOUnavailable
	}
	return nil
}

func (s *PostgresVirtualKeyStore) LoadSSOConnectionSettings(ctx context.Context, id string) (int64, []byte, error) {
	var revision int64
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT revision,payload FROM auth_sso_connections WHERE id=$1`, id).Scan(&revision, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrSSOConfiguration
	}
	if err != nil {
		return 0, nil, ErrSSOUnavailable
	}
	return revision, payload, nil
}

func (s *PostgresVirtualKeyStore) SaveSSOConnectionSettings(ctx context.Context, id string, expected int64, payload []byte) error {
	tag, err := s.pool.Exec(ctx, `UPDATE auth_sso_connections SET revision=revision+1,payload=$1,updated_at=now() WHERE id=$2 AND revision=$3`, payload, id, expected)
	if err != nil {
		return ErrSSOUnavailable
	}
	if tag.RowsAffected() != 1 {
		return ErrSSOConflict
	}
	return nil
}
