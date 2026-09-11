package controlstore

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/assistantstate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateRun(ctx context.Context, record assistantstate.RunRecord, ownerQuota int) (assistantstate.RunRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.RunRecord{}, assistantstate.ErrUnavailable
	}
	if !validAssistantRun(record) || record.Status != "queued" || ownerQuota < 1 || ownerQuota > 100000 {
		return assistantstate.RunRecord{}, assistantstate.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return assistantstate.RunRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 7))`, record.OwnerKey); err != nil {
		return assistantstate.RunRecord{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM gateway_assistant_runs WHERE owner_key=$1 AND retain_until<=now()`, record.OwnerKey); err != nil {
		return assistantstate.RunRecord{}, err
	}
	var parent bool
	if err = tx.QueryRow(ctx, `SELECT true FROM gateway_assistant_threads WHERE owner_key=$1 AND id=$2 FOR UPDATE`, record.OwnerKey, record.ThreadID).Scan(&parent); errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunRecord{}, assistantstate.ErrNotFound
	} else if err != nil {
		return assistantstate.RunRecord{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_assistant_runs WHERE owner_key=$1`, record.OwnerKey).Scan(&count); err != nil {
		return assistantstate.RunRecord{}, err
	}
	if count >= ownerQuota {
		return assistantstate.RunRecord{}, assistantstate.ErrQuotaExceeded
	}
	created, err := scanAssistantRun(tx.QueryRow(ctx, `INSERT INTO gateway_assistant_runs (id,thread_id,owner_key,status,snapshot,retain_until) VALUES ($1,$2,$3,$4,$5::jsonb,$6) ON CONFLICT DO NOTHING RETURNING id,thread_id,owner_key,status,snapshot,revision,retain_until,created_at,updated_at`, record.ID, record.ThreadID, record.OwnerKey, record.Status, record.Snapshot, record.RetainUntil))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunRecord{}, assistantstate.ErrConflict
	}
	if err != nil {
		return assistantstate.RunRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return assistantstate.RunRecord{}, err
	}
	return created, nil
}

func (s *PostgresStore) ListRuns(ctx context.Context, owner, threadID string, options assistantstate.RunPageOptions) ([]assistantstate.RunRecord, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", assistantstate.ErrUnavailable
	}
	if !validAssistantRunPage(owner, threadID, options) {
		return nil, "", assistantstate.ErrInvalid
	}
	if _, err := s.GetThread(ctx, owner, threadID); err != nil {
		return nil, "", err
	}
	cursor, cursorID, err := assistantRunCursor(ctx, s, owner, threadID, "", options)
	if err != nil {
		return nil, "", err
	}
	comparison, order := assistantRunPageDirection(options)
	query := `SELECT id,thread_id,owner_key,status,snapshot,revision,retain_until,created_at,updated_at FROM gateway_assistant_runs WHERE owner_key=$1 AND thread_id=$2 AND ($3::timestamptz IS NULL OR (created_at,id)` + comparison + `($3,$4)) ORDER BY created_at ` + order + `,id ` + order + ` LIMIT $5`
	rows, err := s.pool.Query(ctx, query, owner, threadID, cursor, cursorID, options.Limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	values := make([]assistantstate.RunRecord, 0, options.Limit+1)
	for rows.Next() {
		value, scanErr := scanAssistantRun(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	return assistantRunPage(values, options.Limit)
}

func (s *PostgresStore) GetRun(ctx context.Context, owner, threadID, id string) (assistantstate.RunRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.RunRecord{}, assistantstate.ErrUnavailable
	}
	if owner == "" || !validA2AStorageToken(threadID, 128) || !validA2AStorageToken(id, 128) {
		return assistantstate.RunRecord{}, assistantstate.ErrInvalid
	}
	value, err := scanAssistantRun(s.pool.QueryRow(ctx, `SELECT id,thread_id,owner_key,status,snapshot,revision,retain_until,created_at,updated_at FROM gateway_assistant_runs WHERE owner_key=$1 AND thread_id=$2 AND id=$3`, owner, threadID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunRecord{}, assistantstate.ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) TransitionRun(ctx context.Context, record assistantstate.RunRecord, expectedStatus string, expectedRevision int64) (assistantstate.RunRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.RunRecord{}, assistantstate.ErrUnavailable
	}
	if !validAssistantRun(record) || !assistantstate.ValidRunTransition(expectedStatus, record.Status) || expectedRevision < 1 {
		return assistantstate.RunRecord{}, assistantstate.ErrInvalid
	}
	value, err := scanAssistantRun(s.pool.QueryRow(ctx, `UPDATE gateway_assistant_runs SET status=$4,snapshot=$5::jsonb,revision=revision+1,updated_at=now() WHERE owner_key=$1 AND thread_id=$2 AND id=$3 AND status=$6 AND revision=$7 RETURNING id,thread_id,owner_key,status,snapshot,revision,retain_until,created_at,updated_at`, record.OwnerKey, record.ThreadID, record.ID, record.Status, record.Snapshot, expectedStatus, expectedRevision))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunRecord{}, s.assistantRunConflictOrNotFound(ctx, record.OwnerKey, record.ThreadID, record.ID)
	}
	return value, err
}

func (s *PostgresStore) CreateRunStep(ctx context.Context, record assistantstate.RunStepRecord, runQuota int) (assistantstate.RunStepRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.RunStepRecord{}, assistantstate.ErrUnavailable
	}
	if !validAssistantRunStep(record) || record.Status != "in_progress" || runQuota < 1 || runQuota > 100000 {
		return assistantstate.RunStepRecord{}, assistantstate.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return assistantstate.RunStepRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var runStatus string
	if err = tx.QueryRow(ctx, `SELECT status FROM gateway_assistant_runs WHERE owner_key=$1 AND thread_id=$2 AND id=$3 FOR UPDATE`, record.OwnerKey, record.ThreadID, record.RunID).Scan(&runStatus); errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunStepRecord{}, assistantstate.ErrNotFound
	} else if err != nil {
		return assistantstate.RunStepRecord{}, err
	}
	if runStatus != "in_progress" && runStatus != "requires_action" {
		return assistantstate.RunStepRecord{}, assistantstate.ErrConflict
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_assistant_run_steps WHERE owner_key=$1 AND thread_id=$2 AND run_id=$3`, record.OwnerKey, record.ThreadID, record.RunID).Scan(&count); err != nil {
		return assistantstate.RunStepRecord{}, err
	}
	if count >= runQuota {
		return assistantstate.RunStepRecord{}, assistantstate.ErrQuotaExceeded
	}
	created, err := scanAssistantRunStep(tx.QueryRow(ctx, `INSERT INTO gateway_assistant_run_steps (id,run_id,thread_id,owner_key,status,snapshot) VALUES ($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT DO NOTHING RETURNING id,run_id,thread_id,owner_key,status,snapshot,revision,created_at,updated_at`, record.ID, record.RunID, record.ThreadID, record.OwnerKey, record.Status, record.Snapshot))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunStepRecord{}, assistantstate.ErrConflict
	}
	if err != nil {
		return assistantstate.RunStepRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return assistantstate.RunStepRecord{}, err
	}
	return created, nil
}

