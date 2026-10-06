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
var ErrFileBatchNotFound = errors.New("vector store file batch not found")

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
	VectorStoreID    string
	FileID           string
	OwnerKey         string
	Status           string
	Bytes            int64
	Attributes       map[string]any
	ChunkingStrategy ChunkingStrategy
	CreatedAt        time.Time
}

type ChunkingStrategy struct {
	Type               string
	MaxChunkSizeTokens int
	ChunkOverlapTokens int
}

func (strategy ChunkingStrategy) Valid() bool {
	if strategy.Type == "auto" {
		return strategy.MaxChunkSizeTokens == 0 && strategy.ChunkOverlapTokens == 0
	}
	return strategy.Type == "static" && strategy.MaxChunkSizeTokens >= 100 && strategy.MaxChunkSizeTokens <= 4096 && strategy.ChunkOverlapTokens >= 0 && strategy.ChunkOverlapTokens <= strategy.MaxChunkSizeTokens/2
}

func AutoChunkingStrategy() ChunkingStrategy { return ChunkingStrategy{Type: "auto"} }

type FileBatch struct {
	ID            string
	VectorStoreID string
	OwnerKey      string
	Status        string
	Total         int
	Completed     int
	Failed        int
	Cancelled     int
	CreatedAt     time.Time
}

type FileBatchEntry struct {
	FileID           string
	Attributes       map[string]any
	ChunkingStrategy ChunkingStrategy
}

type Update struct {
	Name         *string
	Metadata     *map[string]string
	ExpiresAfter *int
}

type FileListOptions struct {
	Limit  int
	After  string
	Before string
	Order  string
	Status string
}

func (options FileListOptions) Valid() bool {
	validStatus := options.Status == "" || options.Status == "in_progress" || options.Status == "completed" || options.Status == "failed" || options.Status == "cancelled"
	return options.Limit >= 1 && options.Limit <= 100 && (options.After == "" || options.Before == "") && (options.Order == "asc" || options.Order == "desc") && validStatus
}

type Store interface {
	CreateVectorStore(context.Context, VectorStore, int) (VectorStore, error)
	ListVectorStores(context.Context, string, int, string) ([]VectorStore, string, error)
	GetVectorStore(context.Context, string, string) (VectorStore, error)
	UpdateVectorStore(context.Context, string, string, Update) (VectorStore, error)
	DeleteVectorStore(context.Context, string, string) error
	AttachVectorStoreFile(context.Context, string, string, string, map[string]any, ChunkingStrategy, int, int64) (File, error)
	ListVectorStoreFiles(context.Context, string, string, FileListOptions) ([]File, string, error)
	GetVectorStoreFile(context.Context, string, string, string) (File, error)
	UpdateVectorStoreFile(context.Context, string, string, string, map[string]any) (File, error)
	DeleteVectorStoreFile(context.Context, string, string, string) error
}

type FileBatchStore interface {
	CreateVectorStoreFileBatch(context.Context, FileBatch, []FileBatchEntry, int, int64) (FileBatch, error)
	GetVectorStoreFileBatch(context.Context, string, string, string) (FileBatch, error)
	ListVectorStoreFileBatchFiles(context.Context, string, string, string, FileListOptions) ([]File, string, error)
}
