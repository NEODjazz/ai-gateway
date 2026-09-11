package controlstore

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/assistantstate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateThread(ctx context.Context, record assistantstate.ThreadRecord, ownerQuota int) (assistantstate.ThreadRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.ThreadRecord{}, assistantstate.ErrUnavailable
	}
	if record.ID == "" || len(record.ID) > 128 || record.OwnerKey == "" || len(record.OwnerKey) > 256 || !validAssistantSnapshot(record.Snapshot) || ownerQuota < 1 {
		return assistantstate.ThreadRecord{}, assistantstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return assistantstate.ThreadRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 6))`, record.OwnerKey); err != nil {
		return assistantstate.ThreadRecord{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_assistant_threads WHERE owner_key=$1`, record.OwnerKey).Scan(&count); err != nil {
		return assistantstate.ThreadRecord{}, err
	}
	if count >= ownerQuota {
		return assistantstate.ThreadRecord{}, assistantstate.ErrQuotaExceeded
	}
	created, err := scanAssistantThread(tx.QueryRow(ctx, `INSERT INTO gateway_assistant_threads (id,owner_key,snapshot) VALUES ($1,$2,$3::jsonb) ON CONFLICT DO NOTHING RETURNING id,owner_key,snapshot,revision,created_at,updated_at`, record.ID, record.OwnerKey, record.Snapshot))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.ThreadRecord{}, assistantstate.ErrConflict
	}
	if err != nil {
		return assistantstate.ThreadRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return assistantstate.ThreadRecord{}, err
	}
	return created, nil
}

func (s *PostgresStore) CreateThreadWithMessages(ctx context.Context, thread assistantstate.ThreadRecord, messages []assistantstate.MessageRecord, ownerQuota, messageQuota int) (assistantstate.ThreadRecord, []assistantstate.MessageRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.ThreadRecord{}, nil, assistantstate.ErrUnavailable
	}
	if thread.ID == "" || len(thread.ID) > 128 || thread.OwnerKey == "" || len(thread.OwnerKey) > 256 || !validAssistantSnapshot(thread.Snapshot) || ownerQuota < 1 || messageQuota < 1 || len(messages) < 1 || len(messages) > 100 {
		return assistantstate.ThreadRecord{}, nil, assistantstate.ErrInvalid
	}
	if len(messages) > messageQuota {
		return assistantstate.ThreadRecord{}, nil, assistantstate.ErrQuotaExceeded
	}
	seen := make(map[string]bool, len(messages))
	for _, message := range messages {
		if message.ID == "" || len(message.ID) > 128 || message.ThreadID != thread.ID || message.OwnerKey != thread.OwnerKey || !validAssistantMessageSnapshot(message.Snapshot) || seen[message.ID] {
			return assistantstate.ThreadRecord{}, nil, assistantstate.ErrInvalid
		}
		seen[message.ID] = true
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return assistantstate.ThreadRecord{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 6))`, thread.OwnerKey); err != nil {
		return assistantstate.ThreadRecord{}, nil, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_assistant_threads WHERE owner_key=$1`, thread.OwnerKey).Scan(&count); err != nil {
		return assistantstate.ThreadRecord{}, nil, err
	}
	if count >= ownerQuota {
		return assistantstate.ThreadRecord{}, nil, assistantstate.ErrQuotaExceeded
	}
	createdThread, err := scanAssistantThread(tx.QueryRow(ctx, `INSERT INTO gateway_assistant_threads (id,owner_key,snapshot) VALUES ($1,$2,$3::jsonb) ON CONFLICT DO NOTHING RETURNING id,owner_key,snapshot,revision,created_at,updated_at`, thread.ID, thread.OwnerKey, thread.Snapshot))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.ThreadRecord{}, nil, assistantstate.ErrConflict
	}
	if err != nil {
		return assistantstate.ThreadRecord{}, nil, err
	}
	createdMessages := make([]assistantstate.MessageRecord, 0, len(messages))
	for _, message := range messages {
		created, createErr := scanAssistantMessage(tx.QueryRow(ctx, `INSERT INTO gateway_assistant_messages (id,thread_id,owner_key,snapshot) VALUES ($1,$2,$3,$4::jsonb) ON CONFLICT DO NOTHING RETURNING id,thread_id,owner_key,snapshot,revision,created_at,updated_at`, message.ID, message.ThreadID, message.OwnerKey, message.Snapshot))
		if errors.Is(createErr, pgx.ErrNoRows) {
			return assistantstate.ThreadRecord{}, nil, assistantstate.ErrConflict
		}
		if createErr != nil {
			return assistantstate.ThreadRecord{}, nil, createErr
		}
		createdMessages = append(createdMessages, created)
	}
	if err = tx.Commit(ctx); err != nil {
		return assistantstate.ThreadRecord{}, nil, err
	}
	return createdThread, createdMessages, nil
}

