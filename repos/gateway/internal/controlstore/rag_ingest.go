package controlstore

import (
	"context"
	"encoding/json"
	"errors"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/ragstate"
	"ai-gateway-gateway/internal/vectorstate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) IngestRAG(ctx context.Context, request ragstate.IngestRequest) (ragstate.IngestResult, error) {
	if s == nil || s.pool == nil {
		return ragstate.IngestResult{}, vectorstate.ErrUnavailable
	}
	createFile := request.File != nil
	createStore := request.VectorStore != nil
	if request.OwnerKey == "" || createFile == (request.FileID != "") || createStore == (request.VectorStoreID != "") ||
		request.FileOwnerQuota < 1 || request.VectorStoreQuota < 1 || request.VectorStoreFiles < 1 || request.VectorStoreBytes < 1 ||
		vectorstate.ValidateAttributes(request.Attributes) != "" {
		return ragstate.IngestResult{}, vectorstate.ErrInvalid
	}
	if createFile {
		file := request.File
		if file.ID == "" || file.OwnerKey != request.OwnerKey || file.Filename == "" || file.Purpose != "assistants" || file.ContentType == "" || file.Bytes < 1 || file.Bytes != int64(len(file.Content)) ||
			file.ExpiresAfterSeconds != 0 && (file.ExpiresAfterSeconds < filestate.MinimumExpirySeconds || file.ExpiresAfterSeconds > filestate.MaximumExpirySeconds) {
			return ragstate.IngestResult{}, filestate.ErrInvalid
		}
		request.FileID = file.ID
	}
	if createStore {
		store := request.VectorStore
		if store.ID == "" || store.OwnerKey != request.OwnerKey || store.Name == "" || store.ExpiresAfter < 0 || store.ExpiresAfter > 365 {
			return ragstate.IngestResult{}, vectorstate.ErrInvalid
		}
		request.VectorStoreID = store.ID
	}
	if request.Attributes == nil {
		request.Attributes = map[string]any{}
	}
	attributes, err := json.Marshal(request.Attributes)
	if err != nil {
		return ragstate.IngestResult{}, vectorstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ragstate.IngestResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if createFile {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, request.OwnerKey); err != nil {
			return ragstate.IngestResult{}, err
		}
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, request.OwnerKey); err != nil {
		return ragstate.IngestResult{}, err
	}

	file := filestate.File{OwnerKey: request.OwnerKey}
	if createFile {
		file = *request.File
		if _, err = tx.Exec(ctx, `DELETE FROM gateway_files WHERE owner_key=$1 AND expires_at IS NOT NULL AND expires_at<=now()`, request.OwnerKey); err != nil {
			return ragstate.IngestResult{}, err
		}
		var used int64
		if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(bytes),0) FROM gateway_files WHERE owner_key=$1`, request.OwnerKey).Scan(&used); err != nil {
			return ragstate.IngestResult{}, err
		}
		if file.Bytes > request.FileOwnerQuota || used < 0 || used > request.FileOwnerQuota-file.Bytes {
			return ragstate.IngestResult{}, filestate.ErrQuotaExceeded
		}
		command, insertErr := tx.Exec(ctx, `INSERT INTO gateway_files (id,owner_key,filename,purpose,content_type,bytes,content,expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,CASE WHEN $8::bigint>0 THEN now()+make_interval(secs=>$8) ELSE NULL END) ON CONFLICT DO NOTHING`,
			file.ID, file.OwnerKey, file.Filename, file.Purpose, file.ContentType, file.Bytes, file.Content, file.ExpiresAfterSeconds)
		if insertErr != nil {
			return ragstate.IngestResult{}, insertErr
		}
		if command.RowsAffected() != 1 {
			return ragstate.IngestResult{}, filestate.ErrConflict
		}
		if err = tx.QueryRow(ctx, `SELECT created_at,expires_at FROM gateway_files WHERE owner_key=$1 AND id=$2`, request.OwnerKey, file.ID).Scan(&file.CreatedAt, &file.ExpiresAt); err != nil {
			return ragstate.IngestResult{}, err
		}
		file.Content = nil
	} else {
		err = tx.QueryRow(ctx, `SELECT id,filename,purpose,content_type,bytes,created_at,expires_at FROM gateway_files WHERE owner_key=$1 AND id=$2 AND purpose='assistants' AND (expires_at IS NULL OR expires_at>now())`, request.OwnerKey, request.FileID).
			Scan(&file.ID, &file.Filename, &file.Purpose, &file.ContentType, &file.Bytes, &file.CreatedAt, &file.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ragstate.IngestResult{}, filestate.ErrNotFound
		}
		if err != nil {
			return ragstate.IngestResult{}, err
		}
	}

	store := vectorstate.VectorStore{OwnerKey: request.OwnerKey}
	if createStore {
		store = *request.VectorStore
		if store.Metadata == nil {
			store.Metadata = map[string]string{}
		}
		metadata, marshalErr := json.Marshal(store.Metadata)
		if marshalErr != nil {
			return ragstate.IngestResult{}, vectorstate.ErrInvalid
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_vector_stores WHERE owner_key=$1 AND (expires_at IS NULL OR expires_at>now())`, request.OwnerKey).Scan(&count); err != nil {
			return ragstate.IngestResult{}, err
		}
		if count >= request.VectorStoreQuota {
			return ragstate.IngestResult{}, vectorstate.ErrQuotaExceeded
		}
		command, insertErr := tx.Exec(ctx, `INSERT INTO gateway_vector_stores (id,owner_key,name,metadata,expires_after_days,expires_at)
			VALUES ($1,$2,$3,$4::jsonb,$5,CASE WHEN $5>0 THEN now()+make_interval(days=>$5) ELSE NULL END) ON CONFLICT DO NOTHING`,
			store.ID, store.OwnerKey, store.Name, string(metadata), store.ExpiresAfter)
		if insertErr != nil {
			return ragstate.IngestResult{}, insertErr
		}
		if command.RowsAffected() != 1 {
			return ragstate.IngestResult{}, vectorstate.ErrConflict
		}
	} else {
		var expired bool
		if err = tx.QueryRow(ctx, `SELECT expires_at IS NOT NULL AND expires_at<=now() FROM gateway_vector_stores WHERE owner_key=$1 AND id=$2 FOR UPDATE`, request.OwnerKey, request.VectorStoreID).Scan(&expired); errors.Is(err, pgx.ErrNoRows) || expired {
			return ragstate.IngestResult{}, vectorstate.ErrNotFound
		}
		if err != nil {
			return ragstate.IngestResult{}, err
		}
	}

	var attached bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM gateway_vector_store_files WHERE owner_key=$1 AND vector_store_id=$2 AND file_id=$3)`, request.OwnerKey, request.VectorStoreID, request.FileID).Scan(&attached); err != nil {
		return ragstate.IngestResult{}, err
	}
	if attached {
		return ragstate.IngestResult{}, vectorstate.ErrConflict
	}
	var count int
	var usedBytes int64
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(f.bytes),0) FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.owner_key=$1 AND a.vector_store_id=$2`, request.OwnerKey, request.VectorStoreID).Scan(&count, &usedBytes); err != nil {
		return ragstate.IngestResult{}, err
	}
	if count >= request.VectorStoreFiles {
		return ragstate.IngestResult{}, vectorstate.ErrFileQuotaExceeded
	}
	if usedBytes < 0 || file.Bytes < 0 || usedBytes > request.VectorStoreBytes || file.Bytes > request.VectorStoreBytes-usedBytes {
		return ragstate.IngestResult{}, vectorstate.ErrByteQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_vector_store_files (vector_store_id,file_id,owner_key,attributes) VALUES ($1,$2,$3,$4::jsonb) ON CONFLICT DO NOTHING`, request.VectorStoreID, request.FileID, request.OwnerKey, string(attributes))
	if err != nil {
		return ragstate.IngestResult{}, err
	}
	if command.RowsAffected() != 1 {
		return ragstate.IngestResult{}, vectorstate.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE gateway_vector_stores SET last_active_at=now(),updated_at=now(),expires_at=CASE WHEN expires_after_days>0 THEN now()+make_interval(days=>expires_after_days) ELSE NULL END WHERE owner_key=$1 AND id=$2`, request.OwnerKey, request.VectorStoreID); err != nil {
		return ragstate.IngestResult{}, err
	}
	attachment, err := getVectorStoreFile(ctx, tx, request.OwnerKey, request.VectorStoreID, request.FileID)
	if err != nil {
		return ragstate.IngestResult{}, err
	}
	store, err = getVectorStore(ctx, tx, request.OwnerKey, request.VectorStoreID)
	if err != nil {
		return ragstate.IngestResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ragstate.IngestResult{}, err
	}
	return ragstate.IngestResult{File: file, VectorStore: store, Attachment: attachment}, nil
}
