package videostate

import (
	"context"
	"errors"
	"time"

	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

var (
	ErrNotFound      = errors.New("video job not found")
	ErrConflict      = errors.New("video job already exists")
	ErrQuotaExceeded = errors.New("video job quota exceeded")
	ErrUnavailable   = errors.New("video storage is unavailable")
	ErrInvalid       = errors.New("invalid video storage request")
)

type Record struct {
	OwnerKey  string
	Binding   provider.VideoBinding
	Video     openai.Video
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Store interface {
	CreateVideoRecord(context.Context, Record, int) (Record, error)
	GetVideoRecord(context.Context, string, string) (Record, error)
	ListVideoRecords(context.Context, string, int, string) ([]Record, string, error)
	UpdateVideoRecord(context.Context, string, openai.Video) (Record, error)
	DeleteVideoRecord(context.Context, string, string) error
}
