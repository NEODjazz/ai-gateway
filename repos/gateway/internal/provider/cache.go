package provider

import (
	"ai-gateway-gateway/internal/bounded"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

const memoryCacheMaxEntries = 1024
const memoryCacheMaxBytes = 64 << 20

type exactCache struct {
	ttl      time.Duration
	entries  *bounded.Cache[[]byte]
	now      func() time.Time
	maxBytes int
}

type ExactCacheStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

type responseCache interface {
	get(ctx context.Context, key string) ([]byte, bool, error)
	set(ctx context.Context, key string, value []byte) error
}

type distributedExactCache struct {
	store    ExactCacheStore
	ttl      time.Duration
	maxBytes int
}

func newResponseCache(ttl time.Duration, maxBytes int, store ExactCacheStore) responseCache {
	if ttl <= 0 {
		return nil
	}
	if !interfaceIsNil(store) {
		return distributedExactCache{store: store, ttl: ttl, maxBytes: maxBytes}
	}
	return newExactCacheWithLimit(ttl, maxBytes)
}

func newExactCache(ttl time.Duration) *exactCache {
	return newExactCacheWithLimit(ttl, 1_048_576)
}

func newExactCacheWithLimit(ttl time.Duration, maxBytes int) *exactCache {
	if ttl <= 0 {
		return nil
	}
	return &exactCache{ttl: ttl, entries: bounded.New[[]byte](ttl, memoryCacheMaxEntries, memoryCacheMaxBytes), now: time.Now, maxBytes: maxBytes}
}

func (c *exactCache) get(_ context.Context, key string) ([]byte, bool, error) {
	if c == nil || key == "" {
		return nil, false, nil
	}
	payload, found := c.entries.Get(key, c.now())
	return append([]byte(nil), payload...), found, nil
}

func (c *exactCache) set(_ context.Context, key string, value []byte) error {
	if c == nil || key == "" || len(value) > memoryCacheMaxBytes || (c.maxBytes > 0 && len(value) > c.maxBytes) {
		return nil
	}
	c.entries.Set(key, append([]byte(nil), value...), len(value), c.now())
	return nil
}

func (c distributedExactCache) get(ctx context.Context, key string) ([]byte, bool, error) {
	return c.store.Get(ctx, key)
}

func (c distributedExactCache) set(ctx context.Context, key string, value []byte) error {
	if c.maxBytes > 0 && len(value) > c.maxBytes {
		return nil
	}
	return c.store.Set(ctx, key, value, c.ttl)
}

func providerCacheKey(kind string, req modules.RequestContext) string {
	if kind == "chat" && (req.Request.WebSearchOptions != nil || req.Request.WebFetchOptions != nil || openai.ChatRequestsAudio(req.Request)) {
		return ""
	}
	tenant := cacheIsolationScope(req)
	if tenant == "" {
		return ""
	}
	request := req.Request
	if logicalModel := req.Metadata["provider.requested_model"]; logicalModel != "" {
		request.Model = logicalModel
	}
	var value any = request
	if kind == "chat" && request.RequireMatchedStop {
		kind = "chat-matched-stop"
	}
	if kind == "chat" {
		value = chatCacheKeyValue(request)
	}
	if kind == "responses" && req.ResponseRequest != nil {
		responseRequest := *req.ResponseRequest
		if logicalModel := req.Metadata["provider.requested_model"]; logicalModel != "" {
			responseRequest.Model = logicalModel
		}
		// Response IDs refer to state owned by the selected upstream. A cached
		// response from another deployment cannot serve as its continuation.
		value = struct {
			Request                                           openai.ResponseRequest
			Endpoint, ProviderID, ProviderType, UpstreamModel string
		}{
			Request:       responseRequest,
			Endpoint:      req.Metadata["provider.endpoint.name"],
			ProviderID:    req.Metadata["provider.id"],
			ProviderType:  req.Metadata["provider.endpoint.type"],
			UpstreamModel: req.ResponseRequest.Model,
		}
	}
	body, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte("v3\x00"+kind+"\x00"+tenant+"\x00"), body...))
	return hex.EncodeToString(sum[:])
}

