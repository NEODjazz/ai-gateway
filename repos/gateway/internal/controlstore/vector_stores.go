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
	rows, err := s.pool.Query(ctx, `SELECT v.id,v.owner_key,v.name,v.metadata,v.expires_after_days,v.created_at,v.last_active_at,v.expires_at,
		(v.expires_at IS NOT NULL AND v.expires_at<=now()),
		COALESCE((SELECT sum(f.bytes) FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.vector_store_id=v.id AND a.owner_key=v.owner_key),0),
		(SELECT count(*) FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.vector_store_id=v.id AND a.owner_key=v.owner_key)
		FROM gateway_vector_stores v WHERE v.owner_key=$1
		AND ($2::timestamptz IS NULL OR (v.created_at,v.id)<($2::timestamptz,$3))
		ORDER BY v.created_at DESC,v.id DESC LIMIT $4`, owner, cursorTime, cursorID, limit+1)
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

func (s *PostgresStore) AttachVectorStoreFile(ctx context.Context, owner, vectorStoreID, fileID string, attributes map[string]string, quota int, byteQuota int64) (vectorstate.File, error) {
	if s == nil || s.pool == nil {
		return vectorstate.File{}, vectorstate.ErrUnavailable
	}
	if owner == "" || vectorStoreID == "" || fileID == "" || quota < 1 || byteQuota < 1 {
		return vectorstate.File{}, vectorstate.ErrInvalid
	}
	if attributes == nil {
		attributes = map[string]string{}
	}
	encodedAttributes, err := json.Marshal(attributes)
	if err != nil {
		return vectorstate.File{}, vectorstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return vectorstate.File{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var expired bool
	if err = tx.QueryRow(ctx, `SELECT expires_at IS NOT NULL AND expires_at<=now() FROM gateway_vector_stores WHERE owner_key=$1 AND id=$2 FOR UPDATE`, owner, vectorStoreID).Scan(&expired); errors.Is(err, pgx.ErrNoRows) || expired {
		return vectorstate.File{}, vectorstate.ErrNotFound
	} else if err != nil {
		return vectorstate.File{}, err
	}
	var bytes int64
	if err = tx.QueryRow(ctx, `SELECT bytes FROM gateway_files WHERE owner_key=$1 AND id=$2 AND (expires_at IS NULL OR expires_at>now())`, owner, fileID).Scan(&bytes); errors.Is(err, pgx.ErrNoRows) {
		return vectorstate.File{}, vectorstate.ErrFileNotFound
	} else if err != nil {
		return vectorstate.File{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM gateway_vector_store_files WHERE owner_key=$1 AND vector_store_id=$2 AND file_id=$3)`, owner, vectorStoreID, fileID).Scan(&exists); err != nil {
		return vectorstate.File{}, err
	}
	if exists {
		return vectorstate.File{}, vectorstate.ErrConflict
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.owner_key=$1 AND a.vector_store_id=$2`, owner, vectorStoreID).Scan(&count); err != nil {
		return vectorstate.File{}, err
	}
	if count >= quota {
		return vectorstate.File{}, vectorstate.ErrFileQuotaExceeded
	}
	var usedBytes int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(f.bytes),0) FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.owner_key=$1 AND a.vector_store_id=$2`, owner, vectorStoreID).Scan(&usedBytes); err != nil {
		return vectorstate.File{}, err
	}
	if usedBytes < 0 || bytes < 0 || usedBytes > byteQuota || bytes > byteQuota-usedBytes {
		return vectorstate.File{}, vectorstate.ErrByteQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_vector_store_files (vector_store_id,file_id,owner_key,attributes) VALUES ($1,$2,$3,$4::jsonb) ON CONFLICT DO NOTHING`, vectorStoreID, fileID, owner, string(encodedAttributes))
	if err != nil {
		return vectorstate.File{}, err
	}
	if command.RowsAffected() != 1 {
		return vectorstate.File{}, vectorstate.ErrConflict
	}
	file, err := getVectorStoreFile(ctx, tx, owner, vectorStoreID, fileID)
	if err != nil {
		return vectorstate.File{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE gateway_vector_stores SET last_active_at=now(),updated_at=now(),expires_at=CASE WHEN expires_after_days>0 THEN now()+make_interval(days=>expires_after_days) ELSE NULL END WHERE owner_key=$1 AND id=$2`, owner, vectorStoreID); err != nil {
		return vectorstate.File{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return vectorstate.File{}, err
	}
	file.Bytes = bytes
	return file, nil
}

func (s *PostgresStore) ListVectorStoreFiles(ctx context.Context, owner, vectorStoreID string, limit int, after string) ([]vectorstate.File, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", vectorstate.ErrUnavailable
	}
	if owner == "" || vectorStoreID == "" || limit < 1 || limit > 100 {
		return nil, "", vectorstate.ErrInvalid
	}
	if _, err := s.GetVectorStore(ctx, owner, vectorStoreID); err != nil {
		return nil, "", err
	}
	var cursorTime *time.Time
	var cursorID string
	if after != "" {
		var createdAt time.Time
		err := s.pool.QueryRow(ctx, `SELECT a.created_at,a.file_id FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.owner_key=$1 AND a.vector_store_id=$2 AND a.file_id=$3`, owner, vectorStoreID, after).Scan(&createdAt, &cursorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", vectorstate.ErrFileNotFound
		}
		if err != nil {
			return nil, "", err
		}
		cursorTime = &createdAt
	}
	rows, err := s.pool.Query(ctx, `SELECT a.vector_store_id,a.file_id,a.owner_key,a.status,f.bytes,a.attributes,a.created_at FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.owner_key=$1 AND a.vector_store_id=$2 AND ($3::timestamptz IS NULL OR (a.created_at,a.file_id)<($3::timestamptz,$4)) ORDER BY a.created_at DESC,a.file_id DESC LIMIT $5`, owner, vectorStoreID, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	files := make([]vectorstate.File, 0, limit+1)
	for rows.Next() {
		file, scanErr := scanVectorStoreFile(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(files) > limit {
		next = files[limit-1].FileID
		files = files[:limit]
	}
	return files, next, nil
}

func (s *PostgresStore) GetVectorStoreFile(ctx context.Context, owner, vectorStoreID, fileID string) (vectorstate.File, error) {
	if s == nil || s.pool == nil {
		return vectorstate.File{}, vectorstate.ErrUnavailable
	}
	if owner == "" || vectorStoreID == "" || fileID == "" {
		return vectorstate.File{}, vectorstate.ErrInvalid
	}
	return getVectorStoreFile(ctx, s.pool, owner, vectorStoreID, fileID)
}

func (s *PostgresStore) UpdateVectorStoreFile(ctx context.Context, owner, vectorStoreID, fileID string, attributes map[string]string) (vectorstate.File, error) {
	if s == nil || s.pool == nil {
		return vectorstate.File{}, vectorstate.ErrUnavailable
	}
	if owner == "" || vectorStoreID == "" || fileID == "" || attributes == nil {
		return vectorstate.File{}, vectorstate.ErrInvalid
	}
	encodedAttributes, err := json.Marshal(attributes)
	if err != nil {
		return vectorstate.File{}, vectorstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return vectorstate.File{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `UPDATE gateway_vector_store_files SET attributes=$4::jsonb WHERE owner_key=$1 AND vector_store_id=$2 AND file_id=$3`, owner, vectorStoreID, fileID, string(encodedAttributes))
	if err != nil {
		return vectorstate.File{}, err
	}
	if command.RowsAffected() != 1 {
		return vectorstate.File{}, vectorstate.ErrFileNotFound
	}
	file, err := getVectorStoreFile(ctx, tx, owner, vectorStoreID, fileID)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, vectorstate.ErrFileNotFound) {
		return vectorstate.File{}, vectorstate.ErrFileNotFound
	}
	if err != nil {
		return vectorstate.File{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE gateway_vector_stores SET last_active_at=now(),updated_at=now(),expires_at=CASE WHEN expires_after_days>0 THEN now()+make_interval(days=>expires_after_days) ELSE NULL END WHERE owner_key=$1 AND id=$2`, owner, vectorStoreID); err != nil {
		return vectorstate.File{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return vectorstate.File{}, err
	}
	return file, nil
}

func (s *PostgresStore) DeleteVectorStoreFile(ctx context.Context, owner, vectorStoreID, fileID string) error {
	if s == nil || s.pool == nil {
		return vectorstate.ErrUnavailable
	}
	if owner == "" || vectorStoreID == "" || fileID == "" {
		return vectorstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_vector_store_files WHERE owner_key=$1 AND vector_store_id=$2 AND file_id=$3`, owner, vectorStoreID, fileID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return vectorstate.ErrFileNotFound
	}
	return nil
}

type vectorStoreFileQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getVectorStoreFile(ctx context.Context, query vectorStoreFileQuerier, owner, vectorStoreID, fileID string) (vectorstate.File, error) {
	file, err := scanVectorStoreFile(query.QueryRow(ctx, `SELECT a.vector_store_id,a.file_id,a.owner_key,a.status,f.bytes,a.attributes,a.created_at FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.owner_key=$1 AND a.vector_store_id=$2 AND a.file_id=$3`, owner, vectorStoreID, fileID))
	if errors.Is(err, pgx.ErrNoRows) {
		return vectorstate.File{}, vectorstate.ErrFileNotFound
	}
	return file, err
}

type vectorStoreFileScanner interface{ Scan(...any) error }

func scanVectorStoreFile(row vectorStoreFileScanner) (vectorstate.File, error) {
	var file vectorstate.File
	var attributes []byte
	err := row.Scan(&file.VectorStoreID, &file.FileID, &file.OwnerKey, &file.Status, &file.Bytes, &attributes, &file.CreatedAt)
	if err == nil {
		err = json.Unmarshal(attributes, &file.Attributes)
	}
	return file, err
}

type vectorStoreQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getVectorStore(ctx context.Context, query vectorStoreQuerier, owner, id string) (vectorstate.VectorStore, error) {
	store, err := scanVectorStore(query.QueryRow(ctx, `SELECT v.id,v.owner_key,v.name,v.metadata,v.expires_after_days,v.created_at,v.last_active_at,v.expires_at,
		(v.expires_at IS NOT NULL AND v.expires_at<=now()),
		COALESCE((SELECT sum(f.bytes) FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.vector_store_id=v.id AND a.owner_key=v.owner_key),0),
		(SELECT count(*) FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.vector_store_id=v.id AND a.owner_key=v.owner_key)
		FROM gateway_vector_stores v WHERE v.owner_key=$1 AND v.id=$2`, owner, id))
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
	if err := row.Scan(&store.ID, &store.OwnerKey, &store.Name, &metadata, &store.ExpiresAfter, &store.CreatedAt, &store.LastActiveAt, &store.ExpiresAt, &expired, &store.UsageBytes, &store.FileCount); err != nil {
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
