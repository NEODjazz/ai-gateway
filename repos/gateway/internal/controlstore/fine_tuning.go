package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/finetunestate"
	"ai-gateway-gateway/internal/openai"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateFineTuningRecord(ctx context.Context, record finetunestate.Record, ownerQuota int) (finetunestate.Record, error) {
	return s.createFineTuningRecord(ctx, record, ownerQuota, nil)
}

func (s *PostgresStore) CreateFineTuningRecordWithJob(ctx context.Context, record finetunestate.Record, ownerQuota int, job asyncstate.Job) (finetunestate.Record, error) {
	if !asyncstate.Valid(job) || job.ResourceID != record.Job.ID || job.OwnerKey != record.OwnerKey || job.EndpointID != record.Binding.Endpoint {
		return finetunestate.Record{}, finetunestate.ErrInvalid
	}
	return s.createFineTuningRecord(ctx, record, ownerQuota, &job)
}

func (s *PostgresStore) createFineTuningRecord(ctx context.Context, record finetunestate.Record, ownerQuota int, job *asyncstate.Job) (finetunestate.Record, error) {
	if s == nil || s.pool == nil {
		return finetunestate.Record{}, finetunestate.ErrUnavailable
	}
	payload, err := fineTuningRecordPayload(record)
	if err != nil || ownerQuota < 1 || ownerQuota > 100000 {
		return finetunestate.Record{}, finetunestate.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return finetunestate.Record{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 4))`, record.OwnerKey); err != nil {
		return finetunestate.Record{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_fine_tuning_jobs WHERE owner_key=$1`, record.OwnerKey).Scan(&count); err != nil {
		return finetunestate.Record{}, err
	}
	if count >= ownerQuota {
		return finetunestate.Record{}, finetunestate.ErrQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_fine_tuning_jobs (job_id,owner_key,endpoint,model,deployment,snapshot) VALUES ($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT DO NOTHING`, record.Job.ID, record.OwnerKey, record.Binding.Endpoint, record.Binding.Model, record.Binding.Deployment, payload)
	if err != nil {
		return finetunestate.Record{}, err
	}
	if command.RowsAffected() != 1 {
		return finetunestate.Record{}, finetunestate.ErrConflict
	}
	if job != nil {
		command, err = tx.Exec(ctx, `INSERT INTO gateway_async_jobs (kind,resource_id,owner_key,endpoint_id,execution_id,payload,available_at)
			VALUES ($1,$2,$3,$4,$5,$6,now()) ON CONFLICT DO NOTHING`, job.Kind, job.ResourceID, job.OwnerKey, job.EndpointID, job.ExecutionID, job.Payload)
		if err != nil {
			return finetunestate.Record{}, err
		}
		if command.RowsAffected() != 1 {
			return finetunestate.Record{}, asyncstate.ErrConflict
		}
	}
	created, err := getFineTuningRecord(ctx, tx, record.OwnerKey, record.Job.ID)
	if err != nil {
		return finetunestate.Record{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return finetunestate.Record{}, err
	}
	return created, nil
}

func fineTuningRecordPayload(record finetunestate.Record) ([]byte, error) {
	if record.OwnerKey == "" || len(record.OwnerKey) > 256 || record.Job.ID == "" || len(record.Job.ID) > 128 || record.Binding.Endpoint == "" || len(record.Binding.Endpoint) > 128 || record.Binding.Model == "" || len(record.Binding.Model) > 256 || len(record.Binding.Deployment) != 64 {
		return nil, finetunestate.ErrInvalid
	}
	payload, err := json.Marshal(record.Job)
	if err != nil || len(payload) > 4<<20 {
		return nil, finetunestate.ErrInvalid
	}
	return payload, nil
}

func (s *PostgresStore) GetFineTuningRecord(ctx context.Context, owner, id string) (finetunestate.Record, error) {
	if s == nil || s.pool == nil {
		return finetunestate.Record{}, finetunestate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return finetunestate.Record{}, finetunestate.ErrInvalid
	}
	return getFineTuningRecord(ctx, s.pool, owner, id)
}

type fineTuningQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getFineTuningRecord(ctx context.Context, q fineTuningQuerier, owner, id string) (finetunestate.Record, error) {
	var r finetunestate.Record
	var payload []byte
	r.OwnerKey = owner
	err := q.QueryRow(ctx, `SELECT endpoint,model,deployment,snapshot,created_at,updated_at FROM gateway_fine_tuning_jobs WHERE owner_key=$1 AND job_id=$2`, owner, id).Scan(&r.Binding.Endpoint, &r.Binding.Model, &r.Binding.Deployment, &payload, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return finetunestate.Record{}, finetunestate.ErrNotFound
	}
	if err != nil {
		return finetunestate.Record{}, err
	}
	if json.Unmarshal(payload, &r.Job) != nil {
		return finetunestate.Record{}, finetunestate.ErrUnavailable
	}
	return r, nil
}

func (s *PostgresStore) ListFineTuningRecords(ctx context.Context, owner string, limit int, after string) ([]finetunestate.Record, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", finetunestate.ErrUnavailable
	}
	if owner == "" || limit < 1 || limit > 100 {
		return nil, "", finetunestate.ErrInvalid
	}
	var cursor *time.Time
	var cursorID string
	if after != "" {
		var created time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at,job_id FROM gateway_fine_tuning_jobs WHERE owner_key=$1 AND job_id=$2`, owner, after).Scan(&created, &cursorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", finetunestate.ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		cursor = &created
	}
	rows, err := s.pool.Query(ctx, `SELECT job_id,endpoint,model,deployment,snapshot,created_at,updated_at FROM gateway_fine_tuning_jobs WHERE owner_key=$1 AND ($2::timestamptz IS NULL OR (created_at,job_id)<($2,$3)) ORDER BY created_at DESC,job_id DESC LIMIT $4`, owner, cursor, cursorID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	result := make([]finetunestate.Record, 0, limit+1)
	for rows.Next() {
		var r finetunestate.Record
		var payload []byte
		r.OwnerKey = owner
		if err := rows.Scan(&r.Job.ID, &r.Binding.Endpoint, &r.Binding.Model, &r.Binding.Deployment, &payload, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, "", err
		}
		if json.Unmarshal(payload, &r.Job) != nil {
			return nil, "", finetunestate.ErrUnavailable
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(result) > limit {
		next = result[limit-1].Job.ID
		result = result[:limit]
	}
	return result, next, nil
}

func (s *PostgresStore) UpdateFineTuningRecord(ctx context.Context, owner string, job openai.FineTuningJob) (finetunestate.Record, error) {
	if s == nil || s.pool == nil {
		return finetunestate.Record{}, finetunestate.ErrUnavailable
	}
	payload, err := json.Marshal(job)
	if owner == "" || job.ID == "" || err != nil || len(payload) > 4<<20 {
		return finetunestate.Record{}, finetunestate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `UPDATE gateway_fine_tuning_jobs SET snapshot=$3::jsonb,updated_at=now() WHERE owner_key=$1 AND job_id=$2`, owner, job.ID, payload)
	if err != nil {
		return finetunestate.Record{}, err
	}
	if command.RowsAffected() != 1 {
		return finetunestate.Record{}, finetunestate.ErrNotFound
	}
	return s.GetFineTuningRecord(ctx, owner, job.ID)
}

func (s *PostgresStore) FindFineTuningRecordByModel(ctx context.Context, owner, model string) (finetunestate.Record, error) {
	if s == nil || s.pool == nil {
		return finetunestate.Record{}, finetunestate.ErrUnavailable
	}
	if owner == "" || model == "" || len(model) > 256 {
		return finetunestate.Record{}, finetunestate.ErrInvalid
	}
	var id string
	err := s.pool.QueryRow(ctx, `SELECT job_id FROM gateway_fine_tuning_jobs WHERE owner_key=$1 AND snapshot->>'fine_tuned_model'=$2 ORDER BY created_at DESC,job_id DESC LIMIT 1`, owner, model).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return finetunestate.Record{}, finetunestate.ErrNotFound
	}
	if err != nil {
		return finetunestate.Record{}, err
	}
	return s.GetFineTuningRecord(ctx, owner, id)
}
