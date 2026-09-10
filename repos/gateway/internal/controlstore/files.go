package controlstore

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/filestate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) Create(ctx context.Context, file filestate.File, ownerQuota int64) (filestate.File, error) {
	if s == nil || s.pool == nil {
		return filestate.File{}, filestate.ErrUnavailable
	}
	if file.ID == "" || file.OwnerKey == "" || file.Filename == "" || file.Purpose == "" || file.ContentType == "" ||
		file.Bytes < 0 || file.Bytes != int64(len(file.Content)) || ownerQuota <= 0 {
		return filestate.File{}, filestate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return filestate.File{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, file.OwnerKey); err != nil {
		return filestate.File{}, err
	}
	var used int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(bytes),0) FROM gateway_files WHERE owner_key=$1`, file.OwnerKey).Scan(&used); err != nil {
		return filestate.File{}, err
	}
	if file.Bytes > ownerQuota || used > ownerQuota-file.Bytes {
		return filestate.File{}, filestate.ErrQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_files (id,owner_key,filename,purpose,content_type,bytes,content) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, file.ID, file.OwnerKey, file.Filename, file.Purpose, file.ContentType, file.Bytes, file.Content)
	if err != nil {
		return filestate.File{}, err
	}
	if command.RowsAffected() != 1 {
		return filestate.File{}, filestate.ErrConflict
	}
	if err = tx.QueryRow(ctx, `SELECT created_at FROM gateway_files WHERE owner_key=$1 AND id=$2`, file.OwnerKey, file.ID).Scan(&file.CreatedAt); err != nil {
		return filestate.File{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return filestate.File{}, err
	}
	file.Content = nil
	return file, nil
}

func (s *PostgresStore) List(ctx context.Context, owner, purpose string, limit int, after string) ([]filestate.File, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", filestate.ErrUnavailable
	}
	if owner == "" || limit < 1 || limit > 100 {
		return nil, "", filestate.ErrInvalid
	}
	var cursorTime *time.Time
	var cursorID string
	if after != "" {
		var createdAt time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at,id FROM gateway_files WHERE owner_key=$1 AND id=$2`, owner, after).Scan(&createdAt, &cursorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", filestate.ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		cursorTime = &createdAt
	}
	rows, err := s.pool.Query(ctx, `SELECT id,filename,purpose,content_type,bytes,created_at
		FROM gateway_files
		WHERE owner_key=$1 AND ($2='' OR purpose=$2)
		AND ($3::timestamptz IS NULL OR (created_at,id)<($3::timestamptz,$4))
		ORDER BY created_at DESC,id DESC LIMIT $5`, owner, purpose, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	files := make([]filestate.File, 0, limit+1)
	for rows.Next() {
		var file filestate.File
		file.OwnerKey = owner
		if err := rows.Scan(&file.ID, &file.Filename, &file.Purpose, &file.ContentType, &file.Bytes, &file.CreatedAt); err != nil {
			return nil, "", err
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(files) > limit {
		next = files[limit-1].ID
		files = files[:limit]
	}
	return files, next, nil
}

func (s *PostgresStore) Get(ctx context.Context, owner, id string, content bool) (filestate.File, error) {
	if s == nil || s.pool == nil {
		return filestate.File{}, filestate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return filestate.File{}, filestate.ErrInvalid
	}
	f := filestate.File{OwnerKey: owner}
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT id,filename,purpose,content_type,bytes,created_at,CASE WHEN $3 THEN content ELSE NULL END FROM gateway_files WHERE owner_key=$1 AND id=$2`, owner, id, content).Scan(&f.ID, &f.Filename, &f.Purpose, &f.ContentType, &f.Bytes, &f.CreatedAt, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return filestate.File{}, filestate.ErrNotFound
	}
	if err != nil {
		return filestate.File{}, err
	}
	f.Content = payload
	return f, nil
}

func (s *PostgresStore) Delete(ctx context.Context, owner, id string) error {
	if s == nil || s.pool == nil {
		return filestate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return filestate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_files WHERE owner_key=$1 AND id=$2`, owner, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return filestate.ErrNotFound
	}
	return nil
}
