package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/assistantstate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateAssistant(ctx context.Context, record assistantstate.Record, ownerQuota int) (assistantstate.Record, error) {
	if s == nil || s.pool == nil {
		return assistantstate.Record{}, assistantstate.ErrUnavailable
	}
	if !validAssistantRecord(record) || ownerQuota < 1 {
		return assistantstate.Record{}, assistantstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return assistantstate.Record{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 5))`, record.OwnerKey); err != nil {
		return assistantstate.Record{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_assistants WHERE owner_key=$1`, record.OwnerKey).Scan(&count); err != nil {
		return assistantstate.Record{}, err
	}
	if count >= ownerQuota {
		return assistantstate.Record{}, assistantstate.ErrQuotaExceeded
	}
	created, err := scanAssistant(tx.QueryRow(ctx, `INSERT INTO gateway_assistants (id,owner_key,snapshot)
		VALUES ($1,$2,$3::jsonb) ON CONFLICT DO NOTHING
		RETURNING id,owner_key,snapshot,revision,created_at,updated_at`, record.ID, record.OwnerKey, record.Snapshot))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.Record{}, assistantstate.ErrConflict
	}
	if err != nil {
		return assistantstate.Record{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return assistantstate.Record{}, err
	}
	return created, nil
}

func (s *PostgresStore) ListAssistants(ctx context.Context, owner string, limit int, after string) ([]assistantstate.Record, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", assistantstate.ErrUnavailable
	}
	if owner == "" || limit < 1 || limit > 100 {
		return nil, "", assistantstate.ErrInvalid
	}
	var cursor *time.Time
	var cursorID string
	if after != "" {
		var created time.Time
		if err := s.pool.QueryRow(ctx, `SELECT created_at,id FROM gateway_assistants WHERE owner_key=$1 AND id=$2`, owner, after).Scan(&created, &cursorID); errors.Is(err, pgx.ErrNoRows) {
			return nil, "", assistantstate.ErrNotFound
		} else if err != nil {
			return nil, "", err
		}
		cursor = &created
	}
	rows, err := s.pool.Query(ctx, `SELECT id,owner_key,snapshot,revision,created_at,updated_at
		FROM gateway_assistants WHERE owner_key=$1
		AND ($2::timestamptz IS NULL OR (created_at,id)<($2::timestamptz,$3))
		ORDER BY created_at DESC,id DESC LIMIT $4`, owner, cursor, cursorID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	records := make([]assistantstate.Record, 0, limit+1)
	for rows.Next() {
		record, scanErr := scanAssistant(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(records) > limit {
		next = records[limit-1].ID
		records = records[:limit]
	}
	return records, next, nil
}

func (s *PostgresStore) GetAssistant(ctx context.Context, owner, id string) (assistantstate.Record, error) {
	if s == nil || s.pool == nil {
		return assistantstate.Record{}, assistantstate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return assistantstate.Record{}, assistantstate.ErrInvalid
	}
	record, err := scanAssistant(s.pool.QueryRow(ctx, `SELECT id,owner_key,snapshot,revision,created_at,updated_at FROM gateway_assistants WHERE owner_key=$1 AND id=$2`, owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.Record{}, assistantstate.ErrNotFound
	}
	return record, err
}

func (s *PostgresStore) UpdateAssistant(ctx context.Context, owner, id string, snapshot []byte, expectedRevision int64) (assistantstate.Record, error) {
	if s == nil || s.pool == nil {
		return assistantstate.Record{}, assistantstate.ErrUnavailable
	}
	if owner == "" || id == "" || !validAssistantSnapshot(snapshot) || expectedRevision < 1 {
		return assistantstate.Record{}, assistantstate.ErrInvalid
	}
	record, err := scanAssistant(s.pool.QueryRow(ctx, `UPDATE gateway_assistants SET snapshot=$3::jsonb,revision=revision+1,updated_at=now()
		WHERE owner_key=$1 AND id=$2 AND revision=$4
		RETURNING id,owner_key,snapshot,revision,created_at,updated_at`, owner, id, snapshot, expectedRevision))
	if !errors.Is(err, pgx.ErrNoRows) {
		return record, err
	}
	var found bool
	if queryErr := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_assistants WHERE owner_key=$1 AND id=$2)`, owner, id).Scan(&found); queryErr != nil {
		return assistantstate.Record{}, queryErr
	}
	if found {
		return assistantstate.Record{}, assistantstate.ErrConflict
	}
	return assistantstate.Record{}, assistantstate.ErrNotFound
}

func (s *PostgresStore) DeleteAssistant(ctx context.Context, owner, id string) error {
	if s == nil || s.pool == nil {
		return assistantstate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return assistantstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_assistants WHERE owner_key=$1 AND id=$2`, owner, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return assistantstate.ErrNotFound
	}
	return nil
}

func validAssistantRecord(record assistantstate.Record) bool {
	return record.ID != "" && len(record.ID) <= 128 && record.OwnerKey != "" && len(record.OwnerKey) <= 256 && validAssistantSnapshot(record.Snapshot)
}

func validAssistantSnapshot(snapshot []byte) bool {
	if len(snapshot) < 2 || len(snapshot) > assistantstate.MaxSnapshotBytes {
		return false
	}
	var object map[string]any
	return json.Unmarshal(snapshot, &object) == nil && object != nil
}

type assistantScanner interface{ Scan(...any) error }

func scanAssistant(scanner assistantScanner) (assistantstate.Record, error) {
	var record assistantstate.Record
	if err := scanner.Scan(&record.ID, &record.OwnerKey, &record.Snapshot, &record.Revision, &record.CreatedAt, &record.UpdatedAt); err != nil {
		return assistantstate.Record{}, err
	}
	record.Snapshot = append([]byte(nil), record.Snapshot...)
	return record, nil
}
