package gateway

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/redisstore"
)

type RateLimit struct {
	Requests int
	Tokens   int
}

type RateLimitStore interface {
	Allow(ctx context.Context, key string, limit RateLimit, tokens int) (allowed bool, retryAfter time.Duration, err error)
}

type rateWindow struct {
	started  time.Time
	requests int
	tokens   int
}

// MemoryRateLimitStore is the single-instance implementation of the shared
// contract. A Redis implementation can provide the same atomic Allow operation.
type MemoryRateLimitStore struct {
	mu      sync.Mutex
	windows map[string]rateWindow
	now     func() time.Time
}

type RedisRateLimitStore struct {
	store *redisstore.Store
}

func NewRedisRateLimitStore(store *redisstore.Store) *RedisRateLimitStore {
	return &RedisRateLimitStore{store: store}
}

func (s *RedisRateLimitStore) Allow(ctx context.Context, key string, limit RateLimit, tokens int) (bool, time.Duration, error) {
	return s.store.Allow(ctx, key, limit.Requests, limit.Tokens, tokens, time.Minute)
}

func NewMemoryRateLimitStore() *MemoryRateLimitStore {
	return &MemoryRateLimitStore{windows: map[string]rateWindow{}, now: time.Now}
}

func (s *MemoryRateLimitStore) Allow(_ context.Context, key string, limit RateLimit, tokens int) (bool, time.Duration, error) {
	if s == nil || key == "" || (limit.Requests <= 0 && limit.Tokens <= 0) {
		return true, 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	window := s.windows[key]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	if (limit.Requests > 0 && window.requests+1 > limit.Requests) || (limit.Tokens > 0 && window.tokens+tokens > limit.Tokens) {
		return false, time.Minute - now.Sub(window.started), nil
	}
	window.requests++
	window.tokens += tokens
	s.windows[key] = window
	return true, 0, nil
}

func modelAllowed(model string, grants []string) bool {
	if len(grants) == 0 {
		return true
	}
	for _, grant := range grants {
		grant = strings.TrimSpace(grant)
		if grant == "*" || grant == model || (strings.HasSuffix(grant, "*") && strings.HasPrefix(model, strings.TrimSuffix(grant, "*"))) {
			return true
		}
	}
	return false
}

func filterModels(models []openai.Model, grants []string) []openai.Model {
	if len(grants) == 0 {
		return models
	}
	filtered := make([]openai.Model, 0, len(models))
	for _, model := range models {
		if modelAllowed(model.ID, grants) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func estimateChatTokens(request openai.ChatCompletionRequest) int {
	characters := 0
	for _, message := range request.Messages {
		characters += len([]rune(openai.ContentText(message.Content)))
	}
	tokens := (characters + 3) / 4
	if request.MaxTokens != nil {
		tokens += *request.MaxTokens
	}
	if tokens < 1 {
		return 1
	}
	return tokens
}

func estimateResponseTokens(request openai.ResponseRequest) int {
	tokens := (len([]rune(responseInputText(request.Input))) + len([]rune(request.Instructions)) + 3) / 4
	if request.MaxOutputTokens != nil {
		tokens += *request.MaxOutputTokens
	} else if request.MaxTokens != nil {
		tokens += *request.MaxTokens
	}
	if tokens < 1 {
		return 1
	}
	return tokens
}

func estimateEmbeddingTokens(request openai.EmbeddingRequest) int {
	tokens := (len([]rune(openai.EmbeddingInputText(request.Input))) + 3) / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}

func (h Handler) authorizeAccess(w http.ResponseWriter, ctx context.Context, req modules.RequestContext, model string, tokens int) bool {
	if !modelAllowed(model, req.AllowedModels) {
		writeError(w, 403, "model_not_allowed", "credential is not allowed to use model "+strconv.Quote(model))
		return false
	}
	key := req.CredentialID
	if req.TeamID != "" {
		key = "team:" + req.TeamID + ":credential:" + req.CredentialID
	}
	allowed, retryAfter, err := h.rateLimits.Allow(ctx, key, RateLimit{Requests: req.RateLimitRPM, Tokens: req.RateLimitTPM}, tokens)
	if err != nil {
		writeError(w, 503, "rate_limit_unavailable", err.Error())
		return false
	}
	if !allowed {
		seconds := int(retryAfter.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, 429, "rate_limit_exceeded", "credential rate limit exceeded")
		return false
	}
	return true
}
