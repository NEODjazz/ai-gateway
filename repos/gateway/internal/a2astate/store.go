package a2astate

import (
	"context"
	"errors"
	"time"
)

const MaxPayloadBytes = 2 << 20

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
	GetA2ATask(context.Context, string, string, string) (Task, error)
	ListA2ATasks(context.Context, string, string, ListOptions) ([]Task, string, int, error)
}
