package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ai-gateway-gateway/internal/cachedstate"
	"ai-gateway-gateway/internal/openai"
	"github.com/jackc/pgx/v5"
)

const maxCachedContentSnapshotBytes = 1 << 20

func (s *PostgresStore) CreateCachedContentRecord(ctx context.Context, record cachedstate.Record, ownerQuota int) (cachedstate.Record, error) {
	if s == nil || s.pool == nil {
		return cachedstate.Record{}, cachedstate.ErrUnavailable
	}
	payload, expiresAt, err := cachedContentRecordPayload(record)
	if err != nil || ownerQuota < 1 || ownerQuota > 100000 || !expiresAt.After(time.Now()) {
		return cachedstate.Record{}, cachedstate.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return cachedstate.Record{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 17))`, record.OwnerKey); err != nil {
		return cachedstate.Record{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM gateway_cached_contents WHERE owner_key=$1 AND expires_at<=now()`, record.OwnerKey); err != nil {
		return cachedstate.Record{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_cached_contents WHERE owner_key=$1 AND expires_at>now()`, record.OwnerKey).Scan(&count); err != nil {
		return cachedstate.Record{}, err
	}
	if count >= ownerQuota {
		return cachedstate.Record{}, cachedstate.ErrQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_cached_contents (cached_content_name,owner_key,endpoint,model,deployment,policy_fingerprint,snapshot,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8) ON CONFLICT DO NOTHING`, record.Content.Name, record.OwnerKey, record.Binding.Endpoint, record.Binding.Model, record.Binding.Deployment, record.Binding.Policy, payload, expiresAt)
	if err != nil {
		return cachedstate.Record{}, err
	}
	if command.RowsAffected() != 1 {
		return cachedstate.Record{}, cachedstate.ErrConflict
	}
	created, err := getCachedContentRecord(ctx, tx, record.OwnerKey, record.Content.Name)
	if err != nil {
		return cachedstate.Record{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return cachedstate.Record{}, err
	}
	return created, nil
}

func cachedContentRecordPayload(record cachedstate.Record) ([]byte, time.Time, error) {
	content := record.Content
	if record.OwnerKey == "" || len(record.OwnerKey) > 256 || !validCachedContentName(content.Name) || record.Binding.Endpoint == "" || len(record.Binding.Endpoint) > 128 || record.Binding.Model == "" || len(record.Binding.Model) > 256 || len(record.Binding.Deployment) != 64 || len(record.Binding.Policy) != 64 || content.Model == "" || len(content.Model) > 263 {
		return nil, time.Time{}, cachedstate.ErrInvalid
	}
	return cachedContentPayload(content, record.ExpiresAt)
}

func cachedContentPayload(content openai.GeminiCachedContent, expectedExpiry time.Time) ([]byte, time.Time, error) {
	if !validCachedContentName(content.Name) || !strings.HasPrefix(content.Model, "models/") || !validCachedContentModel(strings.TrimPrefix(content.Model, "models/")) {
		return nil, time.Time{}, cachedstate.ErrInvalid
	}
	for _, timestamp := range []string{content.CreateTime, content.UpdateTime, content.ExpireTime} {
		if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
			return nil, time.Time{}, cachedstate.ErrInvalid
		}
	}
	expiresAt, _ := time.Parse(time.RFC3339Nano, content.ExpireTime)
	if !expectedExpiry.IsZero() && !expectedExpiry.Equal(expiresAt) {
		return nil, time.Time{}, cachedstate.ErrInvalid
	}
	if content.UsageMetadata != nil && content.UsageMetadata.TotalTokenCount < 0 {
		return nil, time.Time{}, cachedstate.ErrInvalid
	}
	payload, err := json.Marshal(content)
	if err != nil || len(payload) > maxCachedContentSnapshotBytes {
		return nil, time.Time{}, cachedstate.ErrInvalid
	}
	return payload, expiresAt, nil
}

func validCachedContentModel(model string) bool {
	return model != "" && len(model) <= 256 && model != "." && model != ".." && !strings.ContainsAny(model, "/\\?#%")
}

func validCachedContentName(name string) bool {
	if !strings.HasPrefix(name, "cachedContents/") || len(name) > 256 {
		return false
	}
	id := strings.TrimPrefix(name, "cachedContents/")
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\?#%")
}

func (s *PostgresStore) GetCachedContentRecord(ctx context.Context, owner, name string) (cachedstate.Record, error) {
	if s == nil || s.pool == nil {
		return cachedstate.Record{}, cachedstate.ErrUnavailable
	}
	if owner == "" || !validCachedContentName(name) {
		return cachedstate.Record{}, cachedstate.ErrInvalid
	}
	return getCachedContentRecord(ctx, s.pool, owner, name)
}

