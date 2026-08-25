package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"ai-gateway-gateway/internal/provider"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const revisionCacheKey = "provider-control-plane-revision"

type RevisionCache interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
}

type PostgresStore struct {
	pool  *pgxpool.Pool
	cache RevisionCache
}

func NewPostgresStore(ctx context.Context, dsn string, cache RevisionCache) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &PostgresStore{pool: pool, cache: cache}, nil
}

func (s *PostgresStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *PostgresStore) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("control plane postgres store is unavailable")
	}
	return s.pool.Ping(ctx)
}

func (s *PostgresStore) Load(ctx context.Context) (provider.ControlPlaneSnapshot, bool, error) {
	if s == nil || s.pool == nil {
		return provider.ControlPlaneSnapshot{}, false, errors.New("control plane postgres store is unavailable")
	}
	var revision int64
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT revision, payload FROM gateway_control_plane_state WHERE singleton = TRUE`).Scan(&revision, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return provider.ControlPlaneSnapshot{}, false, nil
	}
	if err != nil {
		return provider.ControlPlaneSnapshot{}, false, err
	}
	var snapshot provider.ControlPlaneSnapshot
	if len(payload) > 0 && string(payload) != "{}" {
		if err := json.Unmarshal(payload, &snapshot); err != nil {
			return provider.ControlPlaneSnapshot{}, false, err
		}
	}
	snapshot.Revision = revision
	return snapshot, true, nil
}

func (s *PostgresStore) Save(ctx context.Context, expectedRevision int64, snapshot provider.ControlPlaneSnapshot) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("control plane postgres store is unavailable")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current int64
	err = tx.QueryRow(ctx, `SELECT revision FROM gateway_control_plane_state WHERE singleton = TRUE FOR UPDATE`).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		if expectedRevision != 0 {
			return 0, provider.ErrControlPlaneConflict
		}
		current = 0
	} else if err != nil {
		return 0, err
	}
	if current != expectedRevision {
		return current, provider.ErrControlPlaneConflict
	}
	newRevision := current + 1
	snapshot.Revision = newRevision
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return 0, err
	}
	if len(payload) > 4<<20 {
		return 0, errors.New("control plane snapshot exceeds 4 MiB")
	}
	_, err = tx.Exec(ctx, `INSERT INTO gateway_control_plane_state (singleton, revision, payload, updated_at)
		VALUES (TRUE, $1, $2, now())
		ON CONFLICT (singleton) DO UPDATE SET revision = EXCLUDED.revision, payload = EXCLUDED.payload, updated_at = now()`, newRevision, payload)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if s.cache != nil {
		_ = s.cache.Set(ctx, revisionCacheKey, []byte(strconv.FormatInt(newRevision, 10)), 0)
	}
	return newRevision, nil
}

func (s *PostgresStore) Revision(ctx context.Context) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("control plane postgres store is unavailable")
	}
	var postgresRevision int64
	if err := s.pool.QueryRow(ctx, `SELECT revision FROM gateway_control_plane_state WHERE singleton = TRUE`).Scan(&postgresRevision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	if s.cache != nil {
		if payload, found, err := s.cache.Get(ctx, revisionCacheKey); err == nil && found {
			if cached, parseErr := strconv.ParseInt(string(payload), 10, 64); parseErr == nil && cached > postgresRevision {
				return cached, nil
			}
		}
	}
	return postgresRevision, nil
}
