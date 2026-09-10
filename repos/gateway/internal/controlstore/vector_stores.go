package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/vectorstate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateVectorStore(ctx context.Context, store vectorstate.VectorStore, ownerQuota int) (vectorstate.VectorStore, error) {
	if s == nil || s.pool == nil {
		return vectorstate.VectorStore{}, vectorstate.ErrUnavailable
	}
	if store.ID == "" || store.OwnerKey == "" || store.Name == "" || store.ExpiresAfter < 0 || ownerQuota < 1 {
		return vectorstate.VectorStore{}, vectorstate.ErrInvalid
	}
	metadata, err := json.Marshal(store.Metadata)
	if err != nil {
		return vectorstate.VectorStore{}, vectorstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return vectorstate.VectorStore{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, store.OwnerKey); err != nil {
		return vectorstate.VectorStore{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_vector_stores WHERE owner_key=$1 AND (expires_at IS NULL OR expires_at>now())`, store.OwnerKey).Scan(&count); err != nil {
		return vectorstate.VectorStore{}, err
	}
	if count >= ownerQuota {
		return vectorstate.VectorStore{}, vectorstate.ErrQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_vector_stores
		(id,owner_key,name,metadata,expires_after_days,expires_at)
		VALUES ($1,$2,$3,$4::jsonb,$5,CASE WHEN $5>0 THEN now()+make_interval(days=>$5) ELSE NULL END)
		ON CONFLICT DO NOTHING`, store.ID, store.OwnerKey, store.Name, string(metadata), store.ExpiresAfter)
	if err != nil {
		return vectorstate.VectorStore{}, err
	}
	if command.RowsAffected() != 1 {
		return vectorstate.VectorStore{}, vectorstate.ErrConflict
	}
	created, err := getVectorStore(ctx, tx, store.OwnerKey, store.ID)
	if err != nil {
		return vectorstate.VectorStore{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return vectorstate.VectorStore{}, err
	}
	return created, nil
}

func (s *PostgresStore) ListVectorStores(ctx context.Context, owner string, limit int, after string) ([]vectorstate.VectorStore, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", vectorstate.ErrUnavailable
	}
	if owner == "" || limit < 1 || limit > 100 {
		return nil, "", vectorstate.ErrInvalid
	}
	var cursorTime *time.Time
	var cursorID string
	if after != "" {
		var createdAt time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at,id FROM gateway_vector_stores WHERE owner_key=$1 AND id=$2`, owner, after).Scan(&createdAt, &cursorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", vectorstate.ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		cursorTime = &createdAt
	}
	rows, err := s.pool.Query(ctx, `SELECT id,owner_key,name,metadata,expires_after_days,created_at,last_active_at,expires_at,
		(expires_at IS NOT NULL AND expires_at<=now())
		FROM gateway_vector_stores WHERE owner_key=$1
		AND ($2::timestamptz IS NULL OR (created_at,id)<($2::timestamptz,$3))
		ORDER BY created_at DESC,id DESC LIMIT $4`, owner, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	stores := make([]vectorstate.VectorStore, 0, limit+1)
	for rows.Next() {
		store, scanErr := scanVectorStore(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		stores = append(stores, store)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(stores) > limit {
		next = stores[limit-1].ID
		stores = stores[:limit]
	}
	return stores, next, nil
}

func (s *PostgresStore) GetVectorStore(ctx context.Context, owner, id string) (vectorstate.VectorStore, error) {
	if s == nil || s.pool == nil {
		return vectorstate.VectorStore{}, vectorstate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return vectorstate.VectorStore{}, vectorstate.ErrInvalid
	}
	return getVectorStore(ctx, s.pool, owner, id)
}

func (s *PostgresStore) UpdateVectorStore(ctx context.Context, owner, id string, update vectorstate.Update) (vectorstate.VectorStore, error) {
	if s == nil || s.pool == nil {
		return vectorstate.VectorStore{}, vectorstate.ErrUnavailable
	}
	if owner == "" || id == "" || update.Name == nil && update.Metadata == nil && update.ExpiresAfter == nil {
		return vectorstate.VectorStore{}, vectorstate.ErrInvalid
	}
	var metadata any
	if update.Metadata != nil {
		encoded, err := json.Marshal(*update.Metadata)
		if err != nil {
			return vectorstate.VectorStore{}, vectorstate.ErrInvalid
		}
		metadata = string(encoded)
	}
	command, err := s.pool.Exec(ctx, `UPDATE gateway_vector_stores SET
		name=COALESCE($3,name), metadata=COALESCE($4::jsonb,metadata),
		expires_after_days=COALESCE($5,expires_after_days),
		expires_at=CASE WHEN $5 IS NULL THEN expires_at WHEN $5=0 THEN NULL ELSE last_active_at+make_interval(days=>$5) END,
		updated_at=now() WHERE owner_key=$1 AND id=$2`, owner, id, update.Name, metadata, update.ExpiresAfter)
	if err != nil {
		return vectorstate.VectorStore{}, err
	}
	if command.RowsAffected() != 1 {
		return vectorstate.VectorStore{}, vectorstate.ErrNotFound
	}
	return getVectorStore(ctx, s.pool, owner, id)
}

func (s *PostgresStore) DeleteVectorStore(ctx context.Context, owner, id string) error {
	if s == nil || s.pool == nil {
		return vectorstate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return vectorstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_vector_stores WHERE owner_key=$1 AND id=$2`, owner, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return vectorstate.ErrNotFound
	}
	return nil
}

type vectorStoreQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getVectorStore(ctx context.Context, query vectorStoreQuerier, owner, id string) (vectorstate.VectorStore, error) {
	store, err := scanVectorStore(query.QueryRow(ctx, `SELECT id,owner_key,name,metadata,expires_after_days,created_at,last_active_at,expires_at,
		(expires_at IS NOT NULL AND expires_at<=now())
		FROM gateway_vector_stores WHERE owner_key=$1 AND id=$2`, owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return vectorstate.VectorStore{}, vectorstate.ErrNotFound
	}
	return store, err
}

type vectorStoreScanner interface{ Scan(...any) error }

func scanVectorStore(row vectorStoreScanner) (vectorstate.VectorStore, error) {
	var store vectorstate.VectorStore
	var metadata []byte
	var expired bool
	if err := row.Scan(&store.ID, &store.OwnerKey, &store.Name, &metadata, &store.ExpiresAfter, &store.CreatedAt, &store.LastActiveAt, &store.ExpiresAt, &expired); err != nil {
		return vectorstate.VectorStore{}, err
	}
	if err := json.Unmarshal(metadata, &store.Metadata); err != nil || store.Metadata == nil {
		return vectorstate.VectorStore{}, vectorstate.ErrInvalid
	}
	store.Status = "completed"
	if expired {
		store.Status = "expired"
	}
	return store, nil
}