func getCachedContentRecord(ctx context.Context, query fineTuningQuerier, owner, name string) (cachedstate.Record, error) {
	var record cachedstate.Record
	var payload []byte
	record.OwnerKey = owner
	err := query.QueryRow(ctx, `SELECT endpoint,model,deployment,policy_fingerprint,snapshot,expires_at,created_at,updated_at FROM gateway_cached_contents WHERE owner_key=$1 AND cached_content_name=$2 AND expires_at>now()`, owner, name).Scan(&record.Binding.Endpoint, &record.Binding.Model, &record.Binding.Deployment, &record.Binding.Policy, &payload, &record.ExpiresAt, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return cachedstate.Record{}, cachedstate.ErrNotFound
	}
	if err != nil {
		return cachedstate.Record{}, err
	}
	if json.Unmarshal(payload, &record.Content) != nil || record.Content.Name != name {
		return cachedstate.Record{}, cachedstate.ErrUnavailable
	}
	if _, _, err := cachedContentPayload(record.Content, record.ExpiresAt); err != nil {
		return cachedstate.Record{}, cachedstate.ErrUnavailable
	}
	return record, nil
}

func (s *PostgresStore) ListCachedContentRecords(ctx context.Context, owner string, limit int, after string) ([]cachedstate.Record, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", cachedstate.ErrUnavailable
	}
	if owner == "" || limit < 1 || limit > 100 || after != "" && !validCachedContentName(after) {
		return nil, "", cachedstate.ErrInvalid
	}
	var cursor *time.Time
	var cursorName string
	if after != "" {
		var created time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at,cached_content_name FROM gateway_cached_contents WHERE owner_key=$1 AND cached_content_name=$2 AND expires_at>now()`, owner, after).Scan(&created, &cursorName)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", cachedstate.ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		cursor = &created
	}
	rows, err := s.pool.Query(ctx, `SELECT cached_content_name,endpoint,model,deployment,policy_fingerprint,snapshot,expires_at,created_at,updated_at FROM gateway_cached_contents WHERE owner_key=$1 AND expires_at>now() AND ($2::timestamptz IS NULL OR (created_at,cached_content_name)<($2,$3)) ORDER BY created_at DESC,cached_content_name DESC LIMIT $4`, owner, cursor, cursorName, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	result := make([]cachedstate.Record, 0, limit+1)
	for rows.Next() {
		var record cachedstate.Record
		var name string
		var payload []byte
		record.OwnerKey = owner
		if err := rows.Scan(&name, &record.Binding.Endpoint, &record.Binding.Model, &record.Binding.Deployment, &record.Binding.Policy, &payload, &record.ExpiresAt, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, "", err
		}
		if json.Unmarshal(payload, &record.Content) != nil || record.Content.Name != name {
			return nil, "", cachedstate.ErrUnavailable
		}
		if _, _, err := cachedContentPayload(record.Content, record.ExpiresAt); err != nil {
			return nil, "", cachedstate.ErrUnavailable
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(result) > limit {
		next = result[limit-1].Content.Name
		result = result[:limit]
	}
	return result, next, nil
}

func (s *PostgresStore) UpdateCachedContentRecord(ctx context.Context, owner string, content openai.GeminiCachedContent) (cachedstate.Record, error) {
	if s == nil || s.pool == nil {
		return cachedstate.Record{}, cachedstate.ErrUnavailable
	}
	payload, expiresAt, err := cachedContentPayload(content, time.Time{})
	if owner == "" || len(owner) > 256 || err != nil || !expiresAt.After(time.Now()) {
		return cachedstate.Record{}, cachedstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `UPDATE gateway_cached_contents SET snapshot=$3::jsonb,expires_at=$4,updated_at=now() WHERE owner_key=$1 AND cached_content_name=$2 AND expires_at>now()`, owner, content.Name, payload, expiresAt)
	if err != nil {
		return cachedstate.Record{}, err
	}
	if command.RowsAffected() != 1 {
		return cachedstate.Record{}, cachedstate.ErrNotFound
	}
	return s.GetCachedContentRecord(ctx, owner, content.Name)
}

func (s *PostgresStore) DeleteCachedContentRecord(ctx context.Context, owner, name string) error {
	if s == nil || s.pool == nil {
		return cachedstate.ErrUnavailable
	}
	if owner == "" || !validCachedContentName(name) {
		return cachedstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_cached_contents WHERE owner_key=$1 AND cached_content_name=$2 AND expires_at>now()`, owner, name)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return cachedstate.ErrNotFound
	}
	return nil
}
