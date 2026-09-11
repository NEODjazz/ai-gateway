package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"ai-gateway-gateway/internal/vectorstate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *PostgresStore) CreateVectorStoreFileBatch(ctx context.Context, batch vectorstate.FileBatch, entries []vectorstate.FileBatchEntry, quota int, byteQuota int64) (vectorstate.FileBatch, error) {
	if s == nil || s.pool == nil {
		return vectorstate.FileBatch{}, vectorstate.ErrUnavailable
	}
	if batch.ID == "" || batch.VectorStoreID == "" || batch.OwnerKey == "" || len(entries) < 1 || len(entries) > 2000 || quota < 1 || byteQuota < 1 {
		return vectorstate.FileBatch{}, vectorstate.ErrInvalid
	}
	encoded := make([]string, len(entries))
	ids := make([]string, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		if entry.FileID == "" || vectorstate.ValidateAttributes(entry.Attributes) != "" {
			return vectorstate.FileBatch{}, vectorstate.ErrInvalid
		}
		if _, found := seen[entry.FileID]; found {
			return vectorstate.FileBatch{}, vectorstate.ErrConflict
		}
		seen[entry.FileID] = struct{}{}
		ids[index] = entry.FileID
		attributes := entry.Attributes
		if attributes == nil {
			attributes = map[string]any{}
		}
		value, err := json.Marshal(attributes)
		if err != nil {
			return vectorstate.FileBatch{}, vectorstate.ErrInvalid
		}
		encoded[index] = string(value)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return vectorstate.FileBatch{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var expired bool
	if err = tx.QueryRow(ctx, `SELECT expires_at IS NOT NULL AND expires_at<=now() FROM gateway_vector_stores WHERE owner_key=$1 AND id=$2 FOR UPDATE`, batch.OwnerKey, batch.VectorStoreID).Scan(&expired); errors.Is(err, pgx.ErrNoRows) || expired {
		return vectorstate.FileBatch{}, vectorstate.ErrNotFound
	} else if err != nil {
		return vectorstate.FileBatch{}, err
	}
	var count int
	var usedBytes int64
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(f.bytes),0) FROM gateway_vector_store_files a JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE a.owner_key=$1 AND a.vector_store_id=$2`, batch.OwnerKey, batch.VectorStoreID).Scan(&count, &usedBytes); err != nil {
		return vectorstate.FileBatch{}, err
	}
	if count > quota || len(entries) > quota-count {
		return vectorstate.FileBatch{}, vectorstate.ErrFileQuotaExceeded
	}
	rows, err := tx.Query(ctx, `SELECT requested.id,f.bytes,a.file_id IS NOT NULL FROM unnest($3::text[]) WITH ORDINALITY requested(id,ordinal) LEFT JOIN gateway_files f ON f.id=requested.id AND f.owner_key=$1 AND (f.expires_at IS NULL OR f.expires_at>now()) LEFT JOIN gateway_vector_store_files a ON a.owner_key=$1 AND a.vector_store_id=$2 AND a.file_id=requested.id ORDER BY requested.ordinal`, batch.OwnerKey, batch.VectorStoreID, ids)
	if err != nil {
		return vectorstate.FileBatch{}, err
	}
	var addedBytes int64
	validated := 0
	for rows.Next() {
		var id string
		var size *int64
		var attached bool
		if err = rows.Scan(&id, &size, &attached); err != nil {
			rows.Close()
			return vectorstate.FileBatch{}, err
		}
		if size == nil {
			rows.Close()
			return vectorstate.FileBatch{}, vectorstate.ErrFileNotFound
		}
		if attached {
			rows.Close()
			return vectorstate.FileBatch{}, vectorstate.ErrConflict
		}
		if *size < 0 || addedBytes > byteQuota || *size > byteQuota-addedBytes {
			rows.Close()
			return vectorstate.FileBatch{}, vectorstate.ErrByteQuotaExceeded
		}
		addedBytes += *size
		validated++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return vectorstate.FileBatch{}, err
	}
	rows.Close()
	if validated != len(entries) {
		return vectorstate.FileBatch{}, vectorstate.ErrFileNotFound
	}
	if usedBytes < 0 || usedBytes > byteQuota || addedBytes > byteQuota-usedBytes {
		return vectorstate.FileBatch{}, vectorstate.ErrByteQuotaExceeded
	}
	if _, err = tx.Exec(ctx, `INSERT INTO gateway_vector_store_file_batches (id,vector_store_id,owner_key,status,total,completed) VALUES ($1,$2,$3,'completed',$4,$4)`, batch.ID, batch.VectorStoreID, batch.OwnerKey, len(entries)); err != nil {
		if vectorStoreBatchUniqueViolation(err) {
			return vectorstate.FileBatch{}, vectorstate.ErrConflict
		}
		return vectorstate.FileBatch{}, err
	}
	queued := &pgx.Batch{}
	for index, entry := range entries {
		queued.Queue(`INSERT INTO gateway_vector_store_files (vector_store_id,file_id,owner_key,attributes) VALUES ($1,$2,$3,$4::jsonb)`, batch.VectorStoreID, entry.FileID, batch.OwnerKey, encoded[index])
		queued.Queue(`INSERT INTO gateway_vector_store_file_batch_files (batch_id,vector_store_id,file_id,owner_key,ordinal) VALUES ($1,$2,$3,$4,$5)`, batch.ID, batch.VectorStoreID, entry.FileID, batch.OwnerKey, index)
	}
	results := tx.SendBatch(ctx, queued)
	for range entries {
		if _, err = results.Exec(); err != nil {
			_ = results.Close()
			if vectorStoreBatchUniqueViolation(err) {
				return vectorstate.FileBatch{}, vectorstate.ErrConflict
			}
			return vectorstate.FileBatch{}, err
		}
		if _, err = results.Exec(); err != nil {
			_ = results.Close()
			return vectorstate.FileBatch{}, err
		}
	}
	if err = results.Close(); err != nil {
		return vectorstate.FileBatch{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE gateway_vector_stores SET last_active_at=now(),updated_at=now(),expires_at=CASE WHEN expires_after_days>0 THEN now()+make_interval(days=>expires_after_days) ELSE NULL END WHERE owner_key=$1 AND id=$2`, batch.OwnerKey, batch.VectorStoreID); err != nil {
		return vectorstate.FileBatch{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return vectorstate.FileBatch{}, err
	}
	return s.GetVectorStoreFileBatch(ctx, batch.OwnerKey, batch.VectorStoreID, batch.ID)
}

func vectorStoreBatchUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *PostgresStore) GetVectorStoreFileBatch(ctx context.Context, owner, vectorStoreID, batchID string) (vectorstate.FileBatch, error) {
	if s == nil || s.pool == nil {
		return vectorstate.FileBatch{}, vectorstate.ErrUnavailable
	}
	if owner == "" || vectorStoreID == "" || batchID == "" {
		return vectorstate.FileBatch{}, vectorstate.ErrInvalid
	}
	var batch vectorstate.FileBatch
	err := s.pool.QueryRow(ctx, `SELECT id,vector_store_id,owner_key,status,total,completed,failed,cancelled,created_at FROM gateway_vector_store_file_batches WHERE owner_key=$1 AND vector_store_id=$2 AND id=$3`, owner, vectorStoreID, batchID).Scan(&batch.ID, &batch.VectorStoreID, &batch.OwnerKey, &batch.Status, &batch.Total, &batch.Completed, &batch.Failed, &batch.Cancelled, &batch.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return vectorstate.FileBatch{}, vectorstate.ErrFileBatchNotFound
	}
	return batch, err
}

func (s *PostgresStore) ListVectorStoreFileBatchFiles(ctx context.Context, owner, vectorStoreID, batchID string, options vectorstate.FileListOptions) ([]vectorstate.File, string, error) {
	if !options.Valid() {
		return nil, "", vectorstate.ErrInvalid
	}
	if _, err := s.GetVectorStoreFileBatch(ctx, owner, vectorStoreID, batchID); err != nil {
		return nil, "", err
	}
	var cursorTime *time.Time
	var cursorID string
	cursor := options.After
	if options.Before != "" {
		cursor = options.Before
	}
	if cursor != "" {
		var createdAt time.Time
		err := s.pool.QueryRow(ctx, `SELECT a.created_at,a.file_id FROM gateway_vector_store_file_batch_files bf JOIN gateway_vector_store_files a ON a.vector_store_id=bf.vector_store_id AND a.file_id=bf.file_id AND a.owner_key=bf.owner_key JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE bf.owner_key=$1 AND bf.vector_store_id=$2 AND bf.batch_id=$3 AND a.file_id=$4 AND ($5='' OR a.status=$5)`, owner, vectorStoreID, batchID, cursor, options.Status).Scan(&createdAt, &cursorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", vectorstate.ErrFileNotFound
		}
		if err != nil {
			return nil, "", err
		}
		cursorTime = &createdAt
	}
	base := `SELECT a.vector_store_id,a.file_id,a.owner_key,a.status,f.bytes,a.attributes,a.created_at FROM gateway_vector_store_file_batch_files bf JOIN gateway_vector_store_files a ON a.vector_store_id=bf.vector_store_id AND a.file_id=bf.file_id AND a.owner_key=bf.owner_key JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE bf.owner_key=$1 AND bf.vector_store_id=$2 AND bf.batch_id=$3 AND ($4='' OR a.status=$4) AND ($5::timestamptz IS NULL OR (a.created_at,a.file_id)<($5::timestamptz,$6)) ORDER BY a.created_at DESC,a.file_id DESC LIMIT $7`
	if options.Order == "asc" {
		base = `SELECT a.vector_store_id,a.file_id,a.owner_key,a.status,f.bytes,a.attributes,a.created_at FROM gateway_vector_store_file_batch_files bf JOIN gateway_vector_store_files a ON a.vector_store_id=bf.vector_store_id AND a.file_id=bf.file_id AND a.owner_key=bf.owner_key JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE bf.owner_key=$1 AND bf.vector_store_id=$2 AND bf.batch_id=$3 AND ($4='' OR a.status=$4) AND ($5::timestamptz IS NULL OR (a.created_at,a.file_id)>($5::timestamptz,$6)) ORDER BY a.created_at ASC,a.file_id ASC LIMIT $7`
	}
	if options.Before != "" && options.Order == "desc" {
		base = `SELECT a.vector_store_id,a.file_id,a.owner_key,a.status,f.bytes,a.attributes,a.created_at FROM gateway_vector_store_file_batch_files bf JOIN gateway_vector_store_files a ON a.vector_store_id=bf.vector_store_id AND a.file_id=bf.file_id AND a.owner_key=bf.owner_key JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE bf.owner_key=$1 AND bf.vector_store_id=$2 AND bf.batch_id=$3 AND ($4='' OR a.status=$4) AND (a.created_at,a.file_id)>($5::timestamptz,$6) ORDER BY a.created_at ASC,a.file_id ASC LIMIT $7`
	}
	if options.Before != "" && options.Order == "asc" {
		base = `SELECT a.vector_store_id,a.file_id,a.owner_key,a.status,f.bytes,a.attributes,a.created_at FROM gateway_vector_store_file_batch_files bf JOIN gateway_vector_store_files a ON a.vector_store_id=bf.vector_store_id AND a.file_id=bf.file_id AND a.owner_key=bf.owner_key JOIN gateway_files f ON f.id=a.file_id AND f.owner_key=a.owner_key AND (f.expires_at IS NULL OR f.expires_at>now()) WHERE bf.owner_key=$1 AND bf.vector_store_id=$2 AND bf.batch_id=$3 AND ($4='' OR a.status=$4) AND (a.created_at,a.file_id)<($5::timestamptz,$6) ORDER BY a.created_at DESC,a.file_id DESC LIMIT $7`
	}
	rows, err := s.pool.Query(ctx, base, owner, vectorStoreID, batchID, options.Status, cursorTime, cursorID, options.Limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	files := make([]vectorstate.File, 0, options.Limit+1)
	for rows.Next() {
		file, scanErr := scanVectorStoreFile(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		files = append(files, file)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(files) > options.Limit {
		next = files[options.Limit-1].FileID
		files = files[:options.Limit]
	}
	if options.Before != "" {
		slices.Reverse(files)
	}
	return files, next, nil
}
