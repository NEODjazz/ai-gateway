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
var ErrFileNotFound = errors.New("vector store file not found")
var ErrFileQuotaExceeded = errors.New("vector store file quota exceeded")
var ErrByteQuotaExceeded = errors.New("vector store byte quota exceeded")

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
	UsageBytes   int64
	FileCount    int
}

type File struct {
	VectorStoreID string
	FileID        string
	OwnerKey      string
	Status        string
	Bytes         int64
	CreatedAt     time.Time
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
	AttachVectorStoreFile(context.Context, string, string, string, int, int64) (File, error)
	ListVectorStoreFiles(context.Context, string, string, int, string) ([]File, string, error)
	GetVectorStoreFile(context.Context, string, string, string) (File, error)
	DeleteVectorStoreFile(context.Context, string, string, string) error
}
