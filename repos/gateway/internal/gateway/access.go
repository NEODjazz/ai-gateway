package gateway

import (
	"context"
	"net/http"
	"net/url"
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

func toolAllowed(tool string, grants []string) bool {
	return modelAllowed(tool, grants)
}

func chatToolIdentifiers(tools []openai.Tool) ([]string, bool) {
	identifiers := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" || strings.TrimSpace(tool.Function.Name) == "" {
			return nil, false
		}
		identifiers = append(identifiers, tool.Function.Name)
	}
	return identifiers, true
}

func mcpToolIdentifier(tool openai.ResponseTool) (string, bool) {
	label := strings.TrimSpace(tool.ServerLabel)
	parsed, err := url.Parse(tool.ServerURL)
	if label == "" || err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = strings.TrimSuffix(parsed.RawPath, "/")
	return "mcp:" + label + "@" + parsed.String(), true
}

func responseToolIdentifiers(tools []openai.ResponseTool) ([]string, bool) {
	identifiers := make([]string, 0, len(tools))
	for _, tool := range tools {
		var identifier string
		switch tool.Type {
		case "function":
			identifier = tool.Name
		case "mcp":
			var valid bool
			identifier, valid = mcpToolIdentifier(tool)
			if !valid {
				return nil, false
			}
		default:
			return nil, false
		}
		if strings.TrimSpace(identifier) == "" {
			return nil, false
		}
		identifiers = append(identifiers, identifier)
	}
	return identifiers, true
}

func (h Handler) authorizeTools(w http.ResponseWriter, req modules.RequestContext, identifiers []string, valid bool) bool {
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid tool definition")
		return false
	}
	for _, identifier := range identifiers {
		if !h.toolAllowed(identifier, req.AllowedTools) {
			writeError(w, http.StatusForbidden, "tool_not_allowed", "credential is not allowed to use tool "+strconv.Quote(identifier))
			return false
		}
		if req.AccessGroupsEvaluated && (len(req.AccessGroupTools) == 0 || !h.toolAllowed(identifier, req.AccessGroupTools)) {
			writeError(w, http.StatusForbidden, "access_group_tool_not_allowed", "assigned access groups do not allow the requested tool")
			return false
		}
	}
	return true
}

func (h Handler) toolAllowed(identifier string, grants []string) bool {
	if toolAllowed(identifier, grants) {
		return true
	}
	if h.mcp == nil {
		return false
	}
	for _, grant := range grants {
		if strings.HasPrefix(grant, "toolset:") && h.mcp.ToolsetAllows(strings.TrimPrefix(grant, "toolset:"), identifier) {
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

func estimateRerankTokens(request openai.RerankRequest) int {
	text, ok := openai.RerankDocumentText(request)
	if !ok {
		return 0
	}
	return len(strings.Fields(text))
}

func (h Handler) authorizeAccess(w http.ResponseWriter, ctx context.Context, req modules.RequestContext, model string, tokens int) bool {
	if !modelAllowed(model, req.AllowedModels) {
		writeError(w, 403, "model_not_allowed", "credential is not allowed to use model "+strconv.Quote(model))
		return false
	}
	if req.AccessGroupsEvaluated && (len(req.AccessGroupModels) == 0 || !modelAllowed(model, req.AccessGroupModels)) {
		writeError(w, http.StatusForbidden, "access_group_model_not_allowed", "assigned access groups do not allow the requested model")
		return false
	}
	if h.access != nil {
		if allowed, _ := h.access.TagModelAllowed(req.Tags, model); !allowed {
			writeError(w, http.StatusForbidden, "tag_model_not_allowed", "credential tags do not allow the requested model")
			return false
		}
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

func (h Handler) prepareAccessGroups(w http.ResponseWriter, req *modules.RequestContext) bool {
	if len(req.AccessGroupIDs) == 0 {
		return true
	}
	if h.access == nil {
		writeError(w, http.StatusServiceUnavailable, "access_policy_unavailable", "access-group policy is unavailable")
		return false
	}
	policy, err := h.access.ResolveAccessGroups(req.AccessGroupIDs)
	if err != nil {
		writeError(w, http.StatusForbidden, "access_group_not_allowed", "an assigned access group is missing or disabled")
		return false
	}
	req.AccessGroupModels = append([]string(nil), policy.AllowedModels...)
	req.AccessGroupTools = append([]string(nil), policy.AllowedTools...)
	req.AccessGroupsEvaluated = true
	return true
}
