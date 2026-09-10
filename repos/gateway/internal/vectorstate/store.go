package vectorstate

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("vector store not found")
var ErrQuotaExceeded = errors.New("vector store quota exceeded")
var ErrConflict = errors.New("vector store already exists")
var ErrUnavailable = errors.New("vector store storage is unavailable")
var ErrInvalid = errors.New("invalid vector store request")

type VectorStore struct {
	ID           string
	OwnerKey     string
	Name         string
	Status       string
	Metadata     map[string]string
	ExpiresAfter int
	CreatedAt    time.Time
	LastActiveAt time.Time
	ExpiresAt    *time.Time
}

type Update struct {
	Name         *string
	Metadata     *map[string]string
	ExpiresAfter *int
}

type Store interface {
	CreateVectorStore(context.Context, VectorStore, int) (VectorStore, error)
	ListVectorStores(context.Context, string, int, string) ([]VectorStore, string, error)
	GetVectorStore(context.Context, string, string) (VectorStore, error)
	UpdateVectorStore(context.Context, string, string, Update) (VectorStore, error)
	DeleteVectorStore(context.Context, string, string) error
}
