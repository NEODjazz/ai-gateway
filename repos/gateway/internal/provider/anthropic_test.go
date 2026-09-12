package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnthropicRefusalMapsToContentFilter(t *testing.T) {
	if got := anthropicFinishReason("refusal"); got != "content_filter" {
		t.Fatalf("finish reason=%q", got)
	}
}

func TestAnthropicMapsMaxCompletionTokensToMaxTokens(t *testing.T) {
	limit := 321
	request := anthropicChatRequest(openai.ChatCompletionRequest{Model: "claude", MaxCompletionTokens: &limit}, false)
	if request.MaxTokens != limit {
		t.Fatalf("max_completion_tokens was not mapped: %+v", request)
	}
}

func TestAnthropicPreservesExplicitZeroMaxTokens(t *testing.T) {
	zero := 0
	request := anthropicChatRequest(openai.ChatCompletionRequest{AllowZeroMaxTokens: true, MaxTokens: &zero}, false)
	if request.MaxTokens != 0 {
		t.Fatalf("max_tokens=%d", request.MaxTokens)
	}
	ordinary := anthropicChatRequest(openai.ChatCompletionRequest{MaxTokens: &zero}, false)
	if ordinary.MaxTokens != defaultAnthropicMaxTokens {
		t.Fatalf("ordinary zero unexpectedly bypassed default: %d", ordinary.MaxTokens)
	}
	if got := strings.Join(requiredChatCapabilities(openai.ChatCompletionRequest{AllowZeroMaxTokens: true, MaxTokens: &zero}, false), ","); got != "chat,zero_output" {
		t.Fatalf("capabilities=%q", got)
	}
	required := requiredChatCapabilities(openai.ChatCompletionRequest{AllowZeroMaxTokens: true, MaxTokens: &zero}, false)
	if (Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat"}}).supportsCapabilities(required...) || !(Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat", "zero_output"}}).supportsCapabilities(required...) {
		t.Fatal("zero output routing capability is not enforced")
	}
}

func TestAnthropicInferenceGeoWireUsageAndCapability(t *testing.T) {
	request := openai.ChatCompletionRequest{AnthropicInferenceGeo: "us"}
	converted := anthropicChatRequest(request, false)
	encoded, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	if converted.InferenceGeo != "us" || !strings.Contains(string(encoded), `"inference_geo":"us"`) {
		t.Fatalf("request=%s", encoded)
	}
	if got := strings.Join(requiredChatCapabilities(request, false), ","); got != "chat,inference_geo" {
		t.Fatalf("capabilities=%q", got)
	}
	required := requiredChatCapabilities(request, false)
	if (Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat"}}).supportsCapabilities(required...) || !(Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat", "inference_geo"}}).supportsCapabilities(required...) {
		t.Fatal("inference geography routing capability is not enforced")
	}
	response := anthropicToChatCompletion(anthropicResponse{Usage: anthropicUsage{InferenceGeo: "us"}}, "model")
	if response.Usage.InferenceGeo != "us" {
		t.Fatalf("reported geo=%q", response.Usage.InferenceGeo)
	}
	if validateAnthropicInferenceGeo("us", "global") == nil || validateAnthropicInferenceGeo("us", "") == nil {
		t.Fatal("requested inference geography mismatch accepted")
	}
}

func TestAnthropicContextManagementWireAndCapability(t *testing.T) {
	request := openai.ChatCompletionRequest{AnthropicContextManagement: json.RawMessage(`{"edits":[{"type":"clear_tool_uses_20250919"}]}`)}
	converted := anthropicChatRequest(request, false)
	encoded, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"context_management":{"edits"`) || anthropicBetaFeatures(converted) != "context-management-2025-06-27" {
		t.Fatalf("request=%s beta=%q", encoded, anthropicBetaFeatures(converted))
	}
	if got := strings.Join(requiredChatCapabilities(request, false), ","); got != "chat,context_management" {
		t.Fatalf("capabilities=%q", got)
	}
	required := requiredChatCapabilities(request, false)
	if (Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat"}}).supportsCapabilities(required...) || !(Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat", "context_management"}}).supportsCapabilities(required...) {
		t.Fatal("context management routing capability is not enforced")
	}
	response := anthropicToChatCompletion(anthropicResponse{ContextManagement: json.RawMessage(`{"applied_edits":[]}`)}, "model")
	if string(response.NativeContextManagement) != `{"applied_edits":[]}` {
		t.Fatalf("response context=%s", response.NativeContextManagement)
	}
	reported := json.RawMessage(`{"applied_edits":[{"type":"clear_tool_uses_20250919"}]}`)
	if validateAnthropicContextManagement(nil, reported) == nil || validateAnthropicContextManagement(json.RawMessage(`{"edits":[{"type":"clear_thinking_20251015"}]}`), reported) == nil {
		t.Fatal("unrequested context management result accepted")
	}
}

func TestAnthropicToolResultErrorWireAndCapability(t *testing.T) {
	request := openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "tool", ToolCallID: "call_1", Content: "failed", ToolResultError: true}}}
	_, messages := anthropicMessages(request.Messages)
	encoded, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"tool_use_id":"call_1","is_error":true,"content":"failed"`) {
		t.Fatalf("messages=%s", encoded)
	}
	if got := strings.Join(requiredChatCapabilities(request, false), ","); got != "chat,tool_result_error" {
		t.Fatalf("capabilities=%q", got)
	}
	required := requiredChatCapabilities(request, false)
	if (Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat"}}).supportsCapabilities(required...) || !(Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat", "tool_result_error"}}).supportsCapabilities(required...) {
		t.Fatal("tool-result error routing capability is not enforced")
	}
}

