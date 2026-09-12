package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

const semanticCacheMaxTotalBytes = 64 << 20
const semanticCacheMaxEntries = 1024

type semanticCacheConfig struct {
	ttl        time.Duration
	threshold  float64
	maxEntries int
	maxBytes   int
	embedder   semanticEmbedder
}

type semanticEmbedder interface {
	embed(context.Context, string) ([]float64, error)
}

type openAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

type semanticEntry struct {
	vector    []float64
	payload   []byte
	expiresAt time.Time
}

type semanticResponseCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	threshold  float64
	maxEntries int
	maxBytes   int
	entries    map[string][]semanticEntry
	now        func() time.Time
	embedder   semanticEmbedder
}

func newSemanticResponseCache(cfg semanticCacheConfig) *semanticResponseCache {
	if cfg.ttl <= 0 || cfg.embedder == nil {
		return nil
	}
	if cfg.threshold <= 0 || cfg.threshold > 1 {
		cfg.threshold = 0.95
	}
	if cfg.maxEntries <= 0 {
		cfg.maxEntries = 100
	}
	if cfg.maxEntries > semanticCacheMaxEntries {
		cfg.maxEntries = semanticCacheMaxEntries
	}
	if cfg.maxBytes <= 0 {
		cfg.maxBytes = 1 << 20
	}
	return &semanticResponseCache{
		ttl: cfg.ttl, threshold: cfg.threshold, maxEntries: cfg.maxEntries,
		maxBytes: cfg.maxBytes, entries: map[string][]semanticEntry{}, now: time.Now, embedder: cfg.embedder,
	}
}

func newOpenAIEmbedder(baseURL, apiKey, model string) semanticEmbedder {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(model) == "" {
		return nil
	}
	return &openAIEmbedder{
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model,
		client: newProviderHTTPClient(15 * time.Second),
	}
}

func (e *openAIEmbedder) embed(ctx context.Context, text string) ([]float64, error) {
	payload, err := json.Marshal(openAICompatibleEmbeddingRequest{Model: e.model, Input: text, EncodingFormat: "float"})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, providerURL(e.baseURL, "embeddings"), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	response, err := e.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, responseStatusError("semantic-cache-embedder", response)
	}
	var result openai.EmbeddingResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Data) != 1 || len(result.Data[0].Embedding) == 0 {
		return nil, errors.New("semantic cache embedder returned no vector")
	}
	return result.Data[0].Embedding, nil
}

func (c *semanticResponseCache) lookup(scope string, vector []float64) ([]byte, bool) {
	if c == nil || scope == "" || len(vector) == 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	entries := c.entries[scope]
	active := entries[:0]
	bestScore := -1.0
	bestIndex := -1
	for _, entry := range entries {
		if !now.Before(entry.expiresAt) {
			continue
		}
		active = append(active, entry)
		score := cosineSimilarity(vector, entry.vector)
		if score > bestScore {
			bestScore = score
			bestIndex = len(active) - 1
		}
	}
	clear(entries[len(active):])
	c.entries[scope] = active
	if len(active) == 0 {
		delete(c.entries, scope)
	}
	if bestIndex < 0 || bestScore < c.threshold {
		return nil, false
	}
	return append([]byte(nil), active[bestIndex].payload...), true
}

