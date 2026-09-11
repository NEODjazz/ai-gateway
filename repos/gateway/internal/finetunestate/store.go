package finetunestate

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

var (
	ErrNotFound      = errors.New("fine-tuning job not found")
	ErrConflict      = errors.New("fine-tuning job already exists")
	ErrQuotaExceeded = errors.New("fine-tuning job quota exceeded")
	ErrUnavailable   = errors.New("fine-tuning storage is unavailable")
	ErrInvalid       = errors.New("invalid fine-tuning storage request")
)

type Record struct {
	OwnerKey  string
	Binding   provider.FineTuningBinding
	Job       openai.FineTuningJob
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Store interface {
	CreateFineTuningRecord(context.Context, Record, int) (Record, error)
	GetFineTuningRecord(context.Context, string, string) (Record, error)
	ListFineTuningRecords(context.Context, string, int, string) ([]Record, string, error)
	UpdateFineTuningRecord(context.Context, string, openai.FineTuningJob) (Record, error)
	FindFineTuningRecordByModel(context.Context, string, string) (Record, error)
}
