package gateway

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/vectorstate"
)

const (
	maxVectorSearchFiles      = 20
	maxVectorSearchBytes      = 1 << 20
	maxVectorSearchChunks     = 100
	maxVectorSearchChunkRunes = 2000
	maxVectorSearchQueryRunes = 4096
)

type vectorStoreSearchRequest struct {
	Query         string          `json:"query"`
	Model         string          `json:"model"`
	Provider      string          `json:"provider,omitempty"`
	MaxNumResults int             `json:"max_num_results,omitempty"`
	Filters       json.RawMessage `json:"filters,omitempty"`
}

type vectorSearchChunk struct {
	fileID     string
	filename   string
	attributes map[string]any
	text       string
	index      int
}

type vectorSearchResult struct {
	FileID     string             `json:"file_id"`
	Filename   string             `json:"filename"`
	Score      float64            `json:"score"`
	Attributes map[string]any     `json:"attributes"`
	Content    []vectorSearchText `json:"content"`
	chunkIndex int
}

type vectorSearchText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (h Handler) SearchVectorStore(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	storeID := r.PathValue("id")
	if !validFileToken(storeID, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "vector store ID is invalid")
		return
	}
	var input vectorStoreSearchRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Query) != input.Query || input.Query == "" || !utf8.ValidString(input.Query) || utf8.RuneCountInString(input.Query) > maxVectorSearchQueryRunes {
		writeError(w, http.StatusBadRequest, "invalid_request", "query must contain 1 to 4096 valid UTF-8 characters without surrounding whitespace")
		return
	}
	if strings.TrimSpace(input.Model) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	if input.MaxNumResults == 0 {
		input.MaxNumResults = 10
	}
	if input.MaxNumResults < 1 || input.MaxNumResults > 50 {
		writeError(w, http.StatusBadRequest, "invalid_request", "max_num_results must be between 1 and 50")
		return
	}
	filter, err := parseVectorSearchFilter(input.Filters)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if h.vectorStores == nil || h.files == nil {
		writeError(w, http.StatusServiceUnavailable, "vector_store_search_unavailable", "vector store search is unavailable")
		return
	}
	reqCtx := modules.RequestContext{
		APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: executionID(w), SessionID: sessionID(r),
		Metadata: map[string]string{"gateway.api_type": "vector_store_search"},
	}
	if err := h.pipeline.RunAuthentication(r.Context(), &reqCtx); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
		} else {
			writeError(w, http.StatusBadGateway, "module_failed", "authentication failed")
		}
		return
	}
	reqCtx.APIKey = ""
	if !h.prepareAccessGroups(w, &reqCtx) || !h.authorizeModel(w, reqCtx, input.Model) {
		return
	}
	owner := fileOwnerKey(reqCtx)
	if _, err := h.vectorStores.GetVectorStore(r.Context(), owner, storeID); err != nil {
		writeVectorStoreError(w, err)
		return
	}
	chunks, err := h.loadVectorSearchChunks(r, owner, storeID, filter)
	if err != nil {
		writeVectorSearchError(w, err)
		return
	}
	texts := make([]string, 1, len(chunks)+1)
	texts[0] = input.Query
	for _, chunk := range chunks {
		texts = append(texts, chunk.text)
	}
	embeddingRequest := openai.EmbeddingRequest{Provider: input.Provider, Model: input.Model, Input: texts, EncodingFormat: "float"}
	reqCtx.EmbeddingRequest = &embeddingRequest
	reqCtx.Request = openai.ChatCompletionRequest{Provider: input.Provider, Model: input.Model}
	if err := h.pipeline.RunAfterAuthentication(r.Context(), &reqCtx); err != nil {
		writeError(w, http.StatusBadGateway, "module_failed", err.Error())
		return
	}
	if reqCtx.EmbeddingRequest == nil {
		writeError(w, http.StatusBadGateway, "module_failed", "module removed inference request")
		return
	}
	if !h.authorizeRateLimit(w, r.Context(), reqCtx, estimateEmbeddingTokens(*reqCtx.EmbeddingRequest)) || !h.prepareModelFallbacks(w, r.Context(), &reqCtx, input.Model) {
		return
	}
	embedder, ok := h.provider.(provider.EmbeddingProvider)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "embeddings are not supported by the configured provider")
		return
	}
	response, err := embedder.Embeddings(r.Context(), reqCtx)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	results, ok := rankVectorSearchResults(response, chunks, input.MaxNumResults)
	if !ok {
		writeError(w, http.StatusBadGateway, "provider_failed", "embedding response is invalid")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "vector_store.search_results.page", "search_query": []string{input.Query},
		"data": results, "has_more": false, "next_page": nil,
	})
}