func (c *semanticResponseCache) set(scope string, vector []float64, payload []byte) bool {
	if c == nil || scope == "" || len(vector) == 0 || len(payload) == 0 || len(payload) > c.maxBytes || semanticEntryBytes(scope, vector, payload) > semanticCacheMaxTotalBytes {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneAndEvictLocked(c.now(), semanticEntryBytes(scope, vector, payload))
	entries := c.entries[scope]
	entries = append(entries, semanticEntry{
		vector: append([]float64(nil), vector...), payload: append([]byte(nil), payload...), expiresAt: c.now().Add(c.ttl),
	})
	c.entries[scope] = entries
	return true
}

func semanticEntryBytes(scope string, vector []float64, payload []byte) int {
	return len(scope) + len(vector)*8 + len(payload)
}

func (c *semanticResponseCache) pruneAndEvictLocked(now time.Time, incomingBytes int) {
	for scope, entries := range c.entries {
		active := entries[:0]
		for _, entry := range entries {
			if now.Before(entry.expiresAt) {
				active = append(active, entry)
			}
		}
		clear(entries[len(active):])
		if len(active) == 0 {
			delete(c.entries, scope)
		} else {
			c.entries[scope] = active
		}
	}
	for {
		total, bytes := 0, 0
		oldestScope, oldestIndex := "", -1
		var oldestExpiry time.Time
		for scope, entries := range c.entries {
			for index, entry := range entries {
				total++
				bytes += semanticEntryBytes(scope, entry.vector, entry.payload)
				if oldestIndex < 0 || entry.expiresAt.Before(oldestExpiry) {
					oldestScope, oldestIndex, oldestExpiry = scope, index, entry.expiresAt
				}
			}
		}
		if oldestIndex < 0 || (total < c.maxEntries && bytes+incomingBytes <= semanticCacheMaxTotalBytes) {
			return
		}
		entries := c.entries[oldestScope]
		copy(entries[oldestIndex:], entries[oldestIndex+1:])
		entries[len(entries)-1] = semanticEntry{}
		entries = entries[:len(entries)-1]
		if len(entries) == 0 {
			delete(c.entries, oldestScope)
		} else {
			c.entries[oldestScope] = entries
		}
	}
}

func semanticRequest(req modules.RequestContext, endpoint Endpoint) (string, string, bool) {
	request := req.Request
	if count, message := openai.ChatPromptCacheBreakpoints(request.Messages); message != "" || count > 0 {
		return "", "", false
	}
	if request.RequireMatchedStop {
		return "", "", false
	}
	if request.Logprobs != nil && *request.Logprobs {
		return "", "", false
	}
	if request.WebSearchOptions != nil || request.WebFetchOptions != nil || request.GeminiCodeExecution || request.AnthropicCodeExecution || request.AnthropicContainerID != "" || len(request.AnthropicSkills) > 0 {
		return "", "", false
	}
	if len(request.BedrockRequestMetadata) > 0 {
		return "", "", false
	}
	if request.BedrockGuardrailConfig != nil {
		return "", "", false
	}
	if len(request.GeminiSafetySettings) > 0 {
		return "", "", false
	}
	if len(request.BedrockAdditionalModelRequestFields) > 0 {
		return "", "", false
	}
	if openai.ChatRequestsAudio(request) {
		return "", "", false
	}
	if openai.HasChatAudioInput(request) {
		return "", "", false
	}
	if openai.HasChatFileInput(request) {
		return "", "", false
	}
	if openai.HasChatVideoInput(request) {
		return "", "", false
	}
	if req.CredentialID == "" || len(request.Messages) == 0 || len(request.Tools) > 0 || request.ToolChoice != nil || len(request.Functions) > 0 || request.FunctionCall != nil || openai.ChatHasLegacyFunctionHistory(request) || request.ResponseFormat != nil {
		return "", "", false
	}
	parts := make([]string, 0, len(request.Messages))
	structure := make([]string, 0, len(request.Messages))
	userMessages := 0
	for _, message := range request.Messages {
		if len(message.ToolCalls) > 0 || message.ToolCallID != "" || message.Audio != nil || message.ReasoningContent != "" || len(message.Reasoning) > 0 || len(message.NativeContent) > 0 || !textOnlyContent(message.Content) {
			return "", "", false
		}
		text := openai.ContentText(message.Content)
		if strings.TrimSpace(text) == "" {
			return "", "", false
		}
		switch message.Role {
		case "user":
			userMessages++
		case "system", "developer":
		default:
			return "", "", false
		}
		structureItem := message.Role + "\x1f" + message.Name
		if message.Role != "user" {
			structureItem += "\x1f" + text
		}
		structure = append(structure, structureItem)
		parts = append(parts, message.Role+": "+text)
	}
	if userMessages != 1 {
		return "", "", false
	}
	settings := request
	settings.Messages = nil
	settings.Provider = ""
	if logicalModel := req.Metadata["provider.requested_model"]; logicalModel != "" {
		settings.Model = logicalModel
	}
	settingsJSON, err := json.Marshal(chatCacheKeyValue(settings))
	if err != nil {
		return "", "", false
	}
	scopeHash := sha256.Sum256([]byte(cacheIsolationScope(req) + "\x00" + endpoint.Name + "\x00" + string(settingsJSON) + "\x00" + strings.Join(structure, "\x1e")))
	return hex.EncodeToString(scopeHash[:]), strings.Join(parts, "\n"), true
}

func textOnlyContent(value any) bool {
	switch typed := value.(type) {
	case string:
		return true
	case []any:
		if len(typed) == 0 {
			return false
		}
		for _, item := range typed {
			if !textOnlyContent(item) {
				return false
			}
		}
		return true
	case map[string]any:
		kind, _ := typed["type"].(string)
		text, _ := typed["text"].(string)
		return (kind == "text" || kind == "input_text") && text != ""
	default:
		return false
	}
}

func cosineSimilarity(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return -1
	}
	dot, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for index := range left {
		dot += left[index] * right[index]
		leftNorm += left[index] * left[index]
		rightNorm += right[index] * right[index]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return -1
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}