func (s *PostgresStore) ListRunSteps(ctx context.Context, owner, threadID, runID string, options assistantstate.RunPageOptions) ([]assistantstate.RunStepRecord, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", assistantstate.ErrUnavailable
	}
	if !validAssistantRunPage(owner, threadID, options) || !validA2AStorageToken(runID, 128) {
		return nil, "", assistantstate.ErrInvalid
	}
	if _, err := s.GetRun(ctx, owner, threadID, runID); err != nil {
		return nil, "", err
	}
	cursor, cursorID, err := assistantRunCursor(ctx, s, owner, threadID, runID, options)
	if err != nil {
		return nil, "", err
	}
	comparison, order := assistantRunPageDirection(options)
	query := `SELECT id,run_id,thread_id,owner_key,status,snapshot,revision,created_at,updated_at FROM gateway_assistant_run_steps WHERE owner_key=$1 AND thread_id=$2 AND run_id=$3 AND ($4::timestamptz IS NULL OR (created_at,id)` + comparison + `($4,$5)) ORDER BY created_at ` + order + `,id ` + order + ` LIMIT $6`
	rows, err := s.pool.Query(ctx, query, owner, threadID, runID, cursor, cursorID, options.Limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	values := make([]assistantstate.RunStepRecord, 0, options.Limit+1)
	for rows.Next() {
		value, scanErr := scanAssistantRunStep(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(values) > options.Limit {
		next = values[options.Limit-1].ID
		values = values[:options.Limit]
	}
	return values, next, nil
}

func (s *PostgresStore) GetRunStep(ctx context.Context, owner, threadID, runID, id string) (assistantstate.RunStepRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.RunStepRecord{}, assistantstate.ErrUnavailable
	}
	if owner == "" || !validA2AStorageToken(threadID, 128) || !validA2AStorageToken(runID, 128) || !validA2AStorageToken(id, 128) {
		return assistantstate.RunStepRecord{}, assistantstate.ErrInvalid
	}
	value, err := scanAssistantRunStep(s.pool.QueryRow(ctx, `SELECT id,run_id,thread_id,owner_key,status,snapshot,revision,created_at,updated_at FROM gateway_assistant_run_steps WHERE owner_key=$1 AND thread_id=$2 AND run_id=$3 AND id=$4`, owner, threadID, runID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunStepRecord{}, assistantstate.ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) UpdateRunStep(ctx context.Context, record assistantstate.RunStepRecord, expectedStatus string, expectedRevision int64) (assistantstate.RunStepRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.RunStepRecord{}, assistantstate.ErrUnavailable
	}
	if !validAssistantRunStep(record) || !assistantstate.ValidRunStepTransition(expectedStatus, record.Status) || expectedRevision < 1 {
		return assistantstate.RunStepRecord{}, assistantstate.ErrInvalid
	}
	value, err := scanAssistantRunStep(s.pool.QueryRow(ctx, `UPDATE gateway_assistant_run_steps SET status=$5,snapshot=$6::jsonb,revision=revision+1,updated_at=now() WHERE owner_key=$1 AND thread_id=$2 AND run_id=$3 AND id=$4 AND status=$7 AND revision=$8 RETURNING id,run_id,thread_id,owner_key,status,snapshot,revision,created_at,updated_at`, record.OwnerKey, record.ThreadID, record.RunID, record.ID, record.Status, record.Snapshot, expectedStatus, expectedRevision))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunStepRecord{}, s.assistantRunStepConflictOrNotFound(ctx, record.OwnerKey, record.ThreadID, record.RunID, record.ID)
	}
	return value, err
}

func (s *PostgresStore) CompleteRun(ctx context.Context, run assistantstate.RunRecord, expectedStatus string, expectedRevision int64, step assistantstate.RunStepRecord, message *assistantstate.MessageRecord, messageQuota, stepQuota int) (assistantstate.RunRecord, assistantstate.RunStepRecord, *assistantstate.MessageRecord, error) {
	if s == nil || s.pool == nil {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrUnavailable
	}
	validSource := expectedStatus == "queued" || expectedStatus == "in_progress"
	if !validSource || expectedRevision < 1 || run.Status != "completed" || !validAssistantRun(run) || step.Status != "completed" || !validAssistantRunStep(step) || step.RunID != run.ID || step.ThreadID != run.ThreadID || step.OwnerKey != run.OwnerKey || messageQuota < 1 || messageQuota > 1_000_000 || stepQuota < 1 || stepQuota > 100_000 {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrInvalid
	}
	if message != nil && (message.ID == "" || len(message.ID) > 128 || message.ThreadID != run.ThreadID || message.OwnerKey != run.OwnerKey || !validAssistantMessageSnapshot(message.Snapshot)) {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var parent bool
	if err = tx.QueryRow(ctx, `SELECT true FROM gateway_assistant_threads WHERE owner_key=$1 AND id=$2 FOR UPDATE`, run.OwnerKey, run.ThreadID).Scan(&parent); errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrNotFound
	} else if err != nil {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, err
	}
	var currentStatus string
	var currentRevision int64
	if err = tx.QueryRow(ctx, `SELECT status,revision FROM gateway_assistant_runs WHERE owner_key=$1 AND thread_id=$2 AND id=$3 FOR UPDATE`, run.OwnerKey, run.ThreadID, run.ID).Scan(&currentStatus, &currentRevision); errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrNotFound
	} else if err != nil {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, err
	}
	if currentStatus != expectedStatus || currentRevision != expectedRevision {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrConflict
	}
	var stepCount int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_assistant_run_steps WHERE owner_key=$1 AND thread_id=$2 AND run_id=$3`, run.OwnerKey, run.ThreadID, run.ID).Scan(&stepCount); err != nil {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, err
	}
	if stepCount >= stepQuota {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrQuotaExceeded
	}
	if message != nil {
		var messageCount int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_assistant_messages WHERE owner_key=$1 AND thread_id=$2`, run.OwnerKey, run.ThreadID).Scan(&messageCount); err != nil {
			return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, err
		}
		if messageCount >= messageQuota {
			return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrQuotaExceeded
		}
	}
	completedRun, err := scanAssistantRun(tx.QueryRow(ctx, `UPDATE gateway_assistant_runs SET status='completed',snapshot=$4::jsonb,revision=revision+1,updated_at=now() WHERE owner_key=$1 AND thread_id=$2 AND id=$3 AND status=$5 AND revision=$6 RETURNING id,thread_id,owner_key,status,snapshot,revision,retain_until,created_at,updated_at`, run.OwnerKey, run.ThreadID, run.ID, run.Snapshot, expectedStatus, expectedRevision))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrConflict
	}
	if err != nil {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, err
	}
	completedStep, err := scanAssistantRunStep(tx.QueryRow(ctx, `INSERT INTO gateway_assistant_run_steps (id,run_id,thread_id,owner_key,status,snapshot) VALUES ($1,$2,$3,$4,'completed',$5::jsonb) ON CONFLICT DO NOTHING RETURNING id,run_id,thread_id,owner_key,status,snapshot,revision,created_at,updated_at`, step.ID, step.RunID, step.ThreadID, step.OwnerKey, step.Snapshot))
	if errors.Is(err, pgx.ErrNoRows) {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrConflict
	}
	if err != nil {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, err
	}
	var completedMessage *assistantstate.MessageRecord
	if message != nil {
		created, createErr := scanAssistantMessage(tx.QueryRow(ctx, `INSERT INTO gateway_assistant_messages (id,thread_id,owner_key,snapshot) VALUES ($1,$2,$3,$4::jsonb) ON CONFLICT DO NOTHING RETURNING id,thread_id,owner_key,snapshot,revision,created_at,updated_at`, message.ID, message.ThreadID, message.OwnerKey, message.Snapshot))
		if errors.Is(createErr, pgx.ErrNoRows) {
			return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, assistantstate.ErrConflict
		}
		if createErr != nil {
			return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, createErr
		}
		completedMessage = &created
	}
	if err = tx.Commit(ctx); err != nil {
		return assistantstate.RunRecord{}, assistantstate.RunStepRecord{}, nil, err
	}
	return completedRun, completedStep, completedMessage, nil
}

