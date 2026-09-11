package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/videostate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateVideoRecord(ctx context.Context, record videostate.Record, ownerQuota int) (videostate.Record, error) {
	if s == nil || s.pool == nil {
		return videostate.Record{}, videostate.ErrUnavailable
	}
	payload, err := videoRecordPayload(record)
	if err != nil || ownerQuota < 1 || ownerQuota > 100000 {
		return videostate.Record{}, videostate.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return videostate.Record{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 5))`, record.OwnerKey); err != nil {
		return videostate.Record{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_video_jobs WHERE owner_key=$1`, record.OwnerKey).Scan(&count); err != nil {
		return videostate.Record{}, err
	}
	if count >= ownerQuota {
		return videostate.Record{}, videostate.ErrQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_video_jobs (video_id,owner_key,endpoint,model,deployment,snapshot) VALUES ($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT DO NOTHING`, record.Video.ID, record.OwnerKey, record.Binding.Endpoint, record.Binding.Model, record.Binding.Deployment, payload)
	if err != nil {
		return videostate.Record{}, err
	}
	if command.RowsAffected() != 1 {
		return videostate.Record{}, videostate.ErrConflict
	}
	created, err := getVideoRecord(ctx, tx, record.OwnerKey, record.Video.ID)
	if err != nil {
		return videostate.Record{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return videostate.Record{}, err
	}
	return created, nil
}

func videoRecordPayload(record videostate.Record) ([]byte, error) {
	if record.OwnerKey == "" || len(record.OwnerKey) > 256 || record.Video.ID == "" || len(record.Video.ID) > 128 || record.Binding.Endpoint == "" || len(record.Binding.Endpoint) > 128 || record.Binding.Model == "" || len(record.Binding.Model) > 256 || len(record.Binding.Deployment) != 64 {
		return nil, videostate.ErrInvalid
	}
	payload, err := json.Marshal(record.Video)
	if err != nil || len(payload) > 4<<20 {
		return nil, videostate.ErrInvalid
	}
	return payload, nil
}

func (s *PostgresStore) GetVideoRecord(ctx context.Context, owner, id string) (videostate.Record, error) {
	if s == nil || s.pool == nil {
		return videostate.Record{}, videostate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return videostate.Record{}, videostate.ErrInvalid
	}
	return getVideoRecord(ctx, s.pool, owner, id)
}

func getVideoRecord(ctx context.Context, q fineTuningQuerier, owner, id string) (videostate.Record, error) {
	var record videostate.Record
	var payload []byte
	record.OwnerKey = owner
	err := q.QueryRow(ctx, `SELECT endpoint,model,deployment,snapshot,created_at,updated_at FROM gateway_video_jobs WHERE owner_key=$1 AND video_id=$2`, owner, id).Scan(&record.Binding.Endpoint, &record.Binding.Model, &record.Binding.Deployment, &payload, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return videostate.Record{}, videostate.ErrNotFound
	}
	if err != nil {
		return videostate.Record{}, err
	}
	if json.Unmarshal(payload, &record.Video) != nil {
		return videostate.Record{}, videostate.ErrUnavailable
	}
	return record, nil
}

func (s *PostgresStore) ListVideoRecords(ctx context.Context, owner string, limit int, after string) ([]videostate.Record, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", videostate.ErrUnavailable
	}
	if owner == "" || limit < 1 || limit > 100 {
		return nil, "", videostate.ErrInvalid
	}
	var cursor *time.Time
	var cursorID string
	if after != "" {
		var created time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at,video_id FROM gateway_video_jobs WHERE owner_key=$1 AND video_id=$2`, owner, after).Scan(&created, &cursorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", videostate.ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		cursor = &created
	}
	rows, err := s.pool.Query(ctx, `SELECT video_id,endpoint,model,deployment,snapshot,created_at,updated_at FROM gateway_video_jobs WHERE owner_key=$1 AND ($2::timestamptz IS NULL OR (created_at,video_id)<($2,$3)) ORDER BY created_at DESC,video_id DESC LIMIT $4`, owner, cursor, cursorID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	result := make([]videostate.Record, 0, limit+1)
	for rows.Next() {
		var record videostate.Record
		var payload []byte
		record.OwnerKey = owner
		if err := rows.Scan(&record.Video.ID, &record.Binding.Endpoint, &record.Binding.Model, &record.Binding.Deployment, &payload, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, "", err
		}
		if json.Unmarshal(payload, &record.Video) != nil {
			return nil, "", videostate.ErrUnavailable
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(result) > limit {
		next = result[limit-1].Video.ID
		result = result[:limit]
	}
	return result, next, nil
}

func (s *PostgresStore) UpdateVideoRecord(ctx context.Context, owner string, video openai.Video) (videostate.Record, error) {
	if s == nil || s.pool == nil {
		return videostate.Record{}, videostate.ErrUnavailable
	}
	payload, err := json.Marshal(video)
	if owner == "" || video.ID == "" || err != nil || len(payload) > 4<<20 {
		return videostate.Record{}, videostate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `UPDATE gateway_video_jobs SET snapshot=$3::jsonb,updated_at=now() WHERE owner_key=$1 AND video_id=$2`, owner, video.ID, payload)
	if err != nil {
		return videostate.Record{}, err
	}
	if command.RowsAffected() != 1 {
		return videostate.Record{}, videostate.ErrNotFound
	}
	return s.GetVideoRecord(ctx, owner, video.ID)
}

func (s *PostgresStore) DeleteVideoRecord(ctx context.Context, owner, id string) error {
	if s == nil || s.pool == nil {
		return videostate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return videostate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_video_jobs WHERE owner_key=$1 AND video_id=$2`, owner, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return videostate.ErrNotFound
	}
	return nil
}
