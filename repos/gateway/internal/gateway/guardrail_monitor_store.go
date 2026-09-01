package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/redisstore"
)

const guardrailEventNamespace = "guardrail-monitor-events-v1"

type GuardrailEventStore interface {
	Append(context.Context, GuardrailEvent, int) error
	Recent(context.Context, int) ([]GuardrailEvent, error)
}

type RedisGuardrailEventStore struct {
	store *redisstore.Store
	ttl   time.Duration
}

func NewRedisGuardrailEventStore(store *redisstore.Store, ttl time.Duration) GuardrailEventStore {
	if store == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return &RedisGuardrailEventStore{store: store, ttl: ttl}
}

func (s *RedisGuardrailEventStore) Append(ctx context.Context, event GuardrailEvent, capacity int) error {
	if s == nil || s.store == nil {
		return errors.New("guardrail event store is not configured")
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return s.store.PushBounded(ctx, guardrailEventNamespace, payload, capacity, s.ttl)
}

func (s *RedisGuardrailEventStore) Recent(ctx context.Context, limit int) ([]GuardrailEvent, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("guardrail event store is not configured")
	}
	values, err := s.store.ListBounded(ctx, guardrailEventNamespace, limit)
	if err != nil {
		return nil, err
	}
	events := make([]GuardrailEvent, 0, len(values))
	cutoff := time.Now().UTC().Add(-s.ttl)
	for _, value := range values {
		var event GuardrailEvent
		if err := json.Unmarshal(value, &event); err != nil {
			return nil, errors.New("invalid guardrail event in shared store")
		}
		normalized, ok := normalizeGuardrailEvent(event)
		if !ok {
			return nil, errors.New("unsafe guardrail event in shared store")
		}
		if normalized.OccurredAt.Before(cutoff) {
			continue
		}
		events = append(events, normalized)
	}
	return events, nil
}