func TestAnthropicPDFDocumentWireAndCapability(t *testing.T) {
	file := map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,JVBERi0xLjcKY29udGVudA==", "filename": "input.pdf"}
	request := openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: []any{file}}}}
	_, messages := anthropicMessages(request.Messages)
	encoded, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"type":"document","source":{"data":"JVBERi0xLjcKY29udGVudA==","media_type":"application/pdf","type":"base64"}`) {
		t.Fatalf("messages=%s", encoded)
	}
	if got := strings.Join(requiredChatCapabilities(request, false), ","); got != "chat,file_input" {
		t.Fatalf("capabilities=%q", got)
	}
	required := requiredChatCapabilities(request, false)
	if (Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat"}}).supportsCapabilities(required...) || !(Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat", "file_input"}}).supportsCapabilities(required...) {
		t.Fatal("PDF document routing capability is not enforced")
	}
}

func TestAnthropicPDFDocumentCitationsWireAndCapability(t *testing.T) {
	file := map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,JVBERi0xLjcKY29udGVudA==", "filename": "input.pdf"}
	request := openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: []any{file}, AnthropicDocumentCitations: []bool{true}}}}
	_, messages := anthropicMessages(request.Messages)
	encoded, err := json.Marshal(messages)
	if err != nil || !strings.Contains(string(encoded), `"citations":{"enabled":true}`) {
		t.Fatalf("messages=%s err=%v", encoded, err)
	}
	if got := strings.Join(requiredChatCapabilities(request, false), ","); got != "chat,document_citations,file_input" {
		t.Fatalf("capabilities=%q", got)
	}
	required := requiredChatCapabilities(request, false)
	if (Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat", "file_input"}}).supportsCapabilities(required...) || !(Endpoint{Provider: Anthropic{}, Capabilities: []string{"chat", "file_input", "document_citations"}}).supportsCapabilities(required...) {
		t.Fatal("document citations routing capability is not enforced")
	}
}

func TestAnthropicThinkingWireAndCapability(t *testing.T) {
	budget := 2048
	request := anthropicChatRequest(openai.ChatCompletionRequest{AnthropicThinking: &openai.AnthropicThinkingConfig{Type: "enabled", BudgetTokens: &budget, Display: "summarized"}}, false)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if request.Thinking == nil || request.Thinking.BudgetTokens == nil || *request.Thinking.BudgetTokens != budget || !strings.Contains(string(encoded), `"thinking":{"type":"enabled","budget_tokens":2048,"display":"summarized"}`) {
		t.Fatalf("request=%s", encoded)
	}
	if got := strings.Join(requiredChatCapabilities(openai.ChatCompletionRequest{AnthropicThinking: &openai.AnthropicThinkingConfig{Type: "adaptive"}}, false), ","); got != "chat,thinking" {
		t.Fatalf("capabilities=%q", got)
	}
}

func TestAnthropicForwardsTopK(t *testing.T) {
	topK := 40
	request := anthropicChatRequest(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &topK}}, false)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if request.TopK == nil || *request.TopK != topK || !strings.Contains(string(encoded), `"top_k":40`) {
		t.Fatalf("request=%s", encoded)
	}
	if err := (Anthropic{}).ValidateChatParameters(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &topK}}); err != nil {
		t.Fatalf("top_k rejected: %v", err)
	}
	negative := -1
	if err := (Anthropic{}).ValidateChatParameters(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &negative}}); err == nil {
		t.Fatal("negative top_k accepted")
	}
}

func TestAnthropicPreservesCodeExecutionVersion(t *testing.T) {
	request := anthropicChatRequest(openai.ChatCompletionRequest{AnthropicCodeExecution: true, AnthropicCodeExecutionType: "code_execution_20260521"}, false)
	if len(request.Tools) != 1 || request.Tools[0].Type != "code_execution_20260521" || request.Tools[0].Name != "code_execution" {
		t.Fatalf("tools=%+v", request.Tools)
	}
}

func TestAnthropicNativeClientToolsWireAndCapabilities(t *testing.T) {
	maxCharacters := 10000
	request := anthropicChatRequest(openai.ChatCompletionRequest{
		AnthropicClientTools: []openai.AnthropicClientTool{
			{Type: "memory_20250818", Name: "memory"},
			{Type: "bash_20250124", Name: "bash", AllowedCallers: []string{"direct"}},
			{Type: "text_editor_20250728", Name: "str_replace_based_edit_tool", MaxCharacters: &maxCharacters},
		},
		ToolChoice: "required",
	}, false)
	if len(request.Tools) != 3 || request.ToolChoice["type"] != "any" || request.Tools[0].Type != "memory_20250818" || request.Tools[1].AllowedCallers[0] != "direct" || request.Tools[2].MaxCharacters == nil || *request.Tools[2].MaxCharacters != maxCharacters {
		t.Fatalf("request=%+v", request)
	}
	if got := strings.Join(requiredChatCapabilities(openai.ChatCompletionRequest{AnthropicClientTools: []openai.AnthropicClientTool{{Type: "memory_20250818"}, {Type: "bash_20250124"}, {Type: "text_editor_20250728"}}}, false), ","); got != "chat,memory_tool,bash_tool,text_editor_tool" {
		t.Fatalf("capabilities=%q", got)
	}
}

func TestAnthropicComputerToolsetWireContinuationAndCapabilities(t *testing.T) {
	enabled := false
	request := anthropicChatRequest(openai.ChatCompletionRequest{
		AnthropicClientToolsets: []openai.AnthropicClientToolset{{Type: "computer_toolset_20260801", Name: "computer", Configs: map[string]openai.AnthropicToolsetMemberConfig{"zoom": {Enabled: &enabled}}, AllowedCallers: []string{"direct"}}},
		Messages: []openai.Message{
			{Role: "assistant", NativeContent: []json.RawMessage{json.RawMessage(`{"type":"tool_use","id":"call","name":"screenshot","toolset_name":"computer","input":{}}`)}},
			{Role: "user", NativeContent: []json.RawMessage{json.RawMessage(`{"type":"tool_result","tool_use_id":"call","toolset_name":"computer","content":"ok"}`)}},
		},
	}, false)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "" || request.Tools[0].Configs["zoom"].Enabled == nil || *request.Tools[0].Configs["zoom"].Enabled || request.Tools[0].AllowedCallers[0] != "direct" || !strings.Contains(string(encoded), `"toolset_name":"computer"`) {
		t.Fatalf("request=%s", encoded)
	}
	if got := strings.Join(requiredChatCapabilities(openai.ChatCompletionRequest{AnthropicClientToolsets: []openai.AnthropicClientToolset{{Type: "computer_toolset_20260801"}}}, false), ","); got != "chat,computer_toolset" {
		t.Fatalf("capabilities=%q", got)
	}
	calls := anthropicToolCalls(anthropicResponse{Content: []anthropicContent{{Type: "tool_use", ID: "next", Name: "left_click", ToolsetName: "computer", Input: map[string]any{"coordinate": []int{1, 2}}}}})
	if len(calls) != 1 || calls[0].ToolsetName != "computer" || calls[0].Function.Name != "left_click" {
		t.Fatalf("response toolset identity lost: %+v", calls)
	}
}

func TestAnthropicBrowserToolsetWireContinuationAndCapabilities(t *testing.T) {
	enabled := true
	request := anthropicChatRequest(openai.ChatCompletionRequest{
		AnthropicClientToolsets: []openai.AnthropicClientToolset{{Type: "browser_toolset_20260801", Name: "browser", Configs: map[string]openai.AnthropicToolsetMemberConfig{"read_console": {Enabled: &enabled}}, AllowedCallers: []string{"direct"}}},
		Messages: []openai.Message{
			{Role: "assistant", NativeContent: []json.RawMessage{json.RawMessage(`{"type":"tool_use","id":"call","name":"navigate","toolset_name":"browser","input":{"url":"https://example.com"}}`)}},
			{Role: "user", NativeContent: []json.RawMessage{json.RawMessage(`{"type":"tool_result","tool_use_id":"call","toolset_name":"browser","content":[{"type":"text","text":"ok"}]}`)}},
		},
	}, false)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != "" || request.Tools[0].Configs["read_console"].Enabled == nil || !*request.Tools[0].Configs["read_console"].Enabled || !strings.Contains(string(encoded), `"toolset_name":"browser"`) {
		t.Fatalf("request=%s", encoded)
	}
	if got := strings.Join(requiredChatCapabilities(openai.ChatCompletionRequest{AnthropicClientToolsets: []openai.AnthropicClientToolset{{Type: "browser_toolset_20260801"}}}, false), ","); got != "chat,browser_toolset" {
		t.Fatalf("capabilities=%q", got)
	}
	calls := anthropicToolCalls(anthropicResponse{Content: []anthropicContent{{Type: "tool_use", ID: "next", Name: "read_page", ToolsetName: "browser", Input: map[string]any{}}}})
	if len(calls) != 1 || calls[0].ToolsetName != "browser" || calls[0].Function.Name != "read_page" {
		t.Fatalf("response toolset identity lost: %+v", calls)
	}
}

func TestAnthropicToolSearchWireAndContinuation(t *testing.T) {
	native := []json.RawMessage{
		json.RawMessage(`{"type":"server_tool_use","id":"srv_1","name":"tool_search_tool_bm25","input":{"query":"weather"}}`),
		json.RawMessage(`{"type":"tool_search_tool_result","tool_use_id":"srv_1","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"weather"}]}}`),
	}
	request := anthropicChatRequest(openai.ChatCompletionRequest{
		Model: "claude", AnthropicToolSearch: "tool_search_tool_bm25_20251119",
		Tools:    []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}, DeferLoading: true}}},
		Messages: []openai.Message{{Role: "assistant", NativeContent: native}, {Role: "user", Content: "continue"}},
	}, false)
	if len(request.Tools) != 2 || !request.Tools[0].DeferLoading || request.Tools[1].Type != "tool_search_tool_bm25_20251119" || request.Tools[1].Name != "tool_search_tool_bm25" {
		t.Fatalf("tools=%+v", request.Tools)
	}
	encoded, err := json.Marshal(request.Messages[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	want := "[" + string(native[0]) + "," + string(native[1]) + "]"
	if string(encoded) != want {
		t.Fatalf("native continuation changed: %s", encoded)
	}
	if got := anthropicBetaFeatures(request); got != "advanced-tool-use-2025-11-20" {
		t.Fatalf("beta header=%q", got)
	}
	content := []anthropicContent{{Type: "server_tool_use", ID: "srv_1", Name: "tool_search_tool_bm25", Input: map[string]any{"query": "weather"}}, {Type: "tool_search_tool_result", ToolUseID: "srv_1", Content: map[string]any{"type": "tool_search_tool_search_result"}}}
	if err := validateAnthropicNativeMessageContent(content); err != nil || len(anthropicNativeMessageContent(content)) != 2 {
		t.Fatalf("tool search response rejected: %v", err)
	}
	if err := validateAnthropicToolSearchContent(content, "tool_search_tool_bm25_20251119"); err != nil {
		t.Fatal(err)
	}
	if err := validateAnthropicToolSearchContent(content, ""); err == nil {
		t.Fatal("unrequested tool search response accepted")
	}
	if got := strings.Join(requiredChatCapabilities(openai.ChatCompletionRequest{AnthropicToolSearch: "tool_search_tool_bm25_20251119"}, false), ","); got != "chat,tool_search" {
		t.Fatalf("capabilities=%q", got)
	}
}

func TestAnthropicSkillExecutionWireContract(t *testing.T) {
	var upstream anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Anthropic-Beta") != "skills-2025-10-02" {
			t.Fatalf("missing Skills beta header: %v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"code_execution","input":{"code":"print(1)"}},{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"code_execution_result","stdout":"1","stderr":"","return_code":0,"content":[]}}],"stop_reason":"pause_turn","usage":{"input_tokens":4,"output_tokens":1,"server_tool_use":{"code_execution_requests":1}},"container":{"id":"container_1","expires_at":"2026-09-12T14:00:00Z","skills":[{"type":"custom","skill_id":"skill_1","version":"v1"}]}}`))
	}))
	defer server.Close()

	maxTokens := 20
	client := NewAnthropic(server.URL, "key", false)
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "claude", MaxTokens: &maxTokens, Messages: []openai.Message{{Role: "user", Content: "run"}},
		AnthropicSkills: []openai.AnthropicSkillReference{{Type: "custom", SkillID: "skill_1", Version: "v1"}}, AnthropicContainerID: "container_previous", AnthropicCodeExecution: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Container == nil || upstream.Container.ID != "container_previous" || len(upstream.Container.Skills) != 1 || upstream.Container.Skills[0].SkillID != "skill_1" {
		t.Fatalf("skill container was not forwarded: %+v", upstream.Container)
	}
	if !strings.Contains(string(response.NativeContainer), `"id":"container_1"`) {
		t.Fatalf("container response was not preserved: %s", response.NativeContainer)
	}
	if response.Usage.ToolRequests != 1 || response.Choices[0].FinishReason != "pause_turn" || len(response.Choices[0].Message.NativeContent) != 2 {
		t.Fatalf("native execution result was not preserved: %+v", response)
	}
	if !hasCapability(requiredChatCapabilities(openai.ChatCompletionRequest{AnthropicSkills: upstream.Container.Skills}, false), "skills") {
		t.Fatal("skill execution did not require the skills capability")
	}
}

