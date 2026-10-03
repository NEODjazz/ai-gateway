package controlstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ai-gateway-gateway/internal/realtimestate"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateRealtimeTicket(ctx context.Context, ticket realtimestate.Ticket) error {
	if s == nil || s.pool == nil {
		return realtimestate.ErrUnavailable
	}
	if !realtimestate.Valid(ticket, time.Now()) {
		return realtimestate.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin realtime ticket admission: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize global and per-owner admission across replicas.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gateway-realtime-browser-tickets', 12))`); err != nil {
		return fmt.Errorf("lock realtime ticket admission: %w", err)
	}
	if !realtimestate.Valid(ticket, time.Now()) {
		return realtimestate.ErrInvalid
	}
	if _, err = tx.Exec(ctx, `DELETE FROM gateway_realtime_browser_tickets WHERE expires_at <= clock_timestamp()`); err != nil {
		return fmt.Errorf("expire realtime tickets: %w", err)
	}
	var duplicate bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_realtime_browser_tickets WHERE ticket_hash=$1)`, ticket.Hash).Scan(&duplicate); err != nil {
		return fmt.Errorf("check realtime ticket identity: %w", err)
	}
	if duplicate {
		return realtimestate.ErrConflict
	}
	var total, owned int
	if err = tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE owner_key=$1) FROM gateway_realtime_browser_tickets`, ticket.OwnerKey).Scan(&total, &owned); err != nil {
		return fmt.Errorf("count realtime tickets: %w", err)
	}
	if total >= realtimestate.MaxTickets || owned >= realtimestate.MaxOwnerTickets {
		return realtimestate.ErrCapacity
	}
	command, err := tx.Exec(ctx, `INSERT INTO gateway_realtime_browser_tickets (ticket_hash, owner_key, model, origin, payload, expires_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (ticket_hash) DO NOTHING`, ticket.Hash, ticket.OwnerKey, ticket.Model, ticket.Origin, ticket.Payload, ticket.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create realtime ticket: %w", err)
	}
	if command.RowsAffected() != 1 {
		return realtimestate.ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit realtime ticket admission: %w", err)
	}
	return nil
}

func (s *PostgresStore) ConsumeRealtimeTicket(ctx context.Context, hash, model, origin string) (realtimestate.Ticket, error) {
	if s == nil || s.pool == nil {
		return realtimestate.Ticket{}, realtimestate.ErrUnavailable
	}
	var ticket realtimestate.Ticket
	err := s.pool.QueryRow(ctx, `DELETE FROM gateway_realtime_browser_tickets WHERE ticket_hash=$1 AND model=$2 AND origin=$3 AND expires_at > clock_timestamp() RETURNING ticket_hash, owner_key, model, origin, payload, expires_at`, hash, model, origin).Scan(&ticket.Hash, &ticket.OwnerKey, &ticket.Model, &ticket.Origin, &ticket.Payload, &ticket.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return realtimestate.Ticket{}, realtimestate.ErrNotFound
	}
	if err != nil {
		return realtimestate.Ticket{}, fmt.Errorf("consume realtime ticket: %w", err)
	}
	return ticket, nil
}
