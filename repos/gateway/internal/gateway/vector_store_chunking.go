package gateway

import (
	"net/http"

	"ai-gateway-gateway/internal/vectorstate"
)

type vectorStoreChunkingStrategy struct {
	Type   string                     `json:"type"`
	Static *vectorStoreStaticChunking `json:"static,omitempty"`
}

type vectorStoreStaticChunking struct {
	MaxChunkSizeTokens int `json:"max_chunk_size_tokens"`
	ChunkOverlapTokens int `json:"chunk_overlap_tokens"`
}

func validateVectorStoreChunkingStrategy(w http.ResponseWriter, strategy *vectorStoreChunkingStrategy) bool {
	if strategy == nil {
		return true
	}
	switch strategy.Type {
	case "auto":
		if strategy.Static != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "auto chunking_strategy must not include static settings")
			return false
		}
		return true
	case "static":
		if strategy.Static == nil || strategy.Static.MaxChunkSizeTokens < 100 || strategy.Static.MaxChunkSizeTokens > 4096 || strategy.Static.ChunkOverlapTokens < 0 || strategy.Static.ChunkOverlapTokens > strategy.Static.MaxChunkSizeTokens/2 {
			writeError(w, http.StatusBadRequest, "invalid_request", "static chunking_strategy requires max_chunk_size_tokens from 100 to 4096 and overlap no greater than half")
			return false
		}
		return true
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "chunking_strategy type must be auto or static")
		return false
	}
}

func normalizedVectorStoreChunkingStrategy(strategy *vectorStoreChunkingStrategy) vectorstate.ChunkingStrategy {
	if strategy == nil || strategy.Type == "auto" {
		return vectorstate.AutoChunkingStrategy()
	}
	return vectorstate.ChunkingStrategy{Type: "static", MaxChunkSizeTokens: strategy.Static.MaxChunkSizeTokens, ChunkOverlapTokens: strategy.Static.ChunkOverlapTokens}
}

func publicVectorStoreChunkingStrategy(strategy vectorstate.ChunkingStrategy) map[string]any {
	if strategy.Type == "static" {
		return map[string]any{"type": "static", "static": map[string]int{"max_chunk_size_tokens": strategy.MaxChunkSizeTokens, "chunk_overlap_tokens": strategy.ChunkOverlapTokens}}
	}
	return map[string]any{"type": "auto"}
}