func TestAnthropicMapsAssistantPrefillWithoutWireExtension(t *testing.T) {
	prefix := true
	request := openai.ChatCompletionRequest{Model: "claude", Messages: []openai.Message{
		{Role: "user", Content: "Choose A or B"},
		{Role: "assistant", Content: "The answer is (", Prefix: &prefix},
	}}
	if err := (Anthropic{}).ValidateChatParameters(request); err != nil {
		t.Fatal(err)
	}
	converted := anthropicChatRequest(request, false)
	if len(converted.Messages) != 2 {
		t.Fatalf("prefill changed during conversion: %+v", converted.Messages)
	}
	blocks, ok := converted.Messages[1].Content.([]anthropicContent)
	if converted.Messages[1].Role != "assistant" || !ok || len(blocks) != 1 || blocks[0].Text != "The answer is (" {
		t.Fatalf("prefill changed during conversion: %+v", converted.Messages)
	}
	encoded, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"prefix"`) {
		t.Fatalf("internal prefix marker leaked upstream: %s", encoded)
	}
}

func TestAnthropicMapsPromptCacheBreakpoints(t *testing.T) {
	request := openai.ChatCompletionRequest{
		Model: "claude",
		Messages: []openai.Message{
			{Role: "system", Content: []any{map[string]any{"type": "text", "text": "rules", "prompt_cache_breakpoint": map[string]any{"mode": "explicit", "ttl": "1h"}}}},
			{Role: "user", Content: []any{map[string]any{"type": "text", "text": "question", "prompt_cache_breakpoint": map[string]any{"mode": "explicit"}}}},
		},
		Tools:                 []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup", Parameters: map[string]any{"type": "object"}, PromptCacheBreakpoint: &openai.PromptCacheBreakpoint{Mode: "explicit", TTL: "5m"}}}},
		AnthropicCacheControl: &openai.PromptCacheBreakpoint{Mode: "explicit", TTL: "1h"},
	}
	converted := anthropicChatRequest(request, false)
	system, ok := converted.System.([]anthropicContent)
	message, messageOK := converted.Messages[0].Content.([]anthropicContent)
	if !ok || !messageOK || len(system) != 1 || len(message) != 1 || len(converted.Tools) != 1 {
		t.Fatalf("unexpected conversion: %+v", converted)
	}
	if system[0].CacheControl == nil || system[0].CacheControl.Type != "ephemeral" || system[0].CacheControl.TTL != "1h" || message[0].CacheControl == nil || message[0].CacheControl.TTL != "" || converted.Tools[0].CacheControl == nil || converted.Tools[0].CacheControl.TTL != "5m" || converted.CacheControl == nil || converted.CacheControl.TTL != "1h" {
		t.Fatalf("cache controls changed: system=%+v message=%+v tool=%+v", system[0], message[0], converted.Tools[0])
	}
	encoded, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"cache_control":{"type":"ephemeral","ttl":"1h"}`, `"cache_control":{"type":"ephemeral"}`, `"cache_control":{"type":"ephemeral","ttl":"5m"}`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("missing %s in %s", expected, encoded)
		}
	}
}

