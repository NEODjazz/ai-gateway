package controlstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/batchstate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateBatch(ctx context.Context, batch batchstate.Batch, items []batchstate.Item, jobs []asyncstate.Job, ownerQuota int) (batchstate.Batch, error) {
	if s == nil || s.pool == nil {
		return batchstate.Batch{}, batchstate.ErrUnavailable
	}
	if !validBatch(batch) || len(items) != batch.Total || len(jobs) != batch.Total || ownerQuota < 1 || ownerQuota > 100000 {
		return batchstate.Batch{}, batchstate.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return batchstate.Batch{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 3))`, batch.OwnerKey); err != nil {
		return batchstate.Batch{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_batches WHERE owner_key=$1 AND status IN ('queued','in_progress','finalizing')`, batch.OwnerKey).Scan(&count); err != nil {
		return batchstate.Batch{}, err
	}
	if count >= ownerQuota {
		return batchstate.Batch{}, batchstate.ErrQuotaExceeded
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_batches
		(id,owner_key,input_file_id,endpoint,completion_window,output_expiry_seconds,status,metadata,identity,total,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT DO NOTHING`,
		batch.ID, batch.OwnerKey, batch.InputFileID, batch.Endpoint, batch.CompletionWindow, batch.OutputExpirySeconds, batch.Status, batch.Metadata, batch.Identity, batch.Total, batch.ExpiresAt)
	if err != nil {
		return batchstate.Batch{}, err
	}
	if command.RowsAffected() != 1 {
		return batchstate.Batch{}, batchstate.ErrConflict
	}
	for index := range items {
		item, job := items[index], jobs[index]
		if !validBatchItem(batch, item) || !asyncstate.Valid(job) || job.ResourceID != batch.ID+":"+stringOrdinal(item.Ordinal) || job.OwnerKey != batch.OwnerKey || job.ExecutionID != item.ExecutionID {
			return batchstate.Batch{}, batchstate.ErrInvalid
		}
	}
	const insertChunk = 500
	for start := 0; start < len(items); start += insertChunk {
		end := min(start+insertChunk, len(items))
		statements := &pgx.Batch{}
		for index := start; index < end; index++ {
			item, job := items[index], jobs[index]
			statements.Queue(`INSERT INTO gateway_batch_items (batch_id,owner_key,ordinal,custom_id,url,body,identity,state,execution_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, item.BatchID, item.OwnerKey, item.Ordinal, item.CustomID, item.URL, item.Body, item.Identity, item.State, item.ExecutionID)
			statements.Queue(`INSERT INTO gateway_async_jobs (kind,resource_id,owner_key,endpoint_id,execution_id,payload,available_at) VALUES ($1,$2,$3,$4,$5,$6,now())`, job.Kind, job.ResourceID, job.OwnerKey, job.EndpointID, job.ExecutionID, job.Payload)
		}
		results := tx.SendBatch(ctx, statements)
		for range 2 * (end - start) {
			if _, err = results.Exec(); err != nil {
				_ = results.Close()
				return batchstate.Batch{}, err
			}
		}
		if err = results.Close(); err != nil {
			return batchstate.Batch{}, err
		}
	}
	created, err := getBatch(ctx, tx, batch.OwnerKey, batch.ID)
	if err != nil {
		return batchstate.Batch{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return batchstate.Batch{}, err
	}
	return created, nil
}

func stringOrdinal(value int) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for value > 0 {
		pos--
		buf[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[pos:])
}

func validBatch(batch batchstate.Batch) bool {
	return validA2AStorageToken(batch.ID, 128) && batch.OwnerKey != "" && len(batch.OwnerKey) <= 256 && validA2AStorageToken(batch.InputFileID, 128) &&
		batch.CompletionWindow == "24h" && (batch.OutputExpirySeconds == 0 || batch.OutputExpirySeconds >= 3600 && batch.OutputExpirySeconds <= 2592000) && batch.Status == "queued" && batch.Total >= 1 && batch.Total <= 50000 && len(batch.Metadata) > 0 && json.Valid(batch.Metadata) && len(batch.Identity) > 0 && len(batch.Identity) <= batchstate.MaxIdentityBytes && json.Valid(batch.Identity) && batch.ExpiresAt.After(time.Now())
}

