package mcpstate

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const (
	StatePending   = "pending"
	StateCompleted = "completed"
)

var (
	ErrConflict    = errors.New("MCP tool call idempotency key conflicts with another request")
	ErrUnavailable = errors.New("MCP tool call store is unavailable")
)

type Record struct {
	ScopeKey       string
	IdempotencyKey string
	RequestHash    string
	ExecutionID    string
	State          string
	HTTPStatus     int
	Response       json.RawMessage
}

type Store interface {
	Claim(context.Context, Record) (Record, bool, error)
	Complete(context.Context, string, string, string, int, json.RawMessage) error
	Release(context.Context, string, string, string) error
}

type memoryEntry struct {
	record    Record
	updatedAt time.Time
}

type MemoryStore struct {
	mu       sync.Mutex
	entries  map[string]memoryEntry
	capacity int
	ttl      time.Duration
	now      func() time.Time
}

func NewMemoryStore(capacity int, ttl time.Duration) *MemoryStore {
	if capacity < 1 {
		capacity = 1
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &MemoryStore{entries: make(map[string]memoryEntry), capacity: capacity, ttl: ttl, now: time.Now}
}

func (s *MemoryStore) Claim(_ context.Context, claim Record) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.removeExpired(now)
	key := recordKey(claim.ScopeKey, claim.IdempotencyKey)
	if entry, ok := s.entries[key]; ok {
		if entry.record.RequestHash != claim.RequestHash {
			return Record{}, false, ErrConflict
		}
		return cloneRecord(entry.record), false, nil
	}
	if len(s.entries) >= s.capacity {
		return Record{}, false, ErrUnavailable
	}
	claim.State = StatePending
	claim.HTTPStatus = 0
	claim.Response = nil
	s.entries[key] = memoryEntry{record: cloneRecord(claim), updatedAt: now}
	return cloneRecord(claim), true, nil
}

func (s *MemoryStore) Complete(_ context.Context, scope, key, executionID string, status int, response json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[recordKey(scope, key)]
	if !ok || entry.record.ExecutionID != executionID || entry.record.State != StatePending {
		return ErrConflict
	}
	entry.record.State = StateCompleted
	entry.record.HTTPStatus = status
	entry.record.Response = append(json.RawMessage(nil), response...)
	entry.updatedAt = s.now()
	s.entries[recordKey(scope, key)] = entry
	return nil
}

func (s *MemoryStore) Release(_ context.Context, scope, key, executionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[recordKey(scope, key)]
	if !ok {
		return nil
	}
	if entry.record.ExecutionID != executionID || entry.record.State != StatePending {
		return ErrConflict
	}
	delete(s.entries, recordKey(scope, key))
	return nil
}

func (s *MemoryStore) removeExpired(now time.Time) {
	for key, entry := range s.entries {
		if now.Sub(entry.updatedAt) >= s.ttl {
			delete(s.entries, key)
		}
	}
}

func recordKey(scope, key string) string { return scope + "\x00" + key }

func cloneRecord(record Record) Record {
	record.Response = append(json.RawMessage(nil), record.Response...)
	return record
}