func TestAnthropicChatCompletions(t *testing.T) {
	var upstreamRequest anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Fatalf("expected anthropic api key header")
		}
		if r.Header.Get("Anthropic-Version") == "" {
			t.Fatalf("expected anthropic version header")
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(anthropicResponse{
			ID:         "msg-test",
			Type:       "message",
			Role:       "assistant",
			Model:      upstreamRequest.Model,
			StopReason: "end_turn",
			Content: []anthropicContent{
				{Type: "text", Text: "hello"},
			},
			Usage: anthropicUsage{InputTokens: 3, OutputTokens: 2, CacheReadInputTokens: 2, CacheCreationInputTokens: 1},
		})
	}))
	defer server.Close()

	maxTokens := 77
	temperature := 0.3
	topP := 0.8
	provider := NewAnthropic(server.URL, "test-key", false)
	response, err := provider.ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:       "claude-test",
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
		TopP:        &topP,
		Messages: []openai.Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstreamRequest.System != "be concise" {
		t.Fatalf("unexpected system prompt: %q", upstreamRequest.System)
	}
	if upstreamRequest.MaxTokens != 77 || upstreamRequest.Temperature == nil || *upstreamRequest.Temperature != temperature || upstreamRequest.TopP == nil || *upstreamRequest.TopP != topP {
		t.Fatalf("generation options were not forwarded: %+v", upstreamRequest)
	}
	if len(upstreamRequest.Messages) != 1 || upstreamRequest.Messages[0].Role != "user" || upstreamRequest.Messages[0].Content != "hi" {
		t.Fatalf("unexpected upstream messages: %+v", upstreamRequest.Messages)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("unexpected response content: %+v", response)
	}
	if response.Usage.PromptTokens != 6 || response.Usage.TotalTokens != 8 || response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedTokens != 2 || response.Usage.PromptTokensDetails.CacheWriteTokens != 1 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}

