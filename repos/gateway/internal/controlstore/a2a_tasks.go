package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateA2ATask(ctx context.Context, task a2astate.Task, ownerQuota int, ttl time.Duration) (a2astate.Task, error) {
	if s == nil || s.pool == nil {
		return a2astate.Task{}, a2astate.ErrUnavailable
	}
	if !validA2ATask(task) || ownerQuota < 1 || ownerQuota > 100000 || ttl < time.Second || ttl > 365*24*time.Hour {
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
	created, err := getA2ATask(ctx, tx, task.OwnerKey, task.AgentID, task.ID)
	if err != nil {
		return a2astate.Task{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return a2astate.Task{}, err
	}
	return created, nil
}

func (s *PostgresStore) GetA2ATask(ctx context.Context, owner, agent, id string) (a2astate.Task, error) {
	if s == nil || s.pool == nil {
		return a2astate.Task{}, a2astate.ErrUnavailable
	}
	if owner == "" || !validA2AStorageToken(agent, 128) || !validA2AStorageToken(id, 128) {
		return a2astate.Task{}, a2astate.ErrInvalid
	}
	return getA2ATask(ctx, s.pool, owner, agent, id)
}

func (s *PostgresStore) UpdateA2ATask(ctx context.Context, task a2astate.Task, expectedUpdatedAt time.Time, ttl time.Duration) (a2astate.Task, error) {
	if s == nil || s.pool == nil {
		return a2astate.Task{}, a2astate.ErrUnavailable
	}
	if !validA2ATask(task) || expectedUpdatedAt.IsZero() || ttl < time.Second || ttl > 365*24*time.Hour {
		return a2astate.Task{}, a2astate.ErrInvalid
	}
	updated, err := scanA2ATask(s.pool.QueryRow(ctx, `UPDATE gateway_a2a_tasks SET state=$5,payload=$6,
		updated_at=GREATEST(clock_timestamp(),updated_at+interval '1 microsecond'),expires_at=GREATEST(expires_at,clock_timestamp()+make_interval(secs=>$8))
		WHERE id=$1 AND owner_key=$2 AND agent_id=$3 AND model=$4 AND context_id=$7 AND updated_at=$9 AND expires_at>now()
		RETURNING id,owner_key,agent_id,model,context_id,state,payload,created_at,updated_at,expires_at`,
		task.ID, task.OwnerKey, task.AgentID, task.Model, task.State, task.Payload, task.ContextID, int64(ttl/time.Second), expectedUpdatedAt))
	if !errors.Is(err, pgx.ErrNoRows) {
		return updated, err
	}
	if _, getErr := getA2ATask(ctx, s.pool, task.OwnerKey, task.AgentID, task.ID); getErr == nil {
		return a2astate.Task{}, a2astate.ErrConflict
	} else if errors.Is(getErr, a2astate.ErrNotFound) {
		return a2astate.Task{}, a2astate.ErrNotFound
	} else {
		return a2astate.Task{}, getErr
	}
}

func (s *PostgresStore) ListA2ATasks(ctx context.Context, owner, agent string, options a2astate.ListOptions) ([]a2astate.Task, string, int, error) {
	if s == nil || s.pool == nil {
		return nil, "", 0, a2astate.ErrUnavailable
	}
	if owner == "" || !validA2AStorageToken(agent, 128) || options.Model == "" || len(options.Model) > 256 || options.Limit < 1 || options.Limit > 100 ||
		(options.After != "" && !validA2AStorageToken(options.After, 128)) ||
		(options.ContextID != "" && !validA2AStorageToken(options.ContextID, 128)) ||
		(options.State != "" && !validA2AStorageToken(options.State, 64)) {
		return nil, "", 0, a2astate.ErrInvalid
	}
	var cursorTime *time.Time
	var cursorID string
	if options.After != "" {
		var createdAt time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at,id FROM gateway_a2a_tasks
			WHERE owner_key=$1 AND agent_id=$2 AND model=$3 AND id=$4 AND expires_at>now()
			AND ($5='' OR context_id=$5) AND ($6='' OR state=$6) AND ($7::timestamptz IS NULL OR updated_at>=$7)`,
			owner, agent, options.Model, options.After, options.ContextID, options.State, options.UpdatedAfter).Scan(&createdAt, &cursorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", 0, a2astate.ErrNotFound
		}
		if err != nil {
			return nil, "", 0, err
		}
		cursorTime = &createdAt
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM gateway_a2a_tasks WHERE owner_key=$1 AND agent_id=$2 AND model=$3 AND expires_at>now()
		AND ($4='' OR context_id=$4) AND ($5='' OR state=$5) AND ($6::timestamptz IS NULL OR updated_at>=$6)`,
		owner, agent, options.Model, options.ContextID, options.State, options.UpdatedAfter).Scan(&total); err != nil {
		return nil, "", 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id,owner_key,agent_id,model,context_id,state,payload,created_at,updated_at,expires_at
		FROM gateway_a2a_tasks WHERE owner_key=$1 AND agent_id=$2 AND model=$3 AND expires_at>now()
		AND ($4='' OR context_id=$4) AND ($5='' OR state=$5) AND ($6::timestamptz IS NULL OR updated_at>=$6)
		AND ($7::timestamptz IS NULL OR (created_at,id)<($7::timestamptz,$8))
		ORDER BY created_at DESC,id DESC LIMIT $9`, owner, agent, options.Model, options.ContextID, options.State, options.UpdatedAfter, cursorTime, cursorID, options.Limit+1)
	if err != nil {
		return nil, "", 0, err
	}
	defer rows.Close()
	tasks := make([]a2astate.Task, 0, options.Limit+1)
	for rows.Next() {
		task, scanErr := scanA2ATask(rows)
		if scanErr != nil {
			return nil, "", 0, scanErr
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, err
	}
	next := ""
	if len(tasks) > options.Limit {
		next = tasks[options.Limit-1].ID
		tasks = tasks[:options.Limit]
	}
	return tasks, next, total, nil
}

func validA2ATask(task a2astate.Task) bool {
	return validA2AStorageToken(task.ID, 128) && task.OwnerKey != "" && len(task.OwnerKey) <= 256 &&
		validA2AStorageToken(task.AgentID, 128) && task.Model != "" && len(task.Model) <= 256 && validA2AStorageToken(task.ContextID, 128) &&
		validA2AStorageToken(task.State, 64) && len(task.Payload) > 0 && len(task.Payload) <= a2astate.MaxPayloadBytes && json.Valid(task.Payload)
}

func validA2AStorageToken(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

type a2aTaskQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getA2ATask(ctx context.Context, query a2aTaskQuerier, owner, agent, id string) (a2astate.Task, error) {
	task, err := scanA2ATask(query.QueryRow(ctx, `SELECT id,owner_key,agent_id,model,context_id,state,payload,created_at,updated_at,expires_at
		FROM gateway_a2a_tasks WHERE owner_key=$1 AND agent_id=$2 AND id=$3 AND expires_at>now()`, owner, agent, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a2astate.Task{}, a2astate.ErrNotFound
	}
	return task, err
}

type a2aTaskScanner interface{ Scan(...any) error }

func scanA2ATask(row a2aTaskScanner) (a2astate.Task, error) {
	var task a2astate.Task
	err := row.Scan(&task.ID, &task.OwnerKey, &task.AgentID, &task.Model, &task.ContextID, &task.State, &task.Payload, &task.CreatedAt, &task.UpdatedAt, &task.ExpiresAt)
	return task, err
}
