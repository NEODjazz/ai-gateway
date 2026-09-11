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
