package modules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type ManagementAuditEvent struct {
	ID                int64          `json:"id"`
	OccurredAt        time.Time      `json:"occurred_at"`
	RequestID         string         `json:"request_id"`
	ActorID           string         `json:"actor_id"`
	ActorCredentialID string         `json:"actor_credential_id"`
	Action            string         `json:"action"`
	TargetType        string         `json:"target_type"`
	TargetID          string         `json:"target_id,omitempty"`
	Outcome           string         `json:"outcome"`
	Details           map[string]any `json:"details,omitempty"`
}

type AuditFilter struct {
	BeforeID int64
	Limit    int
	ActorID  string
	Action   string
}

type AuditStore interface {
	Append(context.Context, ManagementAuditEvent) (ManagementAuditEvent, error)
	List(context.Context, AuditFilter) ([]ManagementAuditEvent, error)
	Ready(context.Context) error
	Close()
}

var ErrInvalidAuditEvent = errors.New("invalid audit event")

type PostgresAuditStore struct{ pool *pgxpool.Pool }

func NewPostgresAuditStore(dsn string) (*PostgresAuditStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("audit postgres dsn is required")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, errors.New("invalid audit postgres configuration")
	}
	return &PostgresAuditStore{pool: pool}, nil
}

func (s *PostgresAuditStore) Ready(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("audit store is unavailable")
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('public.management_audit_events') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		return errors.New("management audit migration is not applied")
	}
	return nil
}
func (s *PostgresAuditStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *PostgresAuditStore) Append(ctx context.Context, event ManagementAuditEvent) (ManagementAuditEvent, error) {
	if err := validateAuditEvent(event); err != nil {
		return ManagementAuditEvent{}, err
	}
	details, err := json.Marshal(event.Details)
	if err != nil || len(details) > 16<<10 {
		return ManagementAuditEvent{}, errors.Join(ErrInvalidAuditEvent, errors.New("invalid audit details"))
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO management_audit_events(request_id,actor_id,actor_credential_id,action,target_type,target_id,outcome,details) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,occurred_at`, event.RequestID, event.ActorID, event.ActorCredentialID, event.Action, event.TargetType, event.TargetID, event.Outcome, details).Scan(&event.ID, &event.OccurredAt)
	return event, err
}

func (s *PostgresAuditStore) List(ctx context.Context, filter AuditFilter) ([]ManagementAuditEvent, error) {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}
	rows, err := s.pool.Query(ctx, `SELECT id,occurred_at,request_id,actor_id,actor_credential_id,action,target_type,target_id,outcome,details FROM management_audit_events WHERE ($1::bigint=0 OR id<$1) AND ($2='' OR actor_id=$2) AND ($3='' OR action=$3) ORDER BY id DESC LIMIT $4`, filter.BeforeID, filter.ActorID, filter.Action, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ManagementAuditEvent, 0)
	for rows.Next() {
		var event ManagementAuditEvent
		var details []byte
		if err := rows.Scan(&event.ID, &event.OccurredAt, &event.RequestID, &event.ActorID, &event.ActorCredentialID, &event.Action, &event.TargetType, &event.TargetID, &event.Outcome, &details); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(details, &event.Details); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func validateAuditEvent(event ManagementAuditEvent) error {
	if strings.TrimSpace(event.RequestID) == "" || strings.TrimSpace(event.ActorID) == "" || strings.TrimSpace(event.ActorCredentialID) == "" || strings.TrimSpace(event.Action) == "" || strings.TrimSpace(event.TargetType) == "" {
		return errors.Join(ErrInvalidAuditEvent, errors.New("audit identity and action are required"))
	}
	if len(event.RequestID) > 128 || len(event.ActorID) > 256 || len(event.ActorCredentialID) > 256 || len(event.Action) > 128 || len(event.TargetType) > 128 || len(event.TargetID) > 256 {
		return errors.Join(ErrInvalidAuditEvent, errors.New("audit event field is too long"))
	}
	if event.Outcome != "attempted" && event.Outcome != "succeeded" && event.Outcome != "failed" {
		return errors.Join(ErrInvalidAuditEvent, errors.New("invalid audit outcome"))
	}
	return nil
}
