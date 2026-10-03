package modules

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresVirtualKeyStore) LoadSSOSettings(ctx context.Context) (int64, []byte, error) {
	var revision int64
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT revision, payload FROM auth_sso_settings WHERE id = 1`).Scan(&revision, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, nil
	}
	return revision, payload, err
}

func (s *PostgresVirtualKeyStore) SaveSSOSettings(ctx context.Context, expected int64, payload []byte) error {
	var affected int64
	if expected == 0 {
		tag, err := s.pool.Exec(ctx, `INSERT INTO auth_sso_settings(id, revision, payload) VALUES(1, 1, $1) ON CONFLICT DO NOTHING`, payload)
		if err != nil {
			return ErrSSOUnavailable
		}
		affected = tag.RowsAffected()
	} else {
		tag, err := s.pool.Exec(ctx, `UPDATE auth_sso_settings SET revision = revision + 1, payload = $1, updated_at = now() WHERE id = 1 AND revision = $2`, payload, expected)
		if err != nil {
			return ErrSSOUnavailable
		}
		affected = tag.RowsAffected()
	}
	if affected != 1 {
		return ErrSSOConflict
	}
	return nil
}
