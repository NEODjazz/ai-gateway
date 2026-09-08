package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DurableEventRepository interface {
	Ready(ctx context.Context) error
	Reserve(ctx context.Context, event BillingEvent) (created bool, err error)
	Enqueue(ctx context.Context, event BillingEvent) (created bool, err error)
}

func (r *PostgresOutboxRepository) Ready(ctx context.Context) error {
	if err := r.pool.Ping(ctx); err != nil {
		return err
	}
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT to_regclass('public.billing_outbox') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("billing_outbox migration is not applied")
	}
	return nil
}

type PostgresOutboxRepository struct {
	pool   *pgxpool.Pool
	writer UsageEventWriter
	poll   time.Duration
	cancel context.CancelFunc
}

func NewPostgresOutboxRepository(dsn string, writer UsageEventWriter, poll time.Duration) (*PostgresOutboxRepository, error) {
	if dsn == "" {
		return nil, errors.New("POSTGRES_DSN is required when durable billing outbox is enabled")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, errors.New("configure postgres outbox: invalid POSTGRES_DSN")
	}
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	workerContext, cancel := context.WithCancel(context.Background())
	repository := &PostgresOutboxRepository{pool: pool, writer: writer, poll: poll, cancel: cancel}
	go repository.run(workerContext)
	return repository, nil
}

func (r *PostgresOutboxRepository) Close() {
	r.cancel()
	r.pool.Close()
}

func (r *PostgresOutboxRepository) Reserve(ctx context.Context, event BillingEvent) (bool, error) {
	if event.EventID == "" {
		return false, errors.New("billing event_id is required for durable reservation")
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO billing_event_ledger (event_id, request_id, phase)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO NOTHING`, event.EventID, event.RequestID, event.Phase)
	if err != nil {
		return false, fmt.Errorf("reserve billing event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *PostgresOutboxRepository) Enqueue(ctx context.Context, event BillingEvent) (bool, error) {
	if event.EventID == "" {
		return false, errors.New("billing event_id is required for durable enqueue")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	created, err := r.enqueueTx(ctx, tx, event)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return created, nil
}

func (r *PostgresOutboxRepository) enqueueTx(ctx context.Context, tx pgx.Tx, event BillingEvent) (bool, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO billing_event_ledger (event_id, request_id, phase)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO NOTHING`, event.EventID, event.RequestID, event.Phase)
	if err != nil {
		return false, fmt.Errorf("claim billing event: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO billing_outbox (event_id, payload)
		VALUES ($1, $2::jsonb)`, event.EventID, payload); err != nil {
		return false, fmt.Errorf("enqueue billing event: %w", err)
	}
	return true, nil
}

func (r *PostgresOutboxRepository) run(ctx context.Context) {
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		deliveryContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		for range 20 {
			delivered, _ := r.DeliverOnce(deliveryContext)
			if !delivered || deliveryContext.Err() != nil {
				break
			}
		}
		cancel()
	}
}

func (r *PostgresOutboxRepository) DeliverOnce(ctx context.Context) (bool, error) {
	var id int64
	var payload []byte
	var attempts int
	err := r.pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id
			FROM billing_outbox
			WHERE delivered_at IS NULL
			  AND available_at <= now()
			  AND (locked_at IS NULL OR locked_at < now() - interval '5 minutes')
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE billing_outbox AS outbox
		SET locked_at = now(), attempts = outbox.attempts + 1
		FROM candidate
		WHERE outbox.id = candidate.id
		RETURNING outbox.id, outbox.payload, outbox.attempts`).Scan(&id, &payload, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var event BillingEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		_ = r.deliveryFailed(ctx, id, attempts, "decode event: "+err.Error())
		return true, err
	}
	if err := r.writer.WriteUsageEvent(ctx, event); err != nil {
		_ = r.deliveryFailed(ctx, id, attempts, err.Error())
		return true, err
	}
	_, err = r.pool.Exec(ctx, `
		UPDATE billing_outbox
		SET delivered_at = now(), locked_at = NULL, last_error = NULL
		WHERE id = $1`, id)
	return true, err
}

func (r *PostgresOutboxRepository) deliveryFailed(ctx context.Context, id int64, attempts int, message string) error {
	backoffSeconds := attempts * attempts
	if backoffSeconds > 60 {
		backoffSeconds = 60
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE billing_outbox
		SET locked_at = NULL,
			available_at = now() + ($2 * interval '1 second'),
			last_error = $3
		WHERE id = $1`, id, backoffSeconds, message)
	return err
}
