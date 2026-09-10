package controlstore

import (
	"bytes"
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) EnqueueAsyncJob(ctx context.Context, job asyncstate.Job) (bool, error) {
	if s == nil || s.pool == nil {
		return false, asyncstate.ErrUnavailable
	}
	if !asyncstate.Valid(job) {
		return false, asyncstate.ErrInvalid
	}
	var availableAt *time.Time
	if !job.AvailableAt.IsZero() {
		value := job.AvailableAt.UTC()
		availableAt = &value
	}
	command, err := s.pool.Exec(ctx, `INSERT INTO gateway_async_jobs (kind,resource_id,owner_key,endpoint_id,execution_id,payload,available_at)
		VALUES ($1,$2,$3,$4,$5,$6,COALESCE($7::timestamptz,now())) ON CONFLICT DO NOTHING`, job.Kind, job.ResourceID, job.OwnerKey, job.EndpointID, job.ExecutionID, job.Payload, availableAt)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() == 1 {
		return true, nil
	}
	var owner, endpoint, execution string
	var payload []byte
	err = s.pool.QueryRow(ctx, `SELECT owner_key,endpoint_id,execution_id,payload FROM gateway_async_jobs WHERE kind=$1 AND resource_id=$2`, job.Kind, job.ResourceID).Scan(&owner, &endpoint, &execution, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, asyncstate.ErrConflict
	}
	if err != nil {
		return false, err
	}
	if owner != job.OwnerKey || endpoint != job.EndpointID || execution != job.ExecutionID || !bytes.Equal(payload, job.Payload) {
		return false, asyncstate.ErrConflict
	}
	return false, nil
}

func (s *PostgresStore) ClaimAsyncJobs(ctx context.Context, kind string, limit int, lease time.Duration) ([]asyncstate.Job, error) {
	if s == nil || s.pool == nil {
		return nil, asyncstate.ErrUnavailable
	}
	if kind == "" || limit < 1 || limit > 100 || lease < time.Second || lease > time.Hour {
		return nil, asyncstate.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `WITH selected AS (
		SELECT kind,resource_id FROM gateway_async_jobs
		WHERE kind=$1 AND available_at<=now() AND (state='pending' OR (state='leased' AND lease_until<=now()))
		ORDER BY available_at,created_at,resource_id FOR UPDATE SKIP LOCKED LIMIT $2
	) UPDATE gateway_async_jobs AS jobs SET state='leased',attempts=jobs.attempts+1,
		lease_generation=jobs.lease_generation+1,lease_until=now()+$3::interval,updated_at=now()
		FROM selected WHERE jobs.kind=selected.kind AND jobs.resource_id=selected.resource_id
		RETURNING jobs.kind,jobs.resource_id,jobs.owner_key,jobs.endpoint_id,jobs.execution_id,jobs.payload,jobs.attempts,jobs.lease_generation,jobs.available_at,jobs.lease_until,jobs.created_at`, kind, limit, lease.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]asyncstate.Job, 0, limit)
	for rows.Next() {
		var job asyncstate.Job
		if err := rows.Scan(&job.Kind, &job.ResourceID, &job.OwnerKey, &job.EndpointID, &job.ExecutionID, &job.Payload, &job.Attempts, &job.LeaseGeneration, &job.AvailableAt, &job.LeaseUntil, &job.CreatedAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *PostgresStore) RetryAsyncJob(ctx context.Context, kind, resourceID string, generation int64, delay time.Duration) error {
	if s == nil || s.pool == nil {
		return asyncstate.ErrUnavailable
	}
	if kind == "" || resourceID == "" || generation < 1 || delay < 0 || delay > 24*time.Hour {
		return asyncstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `UPDATE gateway_async_jobs SET state='pending',available_at=now()+$4::interval,lease_until=NULL,updated_at=now()
		WHERE kind=$1 AND resource_id=$2 AND state='leased' AND lease_generation=$3`, kind, resourceID, generation, delay.String())
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return asyncstate.ErrLeaseLost
	}
	return nil
}

func (s *PostgresStore) CompleteAsyncJob(ctx context.Context, kind, resourceID string, generation int64) error {
	if s == nil || s.pool == nil {
		return asyncstate.ErrUnavailable
	}
	if kind == "" || resourceID == "" || generation < 1 {
		return asyncstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_async_jobs WHERE kind=$1 AND resource_id=$2 AND state='leased' AND lease_generation=$3`, kind, resourceID, generation)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return asyncstate.ErrLeaseLost
	}
	return nil
}
