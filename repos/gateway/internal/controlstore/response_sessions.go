package controlstore

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ResponseSessions stores deployment affinity and immutable response ownership
// independently of the optional Redis cache.
type ResponseSessions struct {
	pool *pgxpool.Pool
}

func (s *PostgresStore) ResponseSessions() *ResponseSessions {
	if s == nil || s.pool == nil {
		return nil
	}
	return &ResponseSessions{pool: s.pool}
}

func (s *ResponseSessions) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := s.validKey(key); err != nil {
		return nil, false, err
	}
	var value []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM gateway_response_sessions WHERE key=$1 AND expires_at>now()`, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func (s *ResponseSessions) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := s.validRecord(key, value, ttl); err != nil {
		return err
	}
	if err := s.cleanupExpired(ctx); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO gateway_response_sessions (key,value,expires_at)
		VALUES ($1,$2,now()+$3::bigint*interval '1 microsecond')
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value,expires_at=EXCLUDED.expires_at`, key, value, ttl.Microseconds())
	return err
}

func (s *ResponseSessions) SetIfAbsentOrEqual(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	if err := s.validRecord(key, value, ttl); err != nil {
		return false, err
	}
	if err := s.cleanupExpired(ctx); err != nil {
		return false, err
	}
	command, err := s.pool.Exec(ctx, `INSERT INTO gateway_response_sessions (key,value,expires_at)
		VALUES ($1,$2,now()+$3::bigint*interval '1 microsecond')
		ON CONFLICT (key) DO UPDATE SET
		value=EXCLUDED.value,
		expires_at=CASE WHEN gateway_response_sessions.expires_at<=now()
			THEN EXCLUDED.expires_at ELSE gateway_response_sessions.expires_at END
		WHERE gateway_response_sessions.expires_at<=now() OR gateway_response_sessions.value=EXCLUDED.value`, key, value, ttl.Microseconds())
	return command.RowsAffected() == 1, err
}

func (s *ResponseSessions) DeleteIfEqual(ctx context.Context, key string, value []byte) (bool, error) {
	if err := s.validRecord(key, value, time.Millisecond); err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current []byte
	var expired bool
	err = tx.QueryRow(ctx, `SELECT value,expires_at<=now() FROM gateway_response_sessions WHERE key=$1 FOR UPDATE`, key).Scan(&current, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if !expired && !bytes.Equal(current, value) {
		return false, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway_response_sessions WHERE key=$1`, key); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (s *ResponseSessions) validKey(key string) error {
	if s == nil || s.pool == nil {
		return errors.New("response session store is unavailable")
	}
	if len(key) < 1 || len(key) > 256 {
		return errors.New("invalid response session key")
	}
	return nil
}

func (s *ResponseSessions) validRecord(key string, value []byte, ttl time.Duration) error {
	if err := s.validKey(key); err != nil {
		return err
	}
	if len(value) < 1 || len(value) > 4096 || ttl < time.Millisecond {
		return errors.New("invalid response session record")
	}
	return nil
}

func (s *ResponseSessions) cleanupExpired(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM gateway_response_sessions WHERE key IN
		(SELECT key FROM gateway_response_sessions WHERE expires_at<=now() ORDER BY expires_at LIMIT 100)`)
	return err
}
