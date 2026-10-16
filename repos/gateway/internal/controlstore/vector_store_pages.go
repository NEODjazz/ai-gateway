package controlstore

import (
	"context"
	"fmt"
	"strings"

	"ai-gateway-gateway/internal/vectorstate"
	"github.com/jackc/pgx/v5"
)

const vectorStorePageFilter = ` FROM gateway_vector_stores v WHERE v.owner_key=$1
	AND ($2='' OR strpos(lower(v.id || ' ' || v.name || ' ' || COALESCE(v.metadata->>'description','')),lower($2))>0)
	AND ($3='' OR CASE WHEN v.expires_at IS NOT NULL AND v.expires_at<=now() THEN 'expired' ELSE 'completed' END=$3)`

func (s *PostgresStore) ListVectorStorePage(ctx context.Context, owner string, q vectorstate.PageQuery) ([]vectorstate.VectorStore, int, error) {
	if s == nil || s.pool == nil {
		return nil, 0, vectorstate.ErrUnavailable
	}
	if owner == "" || !q.Valid() {
		return nil, 0, vectorstate.ErrInvalid
	}
	column := map[string]string{"id": "v.id", "name": "lower(v.name)", "created_at": "v.created_at", "last_active_at": "v.last_active_at"}[q.SortBy]
	order := column + " " + strings.ToUpper(q.SortOrder) + ", v.id " + strings.ToUpper(q.SortOrder)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var total int
	if err = tx.QueryRow(ctx, `SELECT count(*)`+vectorStorePageFilter, owner, q.Search, q.Status).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT v.id,v.owner_key,v.name,v.metadata,v.expires_after_days,v.created_at,v.last_active_at,v.expires_at,
		(v.expires_at IS NOT NULL AND v.expires_at<=now()),
		COALESCE((SELECT sum(f.bytes) FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.vector_store_id=v.id AND a.owner_key=v.owner_key),0),
		(SELECT count(*) FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.vector_store_id=v.id AND a.owner_key=v.owner_key)`+vectorStorePageFilter+fmt.Sprintf(" ORDER BY %s OFFSET $4 LIMIT $5", order), owner, q.Search, q.Status, q.Offset, q.Limit)
	if err != nil {
		return nil, 0, err
	}
	stores := make([]vectorstate.VectorStore, 0, q.Limit)
	for rows.Next() {
		store, scanErr := scanVectorStore(rows)
		if scanErr != nil {
			rows.Close()
			return nil, 0, scanErr
		}
		stores = append(stores, store)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return stores, total, nil
}