func (h Handler) loadVectorSearchChunks(r *http.Request, owner, storeID string, filter vectorSearchFilter) ([]vectorSearchChunk, error) {
	files, next, err := h.vectorStores.ListVectorStoreFiles(r.Context(), owner, storeID, maxVectorSearchFiles, "")
	if err != nil {
		return nil, err
	}
	if next != "" {
		return nil, errVectorSearchTooLarge
	}
	chunks := make([]vectorSearchChunk, 0, len(files))
	totalBytes := int64(0)
	for _, attached := range files {
		if filter != nil && !filter.matches(attached.Attributes) {
			continue
		}
		file, getErr := h.files.Get(r.Context(), owner, attached.FileID, true)
		if getErr != nil {
			return nil, getErr
		}
		contentBytes := int64(len(file.Content))
		if file.Purpose != "assistants" || !supportedVectorSearchContentType(file.ContentType) || !utf8.Valid(file.Content) || strings.IndexByte(string(file.Content), 0) >= 0 || contentBytes > maxVectorSearchBytes || totalBytes > maxVectorSearchBytes-contentBytes {
			return nil, errVectorSearchUnsupportedFile
		}
		totalBytes += contentBytes
		for _, text := range splitVectorSearchText(string(file.Content)) {
			if len(chunks) >= maxVectorSearchChunks {
				return nil, errVectorSearchTooLarge
			}
			chunks = append(chunks, vectorSearchChunk{fileID: file.ID, filename: file.Filename, attributes: normalizedVectorStoreAttributes(attached.Attributes), text: text, index: len(chunks)})
		}
	}
	if len(chunks) == 0 {
		return nil, errVectorSearchEmpty
	}
	return chunks, nil
}

var (
	errVectorSearchTooLarge        = errors.New("vector store exceeds synchronous search limits")
	errVectorSearchUnsupportedFile = errors.New("vector store contains an unsupported file")
	errVectorSearchEmpty           = errors.New("vector store contains no searchable text")
)

func supportedVectorSearchContentType(value string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	return mediaType == "text/plain" || mediaType == "text/markdown" || mediaType == "text/csv" || mediaType == "application/json"
}

func splitVectorSearchText(value string) []string {
	runes := []rune(value)
	chunks := make([]string, 0, (len(runes)+maxVectorSearchChunkRunes-1)/maxVectorSearchChunkRunes)
	for len(runes) > 0 {
		count := len(runes)
		if count > maxVectorSearchChunkRunes {
			count = maxVectorSearchChunkRunes
		}
		text := strings.TrimSpace(string(runes[:count]))
		if text != "" {
			chunks = append(chunks, text)
		}
		runes = runes[count:]
	}
	return chunks
}

func rankVectorSearchResults(response openai.EmbeddingResponse, chunks []vectorSearchChunk, limit int) ([]vectorSearchResult, bool) {
	if len(response.Data) != len(chunks)+1 {
		return nil, false
	}
	vectors := make([][]float64, len(response.Data))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(vectors) || len(item.Embedding) == 0 || vectors[item.Index] != nil {
			return nil, false
		}
		vectors[item.Index] = item.Embedding
	}
	query := vectors[0]
	results := make([]vectorSearchResult, len(chunks))
	for index, chunk := range chunks {
		score, valid := cosineSimilarity(query, vectors[index+1])
		if !valid {
			return nil, false
		}
		results[index] = vectorSearchResult{FileID: chunk.fileID, Filename: chunk.filename, Score: score, Attributes: chunk.attributes, Content: []vectorSearchText{{Type: "text", Text: chunk.text}}, chunkIndex: chunk.index}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > limit {
		results = results[:limit]
	}
	return results, true
}

func cosineSimilarity(left, right []float64) (float64, bool) {
	if len(left) == 0 || len(left) != len(right) {
		return 0, false
	}
	dot, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for index := range left {
		if math.IsNaN(left[index]) || math.IsInf(left[index], 0) || math.IsNaN(right[index]) || math.IsInf(right[index], 0) {
			return 0, false
		}
		dot += left[index] * right[index]
		leftNorm += left[index] * left[index]
		rightNorm += right[index] * right[index]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0, false
	}
	score := dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
	if math.IsNaN(score) || math.IsInf(score, 0) {
		return 0, false
	}
	return math.Max(0, math.Min(1, score)), true
}

func writeVectorSearchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vectorstate.ErrNotFound):
		writeVectorStoreError(w, err)
	case errors.Is(err, filestate.ErrNotFound), errors.Is(err, errVectorSearchUnsupportedFile):
		writeError(w, http.StatusUnprocessableEntity, "vector_store_file_not_searchable", "vector store contains a file that cannot be searched")
	case errors.Is(err, errVectorSearchTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "vector_store_search_too_large", "vector store exceeds synchronous search limits")
	case errors.Is(err, errVectorSearchEmpty):
		writeError(w, http.StatusUnprocessableEntity, "vector_store_not_searchable", "vector store contains no searchable text")
	default:
		writeError(w, http.StatusServiceUnavailable, "vector_store_search_unavailable", "vector store search is unavailable")
	}
}
