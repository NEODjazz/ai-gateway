package provider

import (
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestCacheIsolationIncludesIdentityAndEffectivePolicy(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", UserID: "user", TeamID: "team", AllowedModels: []string{"b", "a"}, Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, Metadata: map[string]string{"policy.modules.dlp.enabled": "true"}}
	for _, tc := range []struct {
		name   string
		change func(*modules.RequestContext)
	}{
		{"credential", func(r *modules.RequestContext) { r.CredentialID = "other" }},
		{"user", func(r *modules.RequestContext) { r.UserID = "other" }},
		{"team", func(r *modules.RequestContext) { r.TeamID = "other" }},
		{"policy", func(r *modules.RequestContext) { r.Metadata = map[string]string{"policy.modules.dlp.enabled": "false"} }},
		{"anonymization", func(r *modules.RequestContext) {
			r.Metadata = map[string]string{"provider.modules.anonymizer.mode": "disabled"}
		}},
		{"grants", func(r *modules.RequestContext) { r.AllowedTools = []string{"new-tool"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.change(&changed)
			if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
				t.Fatal("exact scope shared")
			}
			a, _, _ := semanticRequest(base, Endpoint{Name: "endpoint"})
			b, _, _ := semanticRequest(changed, Endpoint{Name: "endpoint"})
			if a == b {
				t.Fatal("semantic scope shared")
			}
		})
	}
	same := base
	same.AllowedModels = []string{"a", "b"}
	same.RequestID = "another-execution"
	if providerCacheKey("chat", base) != providerCacheKey("chat", same) {
		t.Fatal("non-policy change invalidated cache")
	}
	noIdentity := base
	noIdentity.CredentialID = ""
	if providerCacheKey("chat", noIdentity) != "" {
		t.Fatal("anonymous cache enabled")
	}
	if affinityKey(base, "response") == affinityKey(modules.RequestContext{CredentialID: "other", UserID: base.UserID, TeamID: base.TeamID}, "response") {
		t.Fatal("affinity shared across credentials")
	}
}

func TestCacheIsolationIncludesNativeChatState(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	variants := []struct {
		name   string
		change func(*openai.ChatCompletionRequest)
	}{
		{"native input", func(request *openai.ChatCompletionRequest) {
			request.Messages[0].NativeContent = []json.RawMessage{json.RawMessage(`{"document":{"name":"A","source":{"bytes":"QQ=="}}}`)}
		}},
		{"tool result error", func(request *openai.ChatCompletionRequest) { request.Messages[0].ToolResultError = true }},
		{"document citations", func(request *openai.ChatCompletionRequest) {
			request.Messages[0].AnthropicDocumentCitations = []bool{true}
		}},
		{"document metadata", func(request *openai.ChatCompletionRequest) {
			request.Messages[0].AnthropicDocumentMetadata = []openai.DocumentMetadata{{Title: "Report", Context: "Audited"}}
		}},
		{"native token reserve", func(request *openai.ChatCompletionRequest) { request.NativeInputTokens = 1 }},
		{"clear thinking", func(request *openai.ChatCompletionRequest) { value := true; request.ClearThinking = &value }},
		{"citation options", func(request *openai.ChatCompletionRequest) { request.CitationOptions = "disabled" }},
		{"thinking", func(request *openai.ChatCompletionRequest) {
			request.Thinking = &openai.ChatThinkingOptions{Type: "enabled"}
		}},
		{"include reasoning", func(request *openai.ChatCompletionRequest) { value := true; request.IncludeReasoning = &value }},
		{"reasoning format", func(request *openai.ChatCompletionRequest) { request.ReasoningFormat = "parsed" }},
		{"service tier", func(request *openai.ChatCompletionRequest) { request.BedrockServiceTier = "priority" }},
		{"performance latency", func(request *openai.ChatCompletionRequest) { request.BedrockPerformanceLatency = "optimized" }},
		{"additional response fields", func(request *openai.ChatCompletionRequest) {
			request.BedrockAdditionalModelResponseFieldPaths = []string{"/stop_sequence"}
		}},
		{"additional model request fields", func(request *openai.ChatCompletionRequest) {
			request.BedrockAdditionalModelRequestFields = json.RawMessage(`{"top_k":42}`)
		}},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			changed := base
			changed.Request.Messages = append([]openai.Message(nil), base.Request.Messages...)
			variant.change(&changed.Request)
			if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
				t.Fatal("exact cache shared across native request state")
			}
			baseScope, _, baseEligible := semanticRequest(base, Endpoint{Name: "endpoint"})
			changedScope, _, changedEligible := semanticRequest(changed, Endpoint{Name: "endpoint"})
			if variant.name == "native input" || variant.name == "tool result error" || variant.name == "document citations" || variant.name == "document metadata" || variant.name == "additional model request fields" {
				if !baseEligible || changedEligible {
					t.Fatalf("opaque native state semantic eligibility: base=%v changed=%v", baseEligible, changedEligible)
				}
			} else if !baseEligible || !changedEligible || baseScope == changedScope {
				t.Fatal("semantic cache shared across native controls")
			}
		})
	}
}

func TestPlainTextDocumentsBypassResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: []any{map[string]any{"type": "input_document", "text": "document"}}}}}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache accepted a plain-text document")
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache accepted a plain-text document")
	}
}

func TestGeminiCachedContentBypassesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}, GeminiCachedContent: "cachedContents/owned",
	}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache accepted mutable provider cached content")
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache accepted mutable provider cached content")
	}
}

func TestBedrockRequestMetadataBypassesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, BedrockRequestMetadata: map[string]string{"trace": "billing-42"},
	}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache enabled for provider invocation metadata")
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache enabled for provider invocation metadata")
	}
}

func TestAnthropicThinkingBypassesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, AnthropicThinking: &openai.AnthropicThinkingConfig{Type: "adaptive"}}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache enabled for native thinking")
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache enabled for native thinking")
	}
}

func TestBedrockGuardrailBypassesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, BedrockGuardrailConfig: &openai.BedrockGuardrailConfig{GuardrailIdentifier: "guardrail123", GuardrailVersion: "1"},
	}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache enabled for provider guardrail execution")
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache enabled for provider guardrail execution")
	}
}

func TestGeminiSafetySettingsBypassResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, GeminiSafetySettings: []openai.GeminiSafetySetting{{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_ONLY_HIGH"}},
	}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache enabled for provider safety settings")
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache enabled for provider safety settings")
	}
}

func TestTopLevelPromptCacheControlIsolatedFromResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, AnthropicCacheControl: &openai.PromptCacheBreakpoint{Mode: "explicit", TTL: "5m"},
	}}
	first := providerCacheKey("chat", request)
	request.Request.AnthropicCacheControl = &openai.PromptCacheBreakpoint{Mode: "explicit", TTL: "1h"}
	second := providerCacheKey("chat", request)
	if first == "" || second == "" || first == second {
		t.Fatalf("exact cache keys do not isolate top-level cache control: first=%q second=%q", first, second)
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache enabled for top-level prompt cache control")
	}
}

func TestInferenceGeoIsolatedFromResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, AnthropicInferenceGeo: "global",
	}}
	if key := providerCacheKey("chat", request); key != "" {
		t.Fatalf("exact cache enabled for pinned inference geography: %q", key)
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache enabled for pinned inference geography")
	}
}

func TestContextManagementBypassesResponseCaches(t *testing.T) {
	request := modules.RequestContext{Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, AnthropicContextManagement: json.RawMessage(`{"edits":[{"type":"clear_tool_uses_20250919"}]}`),
	}}
	if key := providerCacheKey("chat", request); key != "" {
		t.Fatalf("exact cache key=%q", key)
	}
	if _, _, ok := semanticRequest(request, Endpoint{}); ok {
		t.Fatal("semantic cache accepted context-managed request")
	}
}

func TestSkillExecutionBypassesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "run"}}, AnthropicCodeExecution: true,
		AnthropicSkills: []openai.AnthropicSkillReference{{Type: "custom", SkillID: "skill_1", Version: "v1"}},
	}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache enabled for skill execution")
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache enabled for skill execution")
	}
}

func TestToolSearchBypassesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "find a tool"}}, AnthropicToolSearch: "tool_search_tool_regex_20251119",
	}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache enabled for tool search")
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache enabled for tool search")
	}
}

func TestNativeClientToolsBypassResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "run"}}, AnthropicClientTools: []openai.AnthropicClientTool{{Type: "bash_20250124", Name: "bash"}},
	}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache enabled for client tool")
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache enabled for client tool")
	}
}

