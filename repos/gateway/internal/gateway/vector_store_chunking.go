package gateway

import "net/http"

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
		writeError(w, http.StatusUnprocessableEntity, "vector_store_chunking_unsupported", "static token-based chunking is unavailable for this vector store runtime")
		return false
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "chunking_strategy type must be auto or static")
		return false
	}
}

func publicVectorStoreChunkingStrategy() map[string]string {
	return map[string]string{"type": "auto"}
}
