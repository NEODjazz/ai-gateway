package a2astate

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
)

const MaxPayloadBytes = 2 << 20
const MaxPushConfigPayloadBytes = 16 << 10

var ErrNotFound = errors.New("A2A task not found")
var ErrConflict = errors.New("A2A task already exists")
var ErrQuotaExceeded = errors.New("A2A task quota exceeded")
var ErrUnavailable = errors.New("A2A task storage is unavailable")
var ErrInvalid = errors.New("invalid A2A task storage request")

type Task struct {
	ID        string
	OwnerKey  string
	AgentID   string
	Model     string
	ContextID string
	State     string
	Payload   []byte
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
}

type PushConfig struct {
	ID        string
	TaskID    string
	OwnerKey  string
	AgentID   string
	Payload   []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ListOptions struct {
	Model        string
	Limit        int
	After        string
	ContextID    string
	State        string
	UpdatedAfter *time.Time
}

type Store interface {
	CreateA2ATask(context.Context, Task, int, time.Duration) (Task, error)
	UpdateA2ATask(context.Context, Task, time.Time, time.Duration) (Task, error)
	GetA2ATask(context.Context, string, string, string) (Task, error)
	ListA2ATasks(context.Context, string, string, ListOptions) ([]Task, string, int, error)
}

// AtomicOutboxStore persists a task transition and its durable follow-up job
// in one transaction. Callers use this for externally observable task events
// that must not be lost between independent database writes.
type AtomicOutboxStore interface {
	Store
	CreateA2ATaskWithJob(context.Context, Task, int, time.Duration, asyncstate.Job) (Task, error)
	UpdateA2ATaskWithJob(context.Context, Task, time.Time, time.Duration, asyncstate.Job) (Task, error)
	CreateA2ATaskWithPushConfig(context.Context, Task, PushConfig, int, int, time.Duration, asyncstate.Job) (Task, error)
	UpdateA2ATaskWithPushConfig(context.Context, Task, PushConfig, time.Time, int, time.Duration, asyncstate.Job) (Task, error)
	CreateA2APushConfig(context.Context, PushConfig, int, asyncstate.Job) (PushConfig, error)
	GetA2APushConfig(context.Context, string, string, string, string) (PushConfig, error)
	ListA2APushConfigs(context.Context, string, string, string, int, string) ([]PushConfig, string, int, error)
	DeleteA2APushConfig(context.Context, string, string, string, string) error
}
