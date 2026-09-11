package batchstate

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
)

var (
	ErrNotFound      = errors.New("batch not found")
	ErrConflict      = errors.New("batch state conflict")
	ErrQuotaExceeded = errors.New("batch quota exceeded")
	ErrUnavailable   = errors.New("batch storage is unavailable")
	ErrInvalid       = errors.New("invalid batch storage request")
)

const MaxIdentityBytes = 16 << 10

type Batch struct {
	ID               string
	OwnerKey         string
	InputFileID      string
	Endpoint         string
	CompletionWindow string
	Status           string
	Metadata         []byte
	Identity         []byte
	Total            int
	Completed        int
	Failed           int
	OutputFileID     string
	ErrorFileID      string
	CreatedAt        time.Time
	InProgressAt     *time.Time
	ExpiresAt        time.Time
	FinalizingAt     *time.Time
	CompletedAt      *time.Time
	FailedAt         *time.Time
	ExpiredAt        *time.Time
	CancellingAt     *time.Time
	CancelledAt      *time.Time
}

type Item struct {
	BatchID     string
	OwnerKey    string
	Ordinal     int
	CustomID    string
	URL         string
	Body        []byte
	Identity    []byte
	State       string
	ExecutionID string
	Result      []byte
}

type Store interface {
	CreateBatch(context.Context, Batch, []Item, []asyncstate.Job, int) (Batch, error)
	GetBatch(context.Context, string, string) (Batch, error)
	ListBatches(context.Context, string, int, string) ([]Batch, string, error)
	GetBatchItem(context.Context, string, string, int) (Item, error)
	StartBatch(context.Context, string, string) (Batch, error)
	FinishBatchItem(context.Context, Item, bool) (Batch, error)
	ListBatchResults(context.Context, string, string, bool) ([]Item, error)
	CancelBatch(context.Context, string, string) (Batch, error)
	ExpireBatch(context.Context, string, string) (Batch, error)
	FinalizeBatch(context.Context, string, string, string, string) (Batch, error)
}