func TestAnthropicMapsWebSearchAndBillsActualUsage(t *testing.T) {
	var upstream map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"msg-search","type":"message","role":"assistant","model":"claude-test","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"weather"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://example.com/weather","title":"Weather","encrypted_content":"safe"}]},{"type":"text","text":"Today is sunny.","citations":[{"type":"web_search_result_location","url":"https://example.com/weather","title":"Weather","cited_text":"sunny"}]}],"usage":{"input_tokens":4,"output_tokens":3,"server_tool_use":{"web_search_requests":2}}}`))
	}))
	defer server.Close()

	provider := NewAnthropic(server.URL, "", false)
	response, err := provider.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "claude-test", Messages: []openai.Message{{Role: "user", Content: "weather"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{UserLocation: &openai.ChatWebSearchUserLocation{
			Type: "approximate", Approximate: &openai.ChatWebSearchApproximateLocation{City: "Paris", Country: "FR", Timezone: "Europe/Paris"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, ok := upstream["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("upstream tools=%#v", upstream["tools"])
	}
	search, ok := tools[0].(map[string]any)
	location, locationOK := search["user_location"].(map[string]any)
	if !ok || !locationOK || search["type"] != "web_search_20250305" || search["name"] != "web_search" || search["max_uses"] != float64(openai.WebSearchMaxUses) || location["type"] != "approximate" || location["city"] != "Paris" || location["country"] != "FR" {
		t.Fatalf("native search tool=%#v", search)
	}
	if _, found := search["input_schema"]; found {
		t.Fatalf("server tool contains client input schema: %#v", search)
	}
	annotations := response.Choices[0].Message.Annotations
	if response.Usage.SearchRequests != 2 || len(annotations) != 1 || annotations[0].URLCitation.StartIndex != 9 || annotations[0].URLCitation.EndIndex != 14 || annotations[0].URLCitation.URL != "https://example.com/weather" {
		t.Fatalf("response=%+v", response)
	}
	native := response.Choices[0].Message.NativeContent
	if len(native) != 3 || !strings.Contains(string(native[0]), `"type":"server_tool_use"`) || !strings.Contains(string(native[1]), `"encrypted_content":"safe"`) || !strings.Contains(string(native[2]), `"type":"text"`) {
		t.Fatalf("native content order or fields changed: %q", native)
	}
}

func TestAnthropicMapsCurrentWebToolControls(t *testing.T) {
	useCache := false
	request := anthropicChatRequest(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{
		WebSearchOptions: &openai.ChatWebSearchOptions{NativeType: "web_search_20260318", AllowedDomains: []string{"example.com"}, AllowedCallers: []string{"direct"}, ResponseInclusion: "excluded"},
		WebFetchOptions:  &openai.ChatWebFetchOptions{NativeType: "web_fetch_20260318", AllowedDomains: []string{"docs.example.com"}, MaxContentTokens: 1000, AllowedCallers: []string{"code_execution_20260521"}, UseCache: &useCache, ResponseInclusion: "full"},
	}}, false)
	if len(request.Tools) != 2 || request.Tools[0].Type != "web_search_20260318" || request.Tools[0].AllowedDomains[0] != "example.com" || request.Tools[0].AllowedCallers[0] != "direct" || request.Tools[0].ResponseInclusion != "excluded" {
		t.Fatalf("search tool=%+v", request.Tools)
	}
	fetch := request.Tools[1]
	if fetch.Type != "web_fetch_20260318" || fetch.UseCache == nil || *fetch.UseCache || fetch.AllowedCallers[0] != "code_execution_20260521" || fetch.ResponseInclusion != "full" {
		t.Fatalf("fetch tool=%+v", fetch)
	}
}

func TestAnthropicRejectsUnrepresentableSearchContextSize(t *testing.T) {
	err := (Anthropic{}).ValidateChatParameters(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{SearchContextSize: "high"}}})
	var failure *Error
	if !errors.As(err, &failure) || failure.Param != "web_search_options.search_context_size" || failure.UpstreamCode != "unsupported_parameter" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnthropicMapsBoundedWebFetch(t *testing.T) {
	var upstream map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"msg-fetch","type":"message","role":"assistant","model":"claude-test","stop_reason":"end_turn","content":[{"type":"web_fetch_tool_result","tool_use_id":"srvtoolu_1","content":{"type":"web_fetch_result","url":"https://docs.example.com/page","content":{"type":"document","source":{"type":"text","data":"source"}}}},{"type":"text","text":"Fetched.","citations":[{"type":"char_location","document_index":0,"document_title":"Page","cited_text":"Fetched"}]}],"usage":{"input_tokens":25,"output_tokens":3,"server_tool_use":{"web_fetch_requests":2}}}`))
	}))
	defer server.Close()
	maximum := 2
	response, err := NewAnthropic(server.URL, "", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "claude-test", Messages: []openai.Message{{Role: "user", Content: "Read https://docs.example.com/page"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{WebFetchOptions: &openai.ChatWebFetchOptions{AllowedDomains: []string{"docs.example.com"}, MaxUses: &maximum, MaxContentTokens: 20000}},
	})
	if err != nil || response.Usage.PromptTokens != 25 || len(response.Choices[0].Message.Annotations) != 1 || response.Choices[0].Message.Annotations[0].URLCitation.URL != "https://docs.example.com/page" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	native := response.Choices[0].Message.NativeContent
	if len(native) != 2 || !strings.Contains(string(native[0]), `"type":"web_fetch_tool_result"`) || !strings.Contains(string(native[0]), `"data":"source"`) || !strings.Contains(string(native[1]), `"text":"Fetched."`) {
		t.Fatalf("native fetch content changed: %q", native)
	}
	tools, ok := upstream["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools=%#v", upstream["tools"])
	}
	fetch := tools[0].(map[string]any)
	citations := fetch["citations"].(map[string]any)
	domains := fetch["allowed_domains"].([]any)
	if fetch["type"] != "web_fetch_20250910" || fetch["name"] != "web_fetch" || fetch["max_uses"] != float64(2) || fetch["max_content_tokens"] != float64(20000) || citations["enabled"] != true || len(domains) != 1 || domains[0] != "docs.example.com" {
		t.Fatalf("fetch=%#v", fetch)
	}
}

