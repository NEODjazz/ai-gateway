package containerstate

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

var (
	ErrNotFound      = errors.New("container not found")
	ErrConflict      = errors.New("container already exists")
	ErrQuotaExceeded = errors.New("container quota exceeded")
	ErrUnavailable   = errors.New("container storage is unavailable")
	ErrInvalid       = errors.New("invalid container storage request")
)

type Record struct {
	OwnerKey  string
	Binding   provider.ContainerBinding
	Container openai.Container
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Store interface {
	CreateContainerRecord(context.Context, Record, int) (Record, error)
	GetContainerRecord(context.Context, string, string) (Record, error)
	ListContainerRecords(context.Context, string, int, string) ([]Record, string, error)
	UpdateContainerRecord(context.Context, string, openai.Container) (Record, error)
	DeleteContainerRecord(context.Context, string, string) error
}