func validAssistantRun(record assistantstate.RunRecord) bool {
	return validA2AStorageToken(record.ID, 128) && validA2AStorageToken(record.ThreadID, 128) && record.OwnerKey != "" && len(record.OwnerKey) <= 256 && assistantstate.ValidRunStatus(record.Status) && validAssistantJSONSnapshot(record.Snapshot, assistantstate.MaxRunSnapshotBytes) && record.RetainUntil.After(time.Now())
}

func validAssistantRunStep(record assistantstate.RunStepRecord) bool {
	return validA2AStorageToken(record.ID, 128) && validA2AStorageToken(record.RunID, 128) && validA2AStorageToken(record.ThreadID, 128) && record.OwnerKey != "" && len(record.OwnerKey) <= 256 && assistantstate.ValidRunStepStatus(record.Status) && validAssistantJSONSnapshot(record.Snapshot, assistantstate.MaxRunStepSnapshotBytes)
}

func validAssistantRunPage(owner, threadID string, options assistantstate.RunPageOptions) bool {
	return owner != "" && validA2AStorageToken(threadID, 128) && options.Limit >= 1 && options.Limit <= 100 &&
		(options.Order == "asc" || options.Order == "desc") && !(options.After != "" && options.Before != "") &&
		(options.After == "" || validA2AStorageToken(options.After, 128)) && (options.Before == "" || validA2AStorageToken(options.Before, 128))
}

