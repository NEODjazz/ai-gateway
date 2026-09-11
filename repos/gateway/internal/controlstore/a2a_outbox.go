package controlstore

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/asyncstate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateA2ATaskWithJob(ctx context.Context, task a2astate.Task, ownerQuota int, ttl time.Duration, job asyncstate.Job) (a2astate.Task, error) {
	if s == nil || s.pool == nil {
		return a2astate.Task{}, a2astate.ErrUnavailable
	}
	if !validA2ATask(task) || ownerQuota < 1 || ownerQuota > 100000 || ttl < time.Second || ttl > 365*24*time.Hour || !validA2AOutboxJob(task, job) {
		return a2astate.Task{}, a2astate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return a2astate.Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 2))`, task.OwnerKey); err != nil {
		return a2astate.Task{}, err
	}
	if _, err = tx.Exec(ctx, `WITH expired AS (
		SELECT id FROM gateway_a2a_tasks WHERE expires_at<=now() ORDER BY expires_at LIMIT 1000
	) DELETE FROM gateway_a2a_tasks AS tasks USING expired WHERE tasks.id=expired.id`); err != nil {
		return a2astate.Task{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_a2a_tasks WHERE owner_key=$1`, task.OwnerKey).Scan(&count); err != nil {
		return a2astate.Task{}, err
	}
	if count >= ownerQuota {
		return a2astate.Task{}, a2astate.ErrQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_a2a_tasks
		(id,owner_key,agent_id,model,context_id,state,payload,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now()+make_interval(secs=>$8)) ON CONFLICT DO NOTHING`,
		task.ID, task.OwnerKey, task.AgentID, task.Model, task.ContextID, task.State, task.Payload, int64(ttl/time.Second))
	if err != nil {
		return a2astate.Task{}, err
	}
	if command.RowsAffected() != 1 {
		return a2astate.Task{}, a2astate.ErrConflict
	}
	if err := insertA2AOutboxJob(ctx, tx, job); err != nil {
		return a2astate.Task{}, err
	}
	created, err := getA2ATask(ctx, tx, task.OwnerKey, task.AgentID, task.ID)
	if err != nil {
		return a2astate.Task{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return a2astate.Task{}, err
	}
	return created, nil
}

func (s *PostgresStore) UpdateA2ATaskWithJob(ctx context.Context, task a2astate.Task, expectedUpdatedAt time.Time, ttl time.Duration, job asyncstate.Job) (a2astate.Task, error) {
	if s == nil || s.pool == nil {
		return a2astate.Task{}, a2astate.ErrUnavailable
	}
	if !validA2ATask(task) || expectedUpdatedAt.IsZero() || ttl < time.Second || ttl > 365*24*time.Hour || !validA2AOutboxJob(task, job) {
		return a2astate.Task{}, a2astate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return a2astate.Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	updated, err := scanA2ATask(tx.QueryRow(ctx, `UPDATE gateway_a2a_tasks SET state=$5,payload=$6,
		updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond'),expires_at=GREATEST(expires_at,clock_timestamp()+make_interval(secs=>$8))
		WHERE id=$1 AND owner_key=$2 AND agent_id=$3 AND model=$4 AND context_id=$7 AND updated_at=$9 AND expires_at>now()
		RETURNING id,owner_key,agent_id,model,context_id,state,payload,created_at,updated_at,expires_at`,
		task.ID, task.OwnerKey, task.AgentID, task.Model, task.State, task.Payload, task.ContextID, int64(ttl/time.Second), expectedUpdatedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := getA2ATask(ctx, tx, task.OwnerKey, task.AgentID, task.ID); getErr == nil {
			return a2astate.Task{}, a2astate.ErrConflict
		} else if errors.Is(getErr, a2astate.ErrNotFound) {
			return a2astate.Task{}, a2astate.ErrNotFound
		} else {
			return a2astate.Task{}, getErr
		}
	}
	if err != nil {
		return a2astate.Task{}, err
	}
	if err := insertA2AOutboxJob(ctx, tx, job); err != nil {
		return a2astate.Task{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return a2astate.Task{}, err
	}
	return updated, nil
}

func validA2AOutboxJob(task a2astate.Task, job asyncstate.Job) bool {
	return asyncstate.Valid(job) && job.ResourceID == task.ID && job.OwnerKey == task.OwnerKey && job.EndpointID == task.AgentID
}

func insertA2AOutboxJob(ctx context.Context, tx pgx.Tx, job asyncstate.Job) error {
	var availableAt *time.Time
	if !job.AvailableAt.IsZero() {
		value := job.AvailableAt.UTC()
		availableAt = &value
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_async_jobs (kind,resource_id,owner_key,endpoint_id,execution_id,payload,available_at)
		VALUES ($1,$2,$3,$4,$5,$6,COALESCE($7::timestamptz,now())) ON CONFLICT DO NOTHING`,
		job.Kind, job.ResourceID, job.OwnerKey, job.EndpointID, job.ExecutionID, job.Payload, availableAt)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return asyncstate.ErrConflict
	}
	return nil
}
