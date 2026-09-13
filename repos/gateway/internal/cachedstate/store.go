package cachedstate

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

var (
	ErrNotFound      = errors.New("cached content not found")
	ErrConflict      = errors.New("cached content already exists")
	ErrQuotaExceeded = errors.New("cached content quota exceeded")
	ErrUnavailable   = errors.New("cached content storage is unavailable")
	ErrInvalid       = errors.New("invalid cached content storage request")
)

type Record struct {
	OwnerKey  string
	Binding   provider.CachedContentBinding
	Content   openai.GeminiCachedContent
	ExpiresAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Store interface {
	CreateCachedContentRecord(context.Context, Record, int) (Record, error)
	GetCachedContentRecord(context.Context, string, string) (Record, error)
	ListCachedContentRecords(context.Context, string, int, string) ([]Record, string, error)
	UpdateCachedContentRecord(context.Context, string, openai.GeminiCachedContent) (Record, error)
	DeleteCachedContentRecord(context.Context, string, string) error
}