func validBatchItem(batch batchstate.Batch, item batchstate.Item) bool {
	return item.BatchID == batch.ID && item.OwnerKey == batch.OwnerKey && item.Ordinal >= 0 && item.Ordinal < batch.Total && len(item.CustomID) >= 1 && len(item.CustomID) <= 128 && item.URL == batch.Endpoint && len(item.Body) > 0 && len(item.Body) <= 4<<20 && json.Valid(item.Body) && len(item.Identity) > 0 && len(item.Identity) <= batchstate.MaxIdentityBytes && json.Valid(item.Identity) && item.State == "pending" && validA2AStorageToken(item.ExecutionID, 128)
}

func (s *PostgresStore) GetBatch(ctx context.Context, owner, id string) (batchstate.Batch, error) {
	if s == nil || s.pool == nil {
		return batchstate.Batch{}, batchstate.ErrUnavailable
	}
	if owner == "" || !validA2AStorageToken(id, 128) {
		return batchstate.Batch{}, batchstate.ErrInvalid
	}
	return getBatch(ctx, s.pool, owner, id)
}

type batchQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getBatch(ctx context.Context, q batchQuerier, owner, id string) (batchstate.Batch, error) {
	b, err := scanBatch(q.QueryRow(ctx, `SELECT id,owner_key,input_file_id,endpoint,completion_window,output_expiry_seconds,status,metadata,identity,total,completed,failed,output_file_id,error_file_id,created_at,in_progress_at,expires_at,finalizing_at,completed_at,failed_at,expired_at,cancelling_at,cancelled_at FROM gateway_batches WHERE owner_key=$1 AND id=$2`, owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return batchstate.Batch{}, batchstate.ErrNotFound
	}
	return b, err
}

type batchScanner interface{ Scan(...any) error }

func scanBatch(row batchScanner) (batchstate.Batch, error) {
	var b batchstate.Batch
	err := row.Scan(&b.ID, &b.OwnerKey, &b.InputFileID, &b.Endpoint, &b.CompletionWindow, &b.OutputExpirySeconds, &b.Status, &b.Metadata, &b.Identity, &b.Total, &b.Completed, &b.Failed, &b.OutputFileID, &b.ErrorFileID, &b.CreatedAt, &b.InProgressAt, &b.ExpiresAt, &b.FinalizingAt, &b.CompletedAt, &b.FailedAt, &b.ExpiredAt, &b.CancellingAt, &b.CancelledAt)
	return b, err
}

func (s *PostgresStore) ListBatches(ctx context.Context, owner string, limit int, after string) ([]batchstate.Batch, string, error) {
	if s == nil || s.pool == nil {
		return nil, "", batchstate.ErrUnavailable
	}
	if owner == "" || limit < 1 || limit > 100 || (after != "" && !validA2AStorageToken(after, 128)) {
		return nil, "", batchstate.ErrInvalid
	}
	var cursorTime *time.Time
	var cursorID string
	if after != "" {
		var created time.Time
		err := s.pool.QueryRow(ctx, `SELECT created_at,id FROM gateway_batches WHERE owner_key=$1 AND id=$2`, owner, after).Scan(&created, &cursorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", batchstate.ErrNotFound
		}
		if err != nil {
			return nil, "", err
		}
		cursorTime = &created
	}
	rows, err := s.pool.Query(ctx, `SELECT id,owner_key,input_file_id,endpoint,completion_window,output_expiry_seconds,status,metadata,identity,total,completed,failed,output_file_id,error_file_id,created_at,in_progress_at,expires_at,finalizing_at,completed_at,failed_at,expired_at,cancelling_at,cancelled_at FROM gateway_batches WHERE owner_key=$1 AND ($2::timestamptz IS NULL OR (created_at,id)<($2::timestamptz,$3)) ORDER BY created_at DESC,id DESC LIMIT $4`, owner, cursorTime, cursorID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	result := make([]batchstate.Batch, 0, limit+1)
	for rows.Next() {
		b, e := scanBatch(rows)
		if e != nil {
			return nil, "", e
		}
		result = append(result, b)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(result) > limit {
		next = result[limit-1].ID
		result = result[:limit]
	}
	return result, next, nil
}

func (s *PostgresStore) GetBatchItem(ctx context.Context, owner, batchID string, ordinal int) (batchstate.Item, error) {
	if s == nil || s.pool == nil {
		return batchstate.Item{}, batchstate.ErrUnavailable
	}
	var item batchstate.Item
	err := scanBatchItem(s.pool.QueryRow(ctx, `SELECT batch_id,owner_key,ordinal,custom_id,url,body,identity,state,execution_id,result FROM gateway_batch_items WHERE owner_key=$1 AND batch_id=$2 AND ordinal=$3`, owner, batchID, ordinal), &item)
	if errors.Is(err, pgx.ErrNoRows) {
		return batchstate.Item{}, batchstate.ErrNotFound
	}
	return item, err
}

func scanBatchItem(row batchScanner, item *batchstate.Item) error {
	var result []byte
	err := row.Scan(&item.BatchID, &item.OwnerKey, &item.Ordinal, &item.CustomID, &item.URL, &item.Body, &item.Identity, &item.State, &item.ExecutionID, &result)
	item.Result = result
	return err
}

func (s *PostgresStore) StartBatch(ctx context.Context, owner, id string) (batchstate.Batch, error) {
	if s == nil || s.pool == nil {
		return batchstate.Batch{}, batchstate.ErrUnavailable
	}
	b, err := scanBatch(s.pool.QueryRow(ctx, `UPDATE gateway_batches SET status='in_progress',in_progress_at=COALESCE(in_progress_at,now()) WHERE owner_key=$1 AND id=$2 AND status IN ('queued','in_progress') RETURNING id,owner_key,input_file_id,endpoint,completion_window,output_expiry_seconds,status,metadata,identity,total,completed,failed,output_file_id,error_file_id,created_at,in_progress_at,expires_at,finalizing_at,completed_at,failed_at,expired_at,cancelling_at,cancelled_at`, owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.GetBatch(ctx, owner, id); getErr != nil {
			return batchstate.Batch{}, getErr
		}
		return batchstate.Batch{}, batchstate.ErrConflict
	}
	return b, err
}

func (s *PostgresStore) FinishBatchItem(ctx context.Context, item batchstate.Item, failed bool) (batchstate.Batch, error) {
	if s == nil || s.pool == nil {
		return batchstate.Batch{}, batchstate.ErrUnavailable
	}
	if len(item.Result) == 0 || !json.Valid(item.Result) {
		return batchstate.Batch{}, batchstate.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return batchstate.Batch{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	b, err := getBatch(ctx, tx, item.OwnerKey, item.BatchID)
	if err != nil {
		return batchstate.Batch{}, err
	}
	var existing batchstate.Item
	err = scanBatchItem(tx.QueryRow(ctx, `SELECT batch_id,owner_key,ordinal,custom_id,url,body,identity,state,execution_id,result FROM gateway_batch_items WHERE owner_key=$1 AND batch_id=$2 AND ordinal=$3 FOR UPDATE`, item.OwnerKey, item.BatchID, item.Ordinal), &existing)
	if err != nil {
		return batchstate.Batch{}, err
	}
	if existing.ExecutionID != item.ExecutionID {
		return batchstate.Batch{}, batchstate.ErrConflict
	}
	if existing.State != "pending" {
		if string(existing.Result) != string(item.Result) {
			return batchstate.Batch{}, batchstate.ErrConflict
		}
		return b, nil
	}
	state := "completed"
	completedDelta, failedDelta := 1, 0
	if failed {
		state = "failed"
		completedDelta, failedDelta = 0, 1
	}
	if b.Status == "cancelled" {
		return b, nil
	}
	if b.Status != "queued" && b.Status != "in_progress" {
		return batchstate.Batch{}, batchstate.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE gateway_batch_items SET state=$4,result=$5 WHERE owner_key=$1 AND batch_id=$2 AND ordinal=$3 AND state='pending'`, item.OwnerKey, item.BatchID, item.Ordinal, state, item.Result); err != nil {
		return batchstate.Batch{}, err
	}
	status := `CASE WHEN completed+$3+failed+$4=total THEN 'finalizing' ELSE 'in_progress' END`
	query := `UPDATE gateway_batches SET completed=completed+$3,failed=failed+$4,status=` + status + `,in_progress_at=COALESCE(in_progress_at,now()),finalizing_at=CASE WHEN completed+$3+failed+$4=total THEN now() ELSE finalizing_at END WHERE owner_key=$1 AND id=$2 AND status IN ('queued','in_progress') RETURNING id,owner_key,input_file_id,endpoint,completion_window,output_expiry_seconds,status,metadata,identity,total,completed,failed,output_file_id,error_file_id,created_at,in_progress_at,expires_at,finalizing_at,completed_at,failed_at,expired_at,cancelling_at,cancelled_at`
	b, err = scanBatch(tx.QueryRow(ctx, query, item.OwnerKey, item.BatchID, completedDelta, failedDelta))
	if errors.Is(err, pgx.ErrNoRows) {
		return batchstate.Batch{}, batchstate.ErrConflict
	}
	if err != nil {
		return batchstate.Batch{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return batchstate.Batch{}, err
	}
	return b, nil
}

func (s *PostgresStore) ListBatchResults(ctx context.Context, owner, batchID string, failed bool) ([]batchstate.Item, error) {
	state := "completed"
	if failed {
		state = "failed"
	}
	rows, err := s.pool.Query(ctx, `SELECT batch_id,owner_key,ordinal,custom_id,url,body,identity,state,execution_id,result FROM gateway_batch_items WHERE owner_key=$1 AND batch_id=$2 AND state=$3 ORDER BY ordinal`, owner, batchID, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []batchstate.Item
	for rows.Next() {
		var i batchstate.Item
		if err := rows.Scan(&i.BatchID, &i.OwnerKey, &i.Ordinal, &i.CustomID, &i.URL, &i.Body, &i.Identity, &i.State, &i.ExecutionID, &i.Result); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CancelBatch(ctx context.Context, owner, id string) (batchstate.Batch, error) {
	b, err := scanBatch(s.pool.QueryRow(ctx, `UPDATE gateway_batches SET status='cancelled',cancelling_at=COALESCE(cancelling_at,now()),cancelled_at=now() WHERE owner_key=$1 AND id=$2 AND status IN ('queued','in_progress') RETURNING id,owner_key,input_file_id,endpoint,completion_window,output_expiry_seconds,status,metadata,identity,total,completed,failed,output_file_id,error_file_id,created_at,in_progress_at,expires_at,finalizing_at,completed_at,failed_at,expired_at,cancelling_at,cancelled_at`, owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := s.GetBatch(ctx, owner, id)
		if getErr != nil {
			return batchstate.Batch{}, getErr
		}
		return existing, batchstate.ErrConflict
	}
	return b, err
}

func (s *PostgresStore) ExpireBatch(ctx context.Context, owner, id string) (batchstate.Batch, error) {
	if s == nil || s.pool == nil {
		return batchstate.Batch{}, batchstate.ErrUnavailable
	}
	b, err := scanBatch(s.pool.QueryRow(ctx, `UPDATE gateway_batches SET status='expired',expired_at=now() WHERE owner_key=$1 AND id=$2 AND expires_at<=now() AND status IN ('queued','in_progress','finalizing') RETURNING id,owner_key,input_file_id,endpoint,completion_window,output_expiry_seconds,status,metadata,identity,total,completed,failed,output_file_id,error_file_id,created_at,in_progress_at,expires_at,finalizing_at,completed_at,failed_at,expired_at,cancelling_at,cancelled_at`, owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := s.GetBatch(ctx, owner, id)
		if getErr != nil {
			return batchstate.Batch{}, getErr
		}
		return existing, batchstate.ErrConflict
	}
	return b, err
}

func (s *PostgresStore) FinalizeBatch(ctx context.Context, owner, id, outputID, errorID string) (batchstate.Batch, error) {
	b, err := scanBatch(s.pool.QueryRow(ctx, `UPDATE gateway_batches SET status='completed',output_file_id=$3,error_file_id=$4,completed_at=now() WHERE owner_key=$1 AND id=$2 AND status='finalizing' RETURNING id,owner_key,input_file_id,endpoint,completion_window,output_expiry_seconds,status,metadata,identity,total,completed,failed,output_file_id,error_file_id,created_at,in_progress_at,expires_at,finalizing_at,completed_at,failed_at,expired_at,cancelling_at,cancelled_at`, owner, id, outputID, errorID))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := s.GetBatch(ctx, owner, id)
		if getErr != nil {
			return batchstate.Batch{}, getErr
		}
		if existing.Status == "completed" && existing.OutputFileID == outputID && existing.ErrorFileID == errorID {
			return existing, nil
		}
		return batchstate.Batch{}, batchstate.ErrConflict
	}
	return b, err
}
