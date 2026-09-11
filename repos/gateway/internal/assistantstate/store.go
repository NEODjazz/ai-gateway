package assistantstate

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("assistant not found")
var ErrQuotaExceeded = errors.New("assistant quota exceeded")
var ErrConflict = errors.New("assistant state conflict")
var ErrUnavailable = errors.New("assistant storage is unavailable")
var ErrInvalid = errors.New("invalid assistant record")

const MaxSnapshotBytes = 1 << 20
const MaxMessageSnapshotBytes = 2 << 20

type Record struct {
	ID        string
	OwnerKey  string
	Snapshot  []byte
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Store interface {
	CreateAssistant(context.Context, Record, int) (Record, error)
	ListAssistants(context.Context, string, int, string) ([]Record, string, error)
	GetAssistant(context.Context, string, string) (Record, error)
	UpdateAssistant(context.Context, string, string, []byte, int64) (Record, error)
	DeleteAssistant(context.Context, string, string) error
}

type ThreadRecord struct {
	ID        string
	OwnerKey  string
	Snapshot  []byte
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type MessageRecord struct {
	ID        string
	ThreadID  string
	OwnerKey  string
	Snapshot  []byte
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ThreadStore interface {
	CreateThread(context.Context, ThreadRecord, int) (ThreadRecord, error)
	ListThreads(context.Context, string, int, string) ([]ThreadRecord, string, error)
	GetThread(context.Context, string, string) (ThreadRecord, error)
	UpdateThread(context.Context, string, string, []byte, int64) (ThreadRecord, error)
	DeleteThread(context.Context, string, string) error
	CreateThreadMessage(context.Context, MessageRecord, int) (MessageRecord, error)
	ListThreadMessages(context.Context, string, string, int, string) ([]MessageRecord, string, error)
	GetThreadMessage(context.Context, string, string, string) (MessageRecord, error)
	UpdateThreadMessage(context.Context, string, string, string, []byte, int64) (MessageRecord, error)
	DeleteThreadMessage(context.Context, string, string, string) error
}
