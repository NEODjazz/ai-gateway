package modules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type StoredVirtualKey struct {
	ID             string
	UserID         string
	TeamID         string
	Roles          []string
	AllowedModels  []string
	RateLimitRPM   int
	RateLimitTPM   int
	RotationFamily string
	RotatedFromID  string
	ExpiresAt      *time.Time
}

type VirtualKeyStore interface {
	Lookup(ctx context.Context, tokenHash string) (StoredVirtualKey, bool, error)
	Ready(ctx context.Context) error
	Close()
}

type PostgresVirtualKeyStore struct{ pool *pgxpool.Pool }

func NewPostgresVirtualKeyStore(dsn string) (*PostgresVirtualKeyStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("auth postgres dsn is required")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, errors.New("invalid auth postgres configuration")
	}
	return &PostgresVirtualKeyStore{pool: pool}, nil
}

func (s *PostgresVirtualKeyStore) Lookup(ctx context.Context, tokenHash string) (StoredVirtualKey, bool, error) {
	if s == nil || s.pool == nil {
		return StoredVirtualKey{}, false, errors.New("auth key store is not initialized")
	}
	var key StoredVirtualKey
	err := s.pool.QueryRow(ctx, `
		UPDATE auth_virtual_keys
		SET last_used_at = now()
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())
		RETURNING id, user_id, COALESCE(team_id, ''), roles, allowed_models,
		          rate_limit_rpm, rate_limit_tpm, rotation_family_id,
		          COALESCE(rotated_from_id, ''), expires_at`, tokenHash).Scan(
		&key.ID, &key.UserID, &key.TeamID, &key.Roles, &key.AllowedModels,
		&key.RateLimitRPM, &key.RateLimitTPM, &key.RotationFamily,
		&key.RotatedFromID, &key.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredVirtualKey{}, false, nil
	}
	if err != nil {
		return StoredVirtualKey{}, false, fmt.Errorf("lookup virtual key: %w", err)
	}
	return key, true, nil
}

func (s *PostgresVirtualKeyStore) Ready(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("auth key store is not initialized")
	}
	if err := s.pool.Ping(ctx); err != nil {
		return errors.New("auth postgres is unavailable")
	}
	var migrationExists bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('public.auth_virtual_keys') IS NOT NULL`).Scan(&migrationExists); err != nil || !migrationExists {
		return errors.New("auth virtual-key migration is not applied")
	}
	return nil
}

func (s *PostgresVirtualKeyStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *PostgresVirtualKeyStore) Create(ctx context.Context, key StoredVirtualKey, tokenHash string) error {
	if key.ID == "" || key.UserID == "" || tokenHash == "" {
		return errors.New("virtual key id, user id, and token hash are required")
	}
	family := key.RotationFamily
	if family == "" {
		family = key.ID
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth_virtual_keys
		(id, token_hash, user_id, team_id, roles, allowed_models, rate_limit_rpm,
		 rate_limit_tpm, rotation_family_id, rotated_from_id, expires_at)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,NULLIF($10,''),$11)`,
		key.ID, tokenHash, key.UserID, key.TeamID, nonNilStrings(key.Roles), nonNilStrings(key.AllowedModels),
		key.RateLimitRPM, key.RateLimitTPM, family, key.RotatedFromID, key.ExpiresAt)
	return err
}

func (s *PostgresVirtualKeyStore) Revoke(ctx context.Context, id string) (bool, error) {
	result, err := s.pool.Exec(ctx, `UPDATE auth_virtual_keys SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1 AND revoked_at IS NULL`, id)
	return result.RowsAffected() == 1, err
}

func (s *PostgresVirtualKeyStore) Rotate(ctx context.Context, oldID string, replacement StoredVirtualKey, tokenHash string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var family string
	if err := tx.QueryRow(ctx, `SELECT rotation_family_id FROM auth_virtual_keys WHERE id=$1 AND revoked_at IS NULL FOR UPDATE`, oldID).Scan(&family); err != nil {
		return err
	}
	if replacement.ID == "" || replacement.UserID == "" || tokenHash == "" {
		return errors.New("replacement key id, user id, and token hash are required")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_virtual_keys
		(id, token_hash, user_id, team_id, roles, allowed_models, rate_limit_rpm,
		 rate_limit_tpm, rotation_family_id, rotated_from_id, expires_at)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11)`,
		replacement.ID, tokenHash, replacement.UserID, replacement.TeamID,
		nonNilStrings(replacement.Roles), nonNilStrings(replacement.AllowedModels), replacement.RateLimitRPM,
		replacement.RateLimitTPM, family, oldID, replacement.ExpiresAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_virtual_keys SET revoked_at=now(), rotated_to_id=$2 WHERE id=$1`, oldID, replacement.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
