package modules

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresVirtualKeyStore) CreateSSOSession(ctx context.Context, hash, login, profile, user string, expires int64, payload []byte) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ErrSSOUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize database admission across replicas; IdP I/O completes beforehand.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(731503152)"); err != nil {
		return ErrSSOUnavailable
	}
	if _, err := tx.Exec(ctx, "DELETE FROM auth_sso_sessions WHERE expires_at <= now()"); err != nil {
		return ErrSSOUnavailable
	}
	if _, err := tx.Exec(ctx, "DELETE FROM auth_sso_logins WHERE expires_at <= now()"); err != nil {
		return ErrSSOUnavailable
	}
	var logins int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM auth_sso_logins").Scan(&logins); err != nil || logins >= 20000 {
		return ErrSSOUnavailable
	}
	var total, userCount, profileCount int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE user_id=$1),count(*) FILTER(WHERE profile_id=$2) FROM auth_sso_sessions`, user, profile).Scan(&total, &userCount, &profileCount); err != nil {
		return ErrSSOUnavailable
	}
	if total >= 10000 || userCount >= 16 || profileCount >= 1000 {
		return ErrSSOUnavailable
	}
	if _, err := tx.Exec(ctx, `INSERT INTO auth_sso_logins(nonce_hash,expires_at) VALUES($1,now()+interval '10 minutes')`, login); err != nil {
		return ErrUnauthorized
	}
	if _, err := tx.Exec(ctx, `INSERT INTO auth_sso_sessions(token_hash,profile_id,user_id,expires_at,payload) VALUES($1,$2,$3,$4,$5)`, hash, profile, user, time.Unix(expires, 0), payload); err != nil {
		return ErrSSOUnavailable
	}
	return tx.Commit(ctx)
}

func (s *PostgresVirtualKeyStore) SSOSessionsReady(ctx context.Context) error {
	var ready bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('auth_sso_sessions') IS NOT NULL AND to_regclass('auth_sso_logins') IS NOT NULL`).Scan(&ready); err != nil || !ready {
		return ErrSSOUnavailable
	}
	return nil
}
func (s *PostgresVirtualKeyStore) LoadSSOSession(ctx context.Context, hash string, now int64) ([]byte, error) {
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM auth_sso_sessions WHERE token_hash=$1 AND expires_at>$2`, hash, time.Unix(now, 0)).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, ErrSSOUnavailable
	}
	return payload, nil
}
func (s *PostgresVirtualKeyStore) RevokeSSOSession(ctx context.Context, hash string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM auth_sso_sessions WHERE token_hash=$1", hash)
	return err
}