func TestAnthropicRejectsInvalidWebFetchUsage(t *testing.T) {
	if err := validateAnthropicUsage(anthropicUsage{ServerToolUse: &anthropicServerToolUsage{WebFetchRequests: openai.WebFetchMaxUses + 1}}); err == nil {
		t.Fatal("invalid web fetch usage accepted")
	}
}

func TestAnthropicRejectsUsageAboveRequestedServerToolLimit(t *testing.T) {
	searchLimit, fetchLimit := 1, 2
	usage := anthropicUsage{ServerToolUse: &anthropicServerToolUsage{WebSearchRequests: 2}}
	if err := validateAnthropicRequestedToolUsage(usage, &openai.ChatWebSearchOptions{MaxUses: &searchLimit}, nil, false); err == nil {
		t.Fatal("search usage above requested limit accepted")
	}
	usage.ServerToolUse = &anthropicServerToolUsage{WebFetchRequests: 3}
	if err := validateAnthropicRequestedToolUsage(usage, nil, &openai.ChatWebFetchOptions{MaxUses: &fetchLimit}, false); err == nil {
		t.Fatal("fetch usage above requested limit accepted")
	}
}

func TestAnthropicRejectsUnsafeNativeContent(t *testing.T) {
	for _, content := range [][]anthropicContent{
		{{Type: "server_tool_use"}, {Type: "unknown"}},
		{{Type: "web_search_tool_result", Content: strings.Repeat("x", 4<<20)}},
	} {
		if err := validateAnthropicNativeMessageContent(content); err == nil {
			t.Fatalf("unsafe native content accepted: %+v", content)
		}
	}
}

func TestAnthropicRejectsFetchOutsideAllowedDomains(t *testing.T) {
	content := []anthropicContent{{Type: "web_fetch_tool_result", Content: map[string]any{"type": "web_fetch_result", "url": "https://private.example.net/page"}}}
	options := &openai.ChatWebFetchOptions{AllowedDomains: []string{"example.com"}, MaxContentTokens: 1000}
	if err := validateAnthropicFetchContent(content, options); err == nil {
		t.Fatal("fetch outside allowed domains accepted")
	}
	content[0].Content = map[string]any{"type": "web_fetch_result", "url": "https://docs.example.com/page"}
	if err := validateAnthropicFetchContent(content, options); err != nil {
		t.Fatalf("allowed subdomain rejected: %v", err)
	}
}

func TestAnthropicConvertsOpenAIVisionContent(t *testing.T) {
	_, messages := anthropicMessages([]openai.Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
	}}})
	blocks, ok := messages[0].Content.([]anthropicContent)
	if !ok || len(blocks) != 2 || blocks[0].Text != "describe" || blocks[1].Type != "image" {
		t.Fatalf("unexpected Anthropic vision blocks: %+v", messages[0].Content)
	}
	source, ok := blocks[1].Source.(map[string]any)
	if !ok || source["media_type"] != "image/png" || source["data"] != "iVBORw0KGgo=" {
		t.Fatalf("unexpected Anthropic image source: %+v", blocks[1].Source)
	}
}