func assistantRunPageDirection(options assistantstate.RunPageOptions) (string, string) {
	comparison, order := "<", "DESC"
	if options.Order == "asc" {
		comparison, order = ">", "ASC"
	}
	if options.Before != "" {
		if comparison == "<" {
			comparison = ">"
		} else {
			comparison = "<"
		}
	}
	return comparison, order
}

func assistantRunCursor(ctx context.Context, s *PostgresStore, owner, threadID, runID string, options assistantstate.RunPageOptions) (*time.Time, string, error) {
	boundary := options.After
	if boundary == "" {
		boundary = options.Before
	}
	if boundary == "" {
		return nil, "", nil
	}
	var created time.Time
	var id string
	var err error
	if runID == "" {
		err = s.pool.QueryRow(ctx, `SELECT created_at,id FROM gateway_assistant_runs WHERE owner_key=$1 AND thread_id=$2 AND id=$3`, owner, threadID, boundary).Scan(&created, &id)
	} else {
		err = s.pool.QueryRow(ctx, `SELECT created_at,id FROM gateway_assistant_run_steps WHERE owner_key=$1 AND thread_id=$2 AND run_id=$3 AND id=$4`, owner, threadID, runID, boundary).Scan(&created, &id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", assistantstate.ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return &created, id, nil
}

func assistantRunPage(values []assistantstate.RunRecord, limit int) ([]assistantstate.RunRecord, string, error) {
	next := ""
	if len(values) > limit {
		next = values[limit-1].ID
		values = values[:limit]
	}
	return values, next, nil
}

func scanAssistantRun(scanner assistantScanner) (assistantstate.RunRecord, error) {
	var value assistantstate.RunRecord
	err := scanner.Scan(&value.ID, &value.ThreadID, &value.OwnerKey, &value.Status, &value.Snapshot, &value.Revision, &value.RetainUntil, &value.CreatedAt, &value.UpdatedAt)
	value.Snapshot = append([]byte(nil), value.Snapshot...)
	return value, err
}

func scanAssistantRunStep(scanner assistantScanner) (assistantstate.RunStepRecord, error) {
	var value assistantstate.RunStepRecord
	err := scanner.Scan(&value.ID, &value.RunID, &value.ThreadID, &value.OwnerKey, &value.Status, &value.Snapshot, &value.Revision, &value.CreatedAt, &value.UpdatedAt)
	value.Snapshot = append([]byte(nil), value.Snapshot...)
	return value, err
}

func (s *PostgresStore) assistantRunConflictOrNotFound(ctx context.Context, owner, threadID, id string) error {
	if _, err := s.GetRun(ctx, owner, threadID, id); err != nil {
		return err
	}
	return assistantstate.ErrConflict
}

func (s *PostgresStore) assistantRunStepConflictOrNotFound(ctx context.Context, owner, threadID, runID, id string) error {
	if _, err := s.GetRunStep(ctx, owner, threadID, runID, id); err != nil {
		return err
	}
	return assistantstate.ErrConflict
}
