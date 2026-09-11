package controlstore

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/asyncstate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateA2APushConfig(ctx context.Context, config a2astate.PushConfig, quota int, job asyncstate.Job) (a2astate.PushConfig, error) {
	if s == nil || s.pool == nil {
		return a2astate.PushConfig{}, a2astate.ErrUnavailable
	}
	if !validA2APushConfig(config) || quota < 1 || quota > 100 || !validA2APushJob(config, job) {
		return a2astate.PushConfig{}, a2astate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return a2astate.PushConfig{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := insertA2APushConfig(ctx, tx, config, quota); err != nil {
		return a2astate.PushConfig{}, err
	}
	if err := insertA2AOutboxJob(ctx, tx, job); err != nil {
		return a2astate.PushConfig{}, err
	}
	created, err := getA2APushConfig(ctx, tx, config.OwnerKey, config.AgentID, config.TaskID, config.ID)
	if err != nil {
		return a2astate.PushConfig{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return a2astate.PushConfig{}, err
	}
	return created, nil
}

func (s *PostgresStore) GetA2APushConfig(ctx context.Context, owner, agent, taskID, id string) (a2astate.PushConfig, error) {
	if s == nil || s.pool == nil {
		return a2astate.PushConfig{}, a2astate.ErrUnavailable
	}
	if owner == "" || !validA2AStorageToken(agent, 128) || !validA2AStorageToken(taskID, 128) || !validA2AStorageToken(id, 128) {
		return a2astate.PushConfig{}, a2astate.ErrInvalid
	}
	return getA2APushConfig(ctx, s.pool, owner, agent, taskID, id)
}

func (s *PostgresStore) ListA2APushConfigs(ctx context.Context, owner, agent, taskID string, limit int, after string) ([]a2astate.PushConfig, string, int, error) {
	if s == nil || s.pool == nil {
		return nil, "", 0, a2astate.ErrUnavailable
	}
	if owner == "" || !validA2AStorageToken(agent, 128) || !validA2AStorageToken(taskID, 128) || limit < 1 || limit > 100 || after != "" && !validA2AStorageToken(after, 128) {
		return nil, "", 0, a2astate.ErrInvalid
	}
	var cursorID string
	var cursorCreated *time.Time
	if after != "" {
		var createdAt time.Time
		if err := s.pool.QueryRow(ctx, `SELECT created_at,id FROM gateway_a2a_push_configs WHERE owner_key=$1 AND agent_id=$2 AND task_id=$3 AND id=$4`, owner, agent, taskID, after).Scan(&createdAt, &cursorID); errors.Is(err, pgx.ErrNoRows) {
			return nil, "", 0, a2astate.ErrNotFound
		} else if err != nil {
			return nil, "", 0, err
		}
		cursorCreated = &createdAt
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM gateway_a2a_push_configs WHERE owner_key=$1 AND agent_id=$2 AND task_id=$3`, owner, agent, taskID).Scan(&total); err != nil {
		return nil, "", 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id,task_id,owner_key,agent_id,payload,created_at,updated_at FROM gateway_a2a_push_configs
		WHERE owner_key=$1 AND agent_id=$2 AND task_id=$3 AND ($4::timestamptz IS NULL OR (created_at,id)<($4::timestamptz,$5))
		ORDER BY created_at DESC,id DESC LIMIT $6`, owner, agent, taskID, cursorCreated, cursorID, limit+1)
	if err != nil {
		return nil, "", 0, err
	}
	defer rows.Close()
	configs := make([]a2astate.PushConfig, 0, limit+1)
	for rows.Next() {
		config, scanErr := scanA2APushConfig(rows)
		if scanErr != nil {
			return nil, "", 0, scanErr
		}
		configs = append(configs, config)
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, err
	}
	next := ""
	if len(configs) > limit {
		next = configs[limit-1].ID
		configs = configs[:limit]
	}
	return configs, next, total, nil
}

func (s *PostgresStore) DeleteA2APushConfig(ctx context.Context, owner, agent, taskID, id string) error {
	if s == nil || s.pool == nil {
		return a2astate.ErrUnavailable
	}
	if owner == "" || !validA2AStorageToken(agent, 128) || !validA2AStorageToken(taskID, 128) || !validA2AStorageToken(id, 128) {
		return a2astate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_a2a_push_configs WHERE owner_key=$1 AND agent_id=$2 AND task_id=$3 AND id=$4`, owner, agent, taskID, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return a2astate.ErrNotFound
	}
	return nil
}

type a2aPushQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func insertA2APushConfig(ctx context.Context, tx pgx.Tx, config a2astate.PushConfig, quota int) error {
	if !validA2APushConfig(config) {
		return a2astate.ErrInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,hashtextextended($2,3)))`, config.OwnerKey, config.TaskID); err != nil {
		return err
	}
	var taskExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_a2a_tasks WHERE id=$1 AND owner_key=$2 AND agent_id=$3 AND expires_at>now())`, config.TaskID, config.OwnerKey, config.AgentID).Scan(&taskExists); err != nil {
		return err
	}
	if !taskExists {
		return a2astate.ErrNotFound
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM gateway_a2a_push_configs WHERE owner_key=$1 AND agent_id=$2 AND task_id=$3`, config.OwnerKey, config.AgentID, config.TaskID).Scan(&count); err != nil {
		return err
	}
	if count >= quota {
		return a2astate.ErrQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_a2a_push_configs (id,task_id,owner_key,agent_id,payload) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, config.ID, config.TaskID, config.OwnerKey, config.AgentID, config.Payload)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return a2astate.ErrConflict
	}
	return nil
}

func validA2APushConfig(config a2astate.PushConfig) bool {
	return validA2AStorageToken(config.ID, 128) && validA2AStorageToken(config.TaskID, 128) && config.OwnerKey != "" && len(config.OwnerKey) <= 256 && validA2AStorageToken(config.AgentID, 128) && len(config.Payload) > 0 && len(config.Payload) <= a2astate.MaxPushConfigPayloadBytes
}

func validA2APushJob(config a2astate.PushConfig, job asyncstate.Job) bool {
	return asyncstate.Valid(job) && job.ResourceID == config.TaskID+":"+config.ID && job.OwnerKey == config.OwnerKey && job.EndpointID == config.AgentID
}

func getA2APushConfig(ctx context.Context, query a2aPushQuerier, owner, agent, taskID, id string) (a2astate.PushConfig, error) {
	config, err := scanA2APushConfig(query.QueryRow(ctx, `SELECT id,task_id,owner_key,agent_id,payload,created_at,updated_at FROM gateway_a2a_push_configs WHERE owner_key=$1 AND agent_id=$2 AND task_id=$3 AND id=$4`, owner, agent, taskID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a2astate.PushConfig{}, a2astate.ErrNotFound
	}
	return config, err
}

type a2aPushScanner interface{ Scan(...any) error }

func scanA2APushConfig(row a2aPushScanner) (a2astate.PushConfig, error) {
	var config a2astate.PushConfig
	err := row.Scan(&config.ID, &config.TaskID, &config.OwnerKey, &config.AgentID, &config.Payload, &config.CreatedAt, &config.UpdatedAt)
	return config, err
}