func TestAnthropicConvertsResponsesVisionInput(t *testing.T) {
	request, err := anthropicResponsesRequest(openai.ResponseRequest{Input: []any{map[string]any{
		"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "describe"},
			map[string]any{"type": "input_image", "image_url": "data:image/jpeg;base64,/9j/"},
		},
	}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	blocks, ok := request.Messages[0].Content.([]anthropicContent)
	if !ok || len(blocks) != 2 || blocks[1].Type != "image" {
		t.Fatalf("unexpected Responses vision conversion: %+v", request.Messages)
	}
}

func TestAnthropicStreamsChatCompletions(t *testing.T) {
	var upstreamRequest anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":3,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer server.Close()

	var payloads []string
	provider := NewAnthropic(server.URL, "test-key", true)
	response, err := provider.StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:  "claude-test",
		Stream: true,
		Messages: []openai.Message{
			{Role: "user", Content: "hi"},
		},
	}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !upstreamRequest.Stream {
		t.Fatal("expected upstream stream to be enabled")
	}
	if len(payloads) != 3 {
		t.Fatalf("expected three chat payloads, got %d: %v", len(payloads), payloads)
	}
	if !strings.Contains(payloads[0], `"content":"hel"`) || !strings.Contains(payloads[1], `"content":"lo"`) || !strings.Contains(payloads[2], `"finish_reason":"stop"`) {
		t.Fatalf("unexpected payloads: %v", payloads)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" || response.Usage.PromptTokens != 6 || response.Usage.TotalTokens != 8 || response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedTokens != 2 || response.Usage.PromptTokensDetails.CacheWriteTokens != 1 {
		t.Fatalf("unexpected streamed response: %+v", response)
	}
}

func TestAnthropicStreamsWebSearchCitationsAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n"+`data: {"type":"message_start","message":{"id":"msg-search","model":"claude-test","usage":{"input_tokens":4,"server_tool_use":{"web_search_requests":1}}}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n"+`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n"+`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Today is sunny."}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n"+`data: {"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.com/weather","title":"Weather","cited_text":"sunny"}}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3,"server_tool_use":{"web_search_requests":2}}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()

	var payloads []string
	response, err := NewAnthropic(server.URL, "", true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "claude-test", Messages: []openai.Message{{Role: "user", Content: "weather"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{}},
	}, func(payload string) error { payloads = append(payloads, payload); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 3 || !strings.Contains(payloads[1], `"annotations":[{"type":"url_citation"`) {
		t.Fatalf("payloads=%v", payloads)
	}
	annotations := response.Choices[0].Message.Annotations
	if response.Usage.SearchRequests != 2 || response.Usage.TotalTokens != 7 || len(annotations) != 1 || annotations[0].URLCitation.StartIndex != 9 || annotations[0].URLCitation.EndIndex != 14 {
		t.Fatalf("response=%+v", response)
	}
}

func TestAnthropicResponses(t *testing.T) {
	var upstreamRequest anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(anthropicResponse{
			ID:         "msg-test",
			Type:       "message",
			Role:       "assistant",
			Model:      upstreamRequest.Model,
			StopReason: "end_turn",
			Content: []anthropicContent{
				{Type: "text", Text: "pong"},
			},
			Usage: anthropicUsage{InputTokens: 4, OutputTokens: 1, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
		})
	}))
	defer server.Close()

	maxOutputTokens := 12
	provider := NewAnthropic(server.URL, "test-key", false)
	response, err := provider.Responses(context.Background(), openai.ResponseRequest{
		Model:           "claude-test",
		Instructions:    "answer shortly",
		Input:           "ping",
		MaxOutputTokens: &maxOutputTokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstreamRequest.System != "answer shortly" || upstreamRequest.MaxTokens != 12 {
		t.Fatalf("unexpected upstream responses request: %+v", upstreamRequest)
	}
	if len(upstreamRequest.Messages) != 1 || upstreamRequest.Messages[0].Content != "ping" {
		t.Fatalf("unexpected upstream messages: %+v", upstreamRequest.Messages)
	}
	if response.OutputText != "pong" {
		t.Fatalf("unexpected output_text: %s", response.OutputText)
	}
	if response.Usage.InputTokens != 9 || response.Usage.TotalTokens != 10 || response.Usage.InputTokensDetails == nil || response.Usage.InputTokensDetails.CachedTokens != 3 || response.Usage.InputTokensDetails.CacheWriteTokens != 2 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}

func TestAnthropicStreamsResponses(t *testing.T) {
	var upstreamRequest anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg-test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":4,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"po"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ng"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer server.Close()

	var events []string
	var payloads []string
	provider := NewAnthropic(server.URL, "test-key", true)
	response, err := provider.StreamResponses(context.Background(), openai.ResponseRequest{
		Model:  "claude-test",
		Input:  "ping",
		Stream: true,
	}, func(event string, payload string) error {
		events = append(events, event)
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !upstreamRequest.Stream {
		t.Fatal("expected upstream stream to be enabled")
	}
	if len(payloads) != 4 {
		t.Fatalf("expected four responses payloads, got %d: %v", len(payloads), payloads)
	}
	if events[0] != "response.created" || events[1] != "response.output_text.delta" || events[3] != "response.completed" {
		t.Fatalf("unexpected events: %v", events)
	}
	if response.OutputText != "pong" || response.Usage.InputTokens != 9 || response.Usage.TotalTokens != 10 || response.Usage.InputTokensDetails == nil || response.Usage.InputTokensDetails.CachedTokens != 3 || response.Usage.InputTokensDetails.CacheWriteTokens != 2 {
		t.Fatalf("unexpected streamed output_text: %s", response.OutputText)
	}
}

func TestAnthropicTranslatesToolCalls(t *testing.T) {
	var upstream anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(anthropicResponse{
			ID: "msg-tool", Model: upstream.Model, StopReason: "tool_use",
			Content: []anthropicContent{{Type: "tool_use", ID: "toolu-1", Name: "weather", Input: map[string]any{"city": "Moscow"}}},
		})
	}))
	defer server.Close()

	response, err := NewAnthropic(server.URL, "key", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "claude-test", Messages: []openai.Message{{Role: "user", Content: "weather"}},
		Tools:      []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Description: "Get weather", Parameters: map[string]any{"type": "object"}}}},
		ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "weather"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream.Tools) != 1 || upstream.Tools[0].Name != "weather" || upstream.ToolChoice["type"] != "tool" || upstream.ToolChoice["name"] != "weather" {
		t.Fatalf("unexpected anthropic tool request: %+v", upstream)
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if response.Choices[0].FinishReason != "tool_calls" || call.ID != "toolu-1" || call.Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("unexpected translated tool call: %+v", response)
	}
}

func TestAnthropicUsesNativeOutputConfigAndMetadata(t *testing.T) {
	var upstream anthropicRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(anthropicResponse{
			ID: "msg-json", Model: upstream.Model, StopReason: "end_turn",
			Content: []anthropicContent{{Type: "text", Text: `{"ok":true}`}},
			Usage:   anthropicUsage{InputTokens: 4, OutputTokens: 5, OutputTokensDetails: &anthropicOutputTokenDetails{ThinkingTokens: 3}},
		})
	}))
	defer server.Close()

	strict := true
	response, err := NewAnthropic(server.URL, "key", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "claude-test", Messages: []openai.Message{{Role: "user", Content: "return json"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{Metadata: map[string]string{"user_id": "customer-42"}, ReasoningEffort: "high"},
		ResponseFormat:        &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "answer", Schema: map[string]any{"type": "object"}, Strict: &strict}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Metadata == nil || upstream.Metadata.UserID != "customer-42" || upstream.OutputConfig == nil || upstream.OutputConfig.Effort != "high" || upstream.OutputConfig.Format == nil || upstream.OutputConfig.Format.Type != "json_schema" || len(upstream.Tools) != 0 || upstream.ToolChoice != nil {
		t.Fatalf("native controls were not preserved: %+v", upstream)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != `{"ok":true}` || len(response.Choices[0].Message.ToolCalls) != 0 || response.Usage.CompletionTokensDetails == nil || response.Usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("structured response was not normalized: %+v", response)
	}
}

func TestAnthropicStreamsToolCallArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg-tool","model":"claude-test","usage":{"input_tokens":3}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu-1","name":"weather","input":{}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Moscow\"}"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4,"output_tokens_details":{"thinking_tokens":2}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer server.Close()

	var payloads []string
	response, err := NewAnthropic(server.URL, "key", true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "claude-test"}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.ID != "toolu-1" || call.Function.Name != "weather" || call.Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("unexpected streamed tool call: %+v", call)
	}
	if len(payloads) != 4 || !strings.Contains(payloads[0], `"tool_calls"`) || !strings.Contains(payloads[3], `"finish_reason":"tool_calls"`) {
		t.Fatalf("unexpected OpenAI tool stream: %v", payloads)
	}
	if response.Usage.CompletionTokensDetails == nil || response.Usage.CompletionTokensDetails.ReasoningTokens != 2 {
		t.Fatalf("stream usage details lost: %+v", response.Usage)
	}
}

func TestAnthropicMapsStopAndParallelControls(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("parallel=%v/stream=%v", parallel, streaming), func(t *testing.T) {
				var upstream anthropicRequest
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
						t.Error(err)
						return
					}
					if streaming {
						_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"test\",\"model\":\"test\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					} else {
						_, _ = fmt.Fprint(w, `{"id":"test","content":[],"stop_reason":"stop_sequence","stop_sequence":"END"}`)
					}
				}))
				defer server.Close()
				client := NewAnthropic(server.URL, "", true)
				request := openai.ChatCompletionRequest{Model: "test", Stop: []any{"\n", "END"}, ParallelToolCalls: &parallel, Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "lookup"}}}, ToolChoice: "required"}
				var err error
				if streaming {
					_, err = client.StreamChatCompletions(context.Background(), request, func(string) error { return nil })
				} else {
					_, err = client.ChatCompletions(context.Background(), request)
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(upstream.StopSequences) != 2 || upstream.StopSequences[0] != "\n" || upstream.StopSequences[1] != "END" || upstream.ToolChoice["disable_parallel_tool_use"] != !parallel || upstream.ToolChoice["type"] != "any" {
					t.Fatalf("native controls lost: %+v", upstream)
				}
				responseRequest := openai.ResponseRequest{Model: "test", ParallelToolCalls: &parallel, Tools: []openai.ResponseTool{{Type: "function", Name: "lookup"}}, ToolChoice: "required"}
				if streaming {
					_, err = client.StreamResponses(context.Background(), responseRequest, func(string, string) error { return nil })
				} else {
					_, err = client.Responses(context.Background(), responseRequest)
				}
				if err != nil {
					t.Fatal(err)
				}
				if upstream.ToolChoice["disable_parallel_tool_use"] != !parallel {
					t.Fatalf("Responses parallel control lost: %+v", upstream)
				}
			})
		}
	}
}

func TestAnthropicParallelControlWithoutTools(t *testing.T) {
	parallel := false
	request := anthropicChatRequest(openai.ChatCompletionRequest{ParallelToolCalls: &parallel}, false)
	if request.ToolChoice != nil {
		t.Fatal("parallel control invented tool choice")
	}
	request = anthropicChatRequest(openai.ChatCompletionRequest{ParallelToolCalls: &parallel, ResponseFormat: &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "answer", Schema: map[string]any{"type": "object"}}}}, false)
	if request.ToolChoice != nil || request.OutputConfig == nil || request.OutputConfig.Format == nil {
		t.Fatal("native structured output invented a tool choice")
	}
}

func TestAnthropicRejectsInvalidNativeMetadataAndUsageDetails(t *testing.T) {
	for _, request := range []openai.ChatCompletionRequest{
		{ChatGenerationOptions: openai.ChatGenerationOptions{Metadata: map[string]string{"other": "value"}}},
		{ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "minimal"}},
	} {
		if err := (Anthropic{}).ValidateChatParameters(request); err == nil {
			t.Fatalf("invalid controls accepted: %+v", request)
		}
	}
	for _, searches := range []int{-1, openai.WebSearchMaxUses + 1} {
		if err := validateAnthropicUsage(anthropicUsage{ServerToolUse: &anthropicServerToolUsage{WebSearchRequests: searches}}); err == nil {
			t.Fatalf("invalid web_search_requests=%d accepted", searches)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(anthropicResponse{ID: "bad", StopReason: "end_turn", Usage: anthropicUsage{OutputTokens: 2, OutputTokensDetails: &anthropicOutputTokenDetails{ThinkingTokens: 3}}})
	}))
	defer server.Close()
	if _, err := NewAnthropic(server.URL, "", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "model"}); err == nil {
		t.Fatal("invalid usage details accepted")
	}
}

func TestAnthropicRejectsMalformedSearchCitation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"bad-citation","stop_reason":"end_turn","content":[{"type":"text","text":"answer","citations":[{"type":"web_search_result_location","url":"javascript:alert(1)","title":"Unsafe","cited_text":"answer"}]}],"usage":{}}`)
	}))
	defer server.Close()
	if _, err := NewAnthropic(server.URL, "", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model"}); err == nil {
		t.Fatal("malformed search citation accepted")
	}
}