func TestNativeClientToolsetsBypassResponseCaches(t *testing.T) {
	for _, toolset := range []openai.AnthropicClientToolset{{Type: "computer_toolset_20260801", Name: "computer"}, {Type: "browser_toolset_20260801", Name: "browser"}} {
		request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
			Model: "model", Messages: []openai.Message{{Role: "user", Content: "run"}}, AnthropicClientToolsets: []openai.AnthropicClientToolset{toolset},
		}}
		if providerCacheKey("chat", request) != "" {
			t.Fatalf("exact cache enabled for %s toolset", toolset.Name)
		}
		if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
			t.Fatalf("semantic cache enabled for %s toolset", toolset.Name)
		}
	}
}

func TestChatAudioInputBypassesResponseCaches(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", UserID: "user", Request: openai.ChatCompletionRequest{
		Model: "model",
		Messages: []openai.Message{
			{Role: "user", Content: []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "UklGRgAAAABXQVZF", "format": "wav"}}}},
		},
	}}
	if providerCacheKey("chat", request) != "" {
		t.Fatal("exact cache enabled for inline audio")
	}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache enabled for inline audio")
	}
}

func TestMemoryCacheAndAffinityBounded(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1, 0)
	c := newExactCache(time.Minute)
	c.now = func() time.Time { return now }
	a := newAffinityStore(time.Minute, nil).(*memoryAffinity)
	a.now = c.now
	for i := 0; i < 10000; i++ {
		key := fmt.Sprint(i)
		_ = c.set(ctx, key, []byte("value"))
		_ = a.set(ctx, key, "endpoint")
	}
	if n, b := c.entries.Size(); n > memoryCacheMaxEntries || b > memoryCacheMaxBytes {
		t.Fatal("cache overflow")
	}
	if n, b := a.entries.Size(); n > memoryAffinityMaxEntries || b > memoryAffinityMaxBytes {
		t.Fatal("affinity overflow")
	}
	now = now.Add(time.Hour)
	_ = c.set(ctx, "new", []byte("value"))
	_ = a.set(ctx, "new", "endpoint")
	if n, _ := c.entries.Size(); n != 1 {
		t.Fatal("expired cache retained")
	}
	if n, _ := a.entries.Size(); n != 1 {
		t.Fatal("expired affinity retained")
	}
}

func TestSemanticCacheHasGlobalByteLimit(t *testing.T) {
	c := newSemanticResponseCache(semanticCacheConfig{ttl: time.Minute, maxEntries: 10000, maxBytes: 1 << 20, embedder: &semanticTestEmbedder{}})
	if c.maxEntries != semanticCacheMaxEntries {
		t.Fatal("entry limit not clamped")
	}
	payload := make([]byte, 1<<20)
	for i := 0; i < 80; i++ {
		c.set(fmt.Sprint(i), []float64{1}, payload)
	}
	count, bytes := 0, 0
	for scope, entries := range c.entries {
		for _, entry := range entries {
			count++
			bytes += semanticEntryBytes(scope, entry.vector, entry.payload)
		}
	}
	if bytes > semanticCacheMaxTotalBytes || count >= 80 {
		t.Fatalf("unbounded semantic cache: %d entries, %d bytes", count, bytes)
	}
}

func TestReasoningHistoryBypassesSemanticCache(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "answer", Reasoning: []openai.ReasoningBlock{{Type: "thinking", Thinking: "private plan", Signature: "signed"}}},
	}}}
	if _, _, eligible := semanticRequest(request, Endpoint{Name: "endpoint"}); eligible {
		t.Fatal("semantic cache accepted signed reasoning history")
	}
}

func TestProviderCacheKeySeparatesClientAPIContract(t *testing.T) {
	base := modules.RequestContext{
		CredentialID: "credential",
		Request:      openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}},
		Metadata:     map[string]string{"gateway.api_type": "chat"},
	}
	messages := base
	messages.Metadata = map[string]string{"gateway.api_type": "messages"}
	if providerCacheKey("chat", base) == providerCacheKey("chat", messages) {
		t.Fatal("chat and Messages contracts share an exact-cache key")
	}
	baseScope, _, baseEligible := semanticRequest(base, Endpoint{Name: "endpoint"})
	messagesScope, _, messagesEligible := semanticRequest(messages, Endpoint{Name: "endpoint"})
	if !baseEligible || !messagesEligible || baseScope == messagesScope {
		t.Fatal("chat and Messages contracts share a semantic-cache scope")
	}
}
