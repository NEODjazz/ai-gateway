package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/openai"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateContainerRecord(ctx context.Context, record containerstate.Record, ownerQuota int) (containerstate.Record, error) {
	if s == nil || s.pool == nil {
		return containerstate.Record{}, containerstate.ErrUnavailable
	}
	payload, err := containerRecordPayload(record)
	if err != nil || ownerQuota < 1 || ownerQuota > 100000 {
		return containerstate.Record{}, containerstate.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return containerstate.Record{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 11))`, record.OwnerKey); err != nil {
		return containerstate.Record{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_containers WHERE owner_key=$1`, record.OwnerKey).Scan(&count); err != nil {
		return containerstate.Record{}, err
	}
	if count >= ownerQuota {
		return containerstate.Record{}, containerstate.ErrQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_containers (container_id,owner_key,endpoint,model,deployment,snapshot) VALUES ($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT DO NOTHING`, record.Container.ID, record.OwnerKey, record.Binding.Endpoint, record.Binding.Model, record.Binding.Deployment, payload)
	if err != nil {
		return containerstate.Record{}, err
	}
	if command.RowsAffected() != 1 {
		return containerstate.Record{}, containerstate.ErrConflict
	}
	created, err := getContainerRecord(ctx, tx, record.OwnerKey, record.Container.ID)
	if err != nil {
		return containerstate.Record{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return containerstate.Record{}, err
	}
	return created, nil
}

func containerRecordPayload(record containerstate.Record) ([]byte, error) {
	if record.OwnerKey == "" || len(record.OwnerKey) > 256 || record.Container.ID == "" || len(record.Container.ID) > 128 || record.Binding.Endpoint == "" || len(record.Binding.Endpoint) > 128 || record.Binding.Model == "" || len(record.Binding.Model) > 256 || len(record.Binding.Deployment) != 64 {
		return nil, containerstate.ErrInvalid
	}
	payload, err := json.Marshal(record.Container)
	if err != nil || len(payload) > 1<<20 {
		return nil, containerstate.ErrInvalid
	}
	return payload, nil
}

func (s *PostgresStore) GetContainerRecord(ctx context.Context, owner, id string) (containerstate.Record, error) {
	if s == nil || s.pool == nil {
		return containerstate.Record{}, containerstate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return containerstate.Record{}, containerstate.ErrInvalid
	}
	return getContainerRecord(ctx, s.pool, owner, id)
}

func getContainerRecord(ctx context.Context, q fineTuningQuerier, owner, id string) (containerstate.Record, error) {
	var record containerstate.Record
	var payload []byte
	record.OwnerKey = owner
	err := q.QueryRow(ctx, `SELECT endpoint,model,deployment,snapshot,created_at,updated_at FROM gateway_containers WHERE owner_key=$1 AND container_id=$2`, owner, id).Scan(&record.Binding.Endpoint, &record.Binding.Model, &record.Binding.Deployment, &payload, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return containerstate.Record{}, containerstate.ErrNotFound
	}
	if err != nil {
		return containerstate.Record{}, err
	}
	if json.Unmarshal(payload, &record.Container) != nil {
		return containerstate.Record{}, containerstate.ErrUnavailable
	}
	return record, nil
}

func (s *PostgresStore) ListContainerRecords(ctx context.Context, owner string, limit int, after string) ([]containerstate.Record, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", containerstate.ErrUnavailable
	}
	if owner == "" || limit < 1 || limit > 100 {
		return nil, "", containerstate.ErrInvalid
	}
	var cursor *time.Time
	var cursorID string
	if after != "" {
		var created time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at,container_id FROM gateway_containers WHERE owner_key=$1 AND container_id=$2`, owner, after).Scan(&created, &cursorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", containerstate.ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		cursor = &created
	}
	rows, err := s.pool.Query(ctx, `SELECT container_id,endpoint,model,deployment,snapshot,created_at,updated_at FROM gateway_containers WHERE owner_key=$1 AND ($2::timestamptz IS NULL OR (created_at,container_id)<($2,$3)) ORDER BY created_at DESC,container_id DESC LIMIT $4`, owner, cursor, cursorID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	result := make([]containerstate.Record, 0, limit+1)
	for rows.Next() {
		var record containerstate.Record
		var payload []byte
		record.OwnerKey = owner
		if err := rows.Scan(&record.Container.ID, &record.Binding.Endpoint, &record.Binding.Model, &record.Binding.Deployment, &payload, &record.CreatedAt, &record.UpdatedAt); err != nil {
			return nil, "", err
		}
		if json.Unmarshal(payload, &record.Container) != nil {
			return nil, "", containerstate.ErrUnavailable
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(result) > limit {
		next = result[limit-1].Container.ID
		result = result[:limit]
	}
	return result, next, nil
}

func (s *PostgresStore) UpdateContainerRecord(ctx context.Context, owner string, container openai.Container) (containerstate.Record, error) {
	if s == nil || s.pool == nil {
		return containerstate.Record{}, containerstate.ErrUnavailable
	}
	payload, err := json.Marshal(container)
	if owner == "" || container.ID == "" || err != nil || len(payload) > 1<<20 {
		return containerstate.Record{}, containerstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `UPDATE gateway_containers SET snapshot=$3::jsonb,updated_at=now() WHERE owner_key=$1 AND container_id=$2`, owner, container.ID, payload)
	if err != nil {
		return containerstate.Record{}, err
	}
	if command.RowsAffected() != 1 {
		return containerstate.Record{}, containerstate.ErrNotFound
	}
	return s.GetContainerRecord(ctx, owner, container.ID)
}

func (s *PostgresStore) DeleteContainerRecord(ctx context.Context, owner, id string) error {
	if s == nil || s.pool == nil {
		return containerstate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return containerstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_containers WHERE owner_key=$1 AND container_id=$2`, owner, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return containerstate.ErrNotFound
	}
	return nil
}