// chatCacheKeyValue includes provider-native request state hidden from the
// public Chat JSON shape. Every new internal field that changes provider output
// must be added here before it is enabled for cached execution.
func chatCacheKeyValue(request openai.ChatCompletionRequest) any {
	nativeContent := make([][]json.RawMessage, len(request.Messages))
	for index := range request.Messages {
		nativeContent[index] = request.Messages[index].NativeContent
	}
	return struct {
		Request                                  openai.ChatCompletionRequest `json:"request"`
		NativeContent                            [][]json.RawMessage          `json:"native_content,omitempty"`
		NativeInputTokens                        int                          `json:"native_input_tokens,omitempty"`
		BedrockServiceTier                       string                       `json:"bedrock_service_tier,omitempty"`
		BedrockPerformanceLatency                string                       `json:"bedrock_performance_latency,omitempty"`
		BedrockAdditionalModelResponseFieldPaths []string                     `json:"bedrock_additional_model_response_field_paths,omitempty"`
	}{
		Request:                                  request,
		NativeContent:                            nativeContent,
		NativeInputTokens:                        request.NativeInputTokens,
		BedrockServiceTier:                       request.BedrockServiceTier,
		BedrockPerformanceLatency:                request.BedrockPerformanceLatency,
		BedrockAdditionalModelResponseFieldPaths: append([]string(nil), request.BedrockAdditionalModelResponseFieldPaths...),
	}
}

func decodeCached[T any](payload []byte) (T, bool) {
	var result T
	if json.Unmarshal(payload, &result) != nil {
		return result, false
	}
	return result, true
}

func (r Router) cacheGet(ctx context.Context, key string) ([]byte, bool, error) {
	if r.cache == nil || key == "" {
		if r.observer != nil {
			r.observer.ObserveCache("get", "disabled")
		}
		return nil, false, nil
	}
	payload, found, err := r.cache.get(ctx, key)
	if r.observer != nil {
		result := "miss"
		if err != nil {
			result = "error"
		} else if found {
			result = "hit"
		}
		r.observer.ObserveCache("get", result)
	}
	return payload, found, err
}

func (r Router) cacheSet(ctx context.Context, key string, value []byte) error {
	if r.cache == nil || key == "" {
		if r.observer != nil {
			r.observer.ObserveCache("set", "disabled")
		}
		return nil
	}
	err := r.cache.set(ctx, key, value)
	if r.observer != nil {
		result := "ok"
		if err != nil {
			result = "error"
		}
		r.observer.ObserveCache("set", result)
	}
	return err
}

// cacheIsolationScope intentionally does not allow implicit team-wide sharing.
// Only effective policy metadata enters the fingerprint; request IDs and usage
// counters would prevent hits and are excluded.
func cacheIsolationScope(req modules.RequestContext) string {
	if req.CredentialID == "" {
		return ""
	}
	canonical := func(values []string) []string { v := append([]string{}, values...); sort.Strings(v); return v }
	policy := map[string]string{}
	for key, value := range req.Metadata {
		if strings.HasPrefix(key, "policy.") || strings.HasPrefix(key, "provider.modules.") || strings.HasPrefix(key, "provider.guardrail.") {
			policy[key] = value
		}
	}
	data, _ := json.Marshal(struct {
		Credential, User, Team, Organization                string
		Roles, Tags, Models, Tools, GroupModels, GroupTools []string
		GroupsEvaluated                                     bool
		Policy                                              map[string]string
	}{req.CredentialID, req.UserID, req.TeamID, req.OrganizationID, canonical(req.Roles), canonical(req.Tags), canonical(req.AllowedModels), canonical(req.AllowedTools), canonical(req.AccessGroupModels), canonical(req.AccessGroupTools), req.AccessGroupsEvaluated, policy})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Cache transport success only when the response outcome is reusable. Empty
// status retains compatibility with adapters that predate explicit statuses.
func cacheableResponsesResult(response openai.ResponseResponse) bool {
	return response.Error == nil && response.IncompleteDetails == nil && (response.Status == "" || response.Status == "completed")
}