func (s *PostgresStore) ListThreads(ctx context.Context, owner string, limit int, after string) ([]assistantstate.ThreadRecord, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", assistantstate.ErrUnavailable
	}
	if owner == "" || limit < 1 || limit > 100 {
		return nil, "", assistantstate.ErrInvalid
	}
	cursor, cursorID, err := assistantCursor(ctx, s, "gateway_assistant_threads", owner, after)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.pool.Query(ctx, `SELECT id,owner_key,snapshot,revision,created_at,updated_at FROM gateway_assistant_threads WHERE owner_key=$1 AND ($2::timestamptz IS NULL OR (created_at,id)<($2,$3)) ORDER BY created_at DESC,id DESC LIMIT $4`, owner, cursor, cursorID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	values := make([]assistantstate.ThreadRecord, 0, limit+1)
	for rows.Next() {
		value, scanErr := scanAssistantThread(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(values) > limit {
		next = values[limit-1].ID
		values = values[:limit]
	}
	return values, next, nil
}

func (s *PostgresStore) GetThread(ctx context.Context, owner, id string) (assistantstate.ThreadRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.ThreadRecord{}, assistantstate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return assistantstate.ThreadRecord{}, assistantstate.ErrInvalid
	}
	value, err := scanAssistantThread(s.pool.QueryRow(ctx, `SELECT id,owner_key,snapshot,revision,created_at,updated_at FROM gateway_assistant_threads WHERE owner_key=$1 AND id=$2`, owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.ThreadRecord{}, assistantstate.ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) UpdateThread(ctx context.Context, owner, id string, snapshot []byte, revision int64) (assistantstate.ThreadRecord, error) {
	if owner == "" || id == "" || !validAssistantSnapshot(snapshot) || revision < 1 {
		return assistantstate.ThreadRecord{}, assistantstate.ErrInvalid
	}
	if s == nil || s.pool == nil {
		return assistantstate.ThreadRecord{}, assistantstate.ErrUnavailable
	}
	value, err := scanAssistantThread(s.pool.QueryRow(ctx, `UPDATE gateway_assistant_threads SET snapshot=$3::jsonb,revision=revision+1,updated_at=now() WHERE owner_key=$1 AND id=$2 AND revision=$4 RETURNING id,owner_key,snapshot,revision,created_at,updated_at`, owner, id, snapshot, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.ThreadRecord{}, s.assistantConflictOrNotFound(ctx, "gateway_assistant_threads", owner, id)
	}
	return value, err
}

func (s *PostgresStore) DeleteThread(ctx context.Context, owner, id string) error {
	return s.deleteAssistantResource(ctx, "gateway_assistant_threads", owner, id)
}

func (s *PostgresStore) CreateThreadMessage(ctx context.Context, record assistantstate.MessageRecord, threadQuota int) (assistantstate.MessageRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.MessageRecord{}, assistantstate.ErrUnavailable
	}
	if record.ID == "" || len(record.ID) > 128 || record.ThreadID == "" || len(record.ThreadID) > 128 || record.OwnerKey == "" || len(record.OwnerKey) > 256 || !validAssistantMessageSnapshot(record.Snapshot) || threadQuota < 1 {
		return assistantstate.MessageRecord{}, assistantstate.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return assistantstate.MessageRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var found bool
	if err = tx.QueryRow(ctx, `SELECT true FROM gateway_assistant_threads WHERE owner_key=$1 AND id=$2 FOR UPDATE`, record.OwnerKey, record.ThreadID).Scan(&found); errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.MessageRecord{}, assistantstate.ErrNotFound
	} else if err != nil {
		return assistantstate.MessageRecord{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_assistant_messages WHERE owner_key=$1 AND thread_id=$2`, record.OwnerKey, record.ThreadID).Scan(&count); err != nil {
		return assistantstate.MessageRecord{}, err
	}
	if count >= threadQuota {
		return assistantstate.MessageRecord{}, assistantstate.ErrQuotaExceeded
	}
	created, err := scanAssistantMessage(tx.QueryRow(ctx, `INSERT INTO gateway_assistant_messages (id,thread_id,owner_key,snapshot) VALUES ($1,$2,$3,$4::jsonb) ON CONFLICT DO NOTHING RETURNING id,thread_id,owner_key,snapshot,revision,created_at,updated_at`, record.ID, record.ThreadID, record.OwnerKey, record.Snapshot))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.MessageRecord{}, assistantstate.ErrConflict
	}
	if err != nil {
		return assistantstate.MessageRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return assistantstate.MessageRecord{}, err
	}
	return created, nil
}

func (s *PostgresStore) ListThreadMessages(ctx context.Context, owner, threadID string, options assistantstate.MessagePageOptions) ([]assistantstate.MessageRecord, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", assistantstate.ErrUnavailable
	}
	if owner == "" || threadID == "" || options.Limit < 1 || options.Limit > 100 || options.After != "" && options.Before != "" || options.Order != "asc" && options.Order != "desc" {
		return nil, "", assistantstate.ErrInvalid
	}
	if _, err := s.GetThread(ctx, owner, threadID); err != nil {
		return nil, "", err
	}
	var cursor *time.Time
	var cursorID string
	boundary := options.After
	if boundary == "" {
		boundary = options.Before
	}
	if boundary != "" {
		var created time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at,id FROM gateway_assistant_messages WHERE owner_key=$1 AND thread_id=$2 AND id=$3`, owner, threadID, boundary).Scan(&created, &cursorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", assistantstate.ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		cursor = &created
	}
	comparison := "<"
	if options.Order == "asc" {
		comparison = ">"
	}
	if options.Before != "" {
		if comparison == "<" {
			comparison = ">"
		} else {
			comparison = "<"
		}
	}
	order := "DESC"
	if options.Order == "asc" {
		order = "ASC"
	}
	query := `SELECT id,thread_id,owner_key,snapshot,revision,created_at,updated_at FROM gateway_assistant_messages WHERE owner_key=$1 AND thread_id=$2 AND ($3::timestamptz IS NULL OR (created_at,id)` + comparison + `($3,$4)) ORDER BY created_at ` + order + `,id ` + order + ` LIMIT $5`
	rows, err := s.pool.Query(ctx, query, owner, threadID, cursor, cursorID, options.Limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	values := make([]assistantstate.MessageRecord, 0, options.Limit+1)
	for rows.Next() {
		value, scanErr := scanAssistantMessage(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(values) > options.Limit {
		next = values[options.Limit-1].ID
		values = values[:options.Limit]
	}
	return values, next, nil
}

func (s *PostgresStore) GetThreadMessage(ctx context.Context, owner, threadID, id string) (assistantstate.MessageRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.MessageRecord{}, assistantstate.ErrUnavailable
	}
	if owner == "" || threadID == "" || id == "" {
		return assistantstate.MessageRecord{}, assistantstate.ErrInvalid
	}
	value, err := scanAssistantMessage(s.pool.QueryRow(ctx, `SELECT id,thread_id,owner_key,snapshot,revision,created_at,updated_at FROM gateway_assistant_messages WHERE owner_key=$1 AND thread_id=$2 AND id=$3`, owner, threadID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.MessageRecord{}, assistantstate.ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) UpdateThreadMessage(ctx context.Context, owner, threadID, id string, snapshot []byte, revision int64) (assistantstate.MessageRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.MessageRecord{}, assistantstate.ErrUnavailable
	}
	if owner == "" || threadID == "" || id == "" || !validAssistantMessageSnapshot(snapshot) || revision < 1 {
		return assistantstate.MessageRecord{}, assistantstate.ErrInvalid
	}
	value, err := scanAssistantMessage(s.pool.QueryRow(ctx, `UPDATE gateway_assistant_messages SET snapshot=$4::jsonb,revision=revision+1,updated_at=now() WHERE owner_key=$1 AND thread_id=$2 AND id=$3 AND revision=$5 RETURNING id,thread_id,owner_key,snapshot,revision,created_at,updated_at`, owner, threadID, id, snapshot, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.MessageRecord{}, s.assistantMessageConflictOrNotFound(ctx, owner, threadID, id)
	}
	return value, err
}

func (s *PostgresStore) DeleteThreadMessage(ctx context.Context, owner, threadID, id string) error {
	if s == nil || s.pool == nil {
		return assistantstate.ErrUnavailable
	}
	if owner == "" || threadID == "" || id == "" {
		return assistantstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM gateway_assistant_messages WHERE owner_key=$1 AND thread_id=$2 AND id=$3`, owner, threadID, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return assistantstate.ErrNotFound
	}
	return nil
}

func assistantCursor(ctx context.Context, s *PostgresStore, table, owner, after string) (*time.Time, string, error) {
	if after == "" {
		return nil, "", nil
	}
	var created time.Time
	var id string
	query := `SELECT created_at,id FROM ` + table + ` WHERE owner_key=$1 AND id=$2`
	if err := s.pool.QueryRow(ctx, query, owner, after).Scan(&created, &id); errors.Is(err, pgx.ErrNoRows) {
		return nil, "", assistantstate.ErrNotFound
	} else if err != nil {
		return nil, "", err
	}
	return &created, id, nil
}

func (s *PostgresStore) assistantConflictOrNotFound(ctx context.Context, table, owner, id string) error {
	var found bool
	query := `SELECT EXISTS(SELECT 1 FROM ` + table + ` WHERE owner_key=$1 AND id=$2)`
	if err := s.pool.QueryRow(ctx, query, owner, id).Scan(&found); err != nil {
		return err
	}
	if found {
		return assistantstate.ErrConflict
	}
	return assistantstate.ErrNotFound
}

func (s *PostgresStore) assistantMessageConflictOrNotFound(ctx context.Context, owner, threadID, id string) error {
	var found bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_assistant_messages WHERE owner_key=$1 AND thread_id=$2 AND id=$3)`, owner, threadID, id).Scan(&found); err != nil {
		return err
	}
	if found {
		return assistantstate.ErrConflict
	}
	return assistantstate.ErrNotFound
}

func (s *PostgresStore) deleteAssistantResource(ctx context.Context, table, owner, id string) error {
	if s == nil || s.pool == nil {
		return assistantstate.ErrUnavailable
	}
	if owner == "" || id == "" {
		return assistantstate.ErrInvalid
	}
	command, err := s.pool.Exec(ctx, `DELETE FROM `+table+` WHERE owner_key=$1 AND id=$2`, owner, id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return assistantstate.ErrNotFound
	}
	return nil
}

func validAssistantMessageSnapshot(snapshot []byte) bool {
	return validAssistantJSONSnapshot(snapshot, assistantstate.MaxMessageSnapshotBytes)
}

func scanAssistantThread(scanner assistantScanner) (assistantstate.ThreadRecord, error) {
	var value assistantstate.ThreadRecord
	err := scanner.Scan(&value.ID, &value.OwnerKey, &value.Snapshot, &value.Revision, &value.CreatedAt, &value.UpdatedAt)
	value.Snapshot = append([]byte(nil), value.Snapshot...)
	return value, err
}
func scanAssistantMessage(scanner assistantScanner) (assistantstate.MessageRecord, error) {
	var value assistantstate.MessageRecord
	err := scanner.Scan(&value.ID, &value.ThreadID, &value.OwnerKey, &value.Snapshot, &value.Revision, &value.CreatedAt, &value.UpdatedAt)
	value.Snapshot = append([]byte(nil), value.Snapshot...)
	return value, err
}
