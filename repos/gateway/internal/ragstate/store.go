package ragstate

import (
	"context"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/vectorstate"
)

type IngestRequest struct {
	OwnerKey         string
	File             *filestate.File
	FileID           string
	VectorStore      *vectorstate.VectorStore
	VectorStoreID    string
	Attributes       map[string]any
	FileOwnerQuota   int64
	VectorStoreQuota int
	VectorStoreFiles int
	VectorStoreBytes int64
}

type IngestResult struct {
	File        filestate.File
	VectorStore vectorstate.VectorStore
	Attachment  vectorstate.File
}

type Store interface {
	IngestRAG(context.Context, IngestRequest) (IngestResult, error)
}
