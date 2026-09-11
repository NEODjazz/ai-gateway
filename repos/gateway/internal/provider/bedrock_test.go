package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func TestBedrockConverseMapsMessagesToolsAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/model/us.anthropic.claude-v1:0/converse" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.EscapedPath(), r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages := body["messages"].([]any)
		if len(messages) != 3 || len(body["system"].([]any)) != 1 || body["inferenceConfig"].(map[string]any)["maxTokens"] != float64(32) {
			t.Fatalf("request=%#v", body)
		}
		toolConfig := body["toolConfig"].(map[string]any)
		if len(toolConfig["tools"].([]any)) != 1 || toolConfig["toolChoice"].(map[string]any)["any"] == nil {
			t.Fatalf("tool config=%#v", toolConfig)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"checking "},{"toolUse":{"toolUseId":"call_2","name":"weather","input":{"city":"Paris"}}}]}},"stopReason":"tool_use","usage":{"inputTokens":9,"outputTokens":4,"totalTokens":13}}`)
	}))
	defer server.Close()
	maxTokens := 32
	client := NewBedrock(server.URL, "provider-key")
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "us.anthropic.claude-v1:0", MaxCompletionTokens: &maxTokens,
		Messages: []openai.Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "weather"},
			{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call_1", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Rome"}`}}}},
			{Role: "tool", ToolCallID: "call_1", Content: "sunny"},
		},
		Tools:      []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}}}},
		ToolChoice: "required",
	})
	if err != nil || response.Usage.TotalTokens != 13 || response.Choices[0].FinishReason != "tool_calls" || openai.ContentText(response.Choices[0].Message.Content) != "checking " || response.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockInvokeMapsAnthropicMessagesAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/model/us.anthropic.claude-v1:0/invoke" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.EscapedPath(), r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["anthropic_version"] != "bedrock-2023-05-31" || body["model"] != nil || body["max_tokens"] != float64(32) {
			t.Fatalf("request=%#v", body)
		}
		if len(body["messages"].([]any)) != 1 || len(body["tools"].([]any)) != 1 {
			t.Fatalf("request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"upstream","content":[{"type":"tool_use","id":"call_1","name":"weather","input":{"city":"Paris"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":7,"output_tokens":5}}`)
	}))
	defer server.Close()
	maxTokens := 32
	request := openai.ChatCompletionRequest{
		Model: "us.anthropic.claude-v1:0", BedrockInvoke: true, MaxTokens: &maxTokens,
		Messages: []openai.Message{{Role: "user", Content: "weather"}},
		Tools:    []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}}}},
	}
	response, err := NewBedrock(server.URL, "provider-key").ChatCompletions(t.Context(), request)
	if err != nil || response.Usage.TotalTokens != 12 || response.Choices[0].FinishReason != "tool_calls" || len(response.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockInvokeRejectsMalformedResponses(t *testing.T) {
	responses := []string{
		`{"id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":"end_turn"}`,
		`{"id":"msg_1","type":"message","role":"assistant","content":[],"stop_reason":"future_reason","usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"id":"msg_1","type":"message","role":"user","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"future_block"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"tool_use","name":"weather","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	for _, responseBody := range responses {
		t.Run(responseBody, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, responseBody) }))
			defer server.Close()
			maxTokens := 1
			_, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", BedrockInvoke: true, MaxTokens: &maxTokens, Messages: []openai.Message{{Role: "user", Content: "hello"}}})
			if err == nil {
				t.Fatal("malformed InvokeModel response accepted")
			}
		})
	}
}

func TestBedrockInvokeRejectsConverseOnlyControlsBeforeHTTP(t *testing.T) {
	client := NewBedrock("http://unused.invalid", "key")
	request := openai.ChatCompletionRequest{Model: "model", BedrockInvoke: true, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "priority"}, Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	if err := client.ValidateChatParameters(request); err == nil {
		t.Fatal("Converse-only control accepted by InvokeModel")
	}
}

func TestBedrockInvokeForwardsStructuredOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OutputConfig *anthropicOutputConfig `json:"output_config"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.OutputConfig == nil || body.OutputConfig.Format == nil || body.OutputConfig.Format.Type != "json_schema" || body.OutputConfig.Format.Schema == nil {
			t.Fatalf("request=%+v", body)
		}
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"model","content":[{"type":"text","text":"{\"answer\":\"ok\"}"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`)
	}))
	defer server.Close()
	strict := true
	maxTokens := 8
	request := openai.ChatCompletionRequest{
		Model: "model", BedrockInvoke: true, MaxTokens: &maxTokens, Messages: []openai.Message{{Role: "user", Content: "answer"}},
		ResponseFormat: &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "answer", Strict: &strict, Schema: map[string]any{"type": "object"}}},
	}
	response, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request)
	if err != nil || openai.ContentText(response.Choices[0].Message.Content) != `{"answer":"ok"}` || response.Usage.TotalTokens != 5 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConversePreservesReasoningHistoryAndResponse(t *testing.T) {
	first, third := 0, 2
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "answer", Reasoning: []openai.ReasoningBlock{
			{Index: &first, Type: "thinking", Thinking: "private plan", Signature: "signed"},
			{Index: &third, Type: "redacted_thinking", Data: "b3BhcXVl"},
		}},
	}}
	converted, err := bedrockChatRequest(request)
	if err != nil || len(converted.Messages[1].Content) != 3 || converted.Messages[1].Content[0].ReasoningContent.ReasoningText.Signature != "signed" || converted.Messages[1].Content[2].ReasoningContent.RedactedContent != "b3BhcXVl" {
		t.Fatalf("request=%+v err=%v", converted, err)
	}
	var upstream bedrockResponse
	upstream.Output.Message = bedrockMessage{Role: "assistant", Content: converted.Messages[1].Content}
	upstream.StopReason = "end_turn"
	upstream.Usage = &struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	}{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
	response, err := bedrockToChat(upstream, "model")
	if err != nil || len(response.Choices[0].Message.Reasoning) != 2 || *response.Choices[0].Message.Reasoning[0].Index != 0 || response.Choices[0].Message.Reasoning[1].Data != "b3BhcXVl" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockAllowsUnsignedReasoningWithoutWeakeningOtherAdapters(t *testing.T) {
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{
		{Role: "user", Content: "question"},
		{Role: "assistant", Reasoning: []openai.ReasoningBlock{{Type: "thinking", Thinking: "plan"}}},
	}}
	bedrock := NewBedrock("http://unused.invalid", "key")
	if err := validateChatAdapter(bedrock, request); err != nil {
		t.Fatalf("Bedrock rejected optional reasoning signature: %v", err)
	}
	anthropic := NewAnthropic("http://unused.invalid", "key", false)
	if err := validateChatAdapter(anthropic, request); err == nil {
		t.Fatal("signed reasoning policy was weakened for Anthropic")
	}
}

func TestBedrockConverseMapsNamedToolChoice(t *testing.T) {
	request := openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "weather"}},
		Tools:      []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather"}}},
		ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "weather"}},
	}
	converted, err := bedrockChatRequest(request)
	if err != nil || converted.ToolConfig == nil || converted.ToolConfig.ToolChoice == nil || converted.ToolConfig.ToolChoice.Tool == nil || converted.ToolConfig.ToolChoice.Tool.Name != "weather" {
		t.Fatalf("request=%+v err=%v", converted, err)
	}
	request.ToolChoice = "none"
	if _, err := bedrockChatRequest(request); err == nil {
		t.Fatal("unsupported none tool choice accepted")
	}
	request.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": "missing"}}
	if _, err := bedrockChatRequest(request); err == nil {
		t.Fatal("unknown named tool accepted")
	}
}

func TestBedrockConverseForwardsServiceTier(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ServiceTier       *bedrockServiceTier       `json:"serviceTier"`
			PerformanceConfig *bedrockPerformanceConfig `json:"performanceConfig"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.ServiceTier == nil || body.ServiceTier.Type != "reserved" || body.PerformanceConfig == nil || body.PerformanceConfig.Latency != "optimized" {
			t.Fatalf("native controls lost: %+v %+v", body.ServiceTier, body.PerformanceConfig)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, BedrockServiceTier: "reserved", BedrockPerformanceLatency: "optimized"}
	if _, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.BedrockPerformanceLatency = "fastest"
	if _, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request); err == nil {
		t.Fatal("unsupported service tier accepted")
	}
}

func TestBedrockConverseForwardsRequestMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RequestMetadata map[string]string `json:"requestMetadata"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.RequestMetadata["tenant:id"] != "customer-42" {
			t.Fatalf("request metadata lost: %#v", body.RequestMetadata)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, BedrockRequestMetadata: map[string]string{"tenant:id": "customer-42"}}
	if _, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.BedrockRequestMetadata = map[string]string{"bad!key": "value"}
	if _, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request); err == nil {
		t.Fatal("invalid direct request metadata accepted")
	}
}

func TestBedrockConverseForwardsAdditionalModelRequestFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Fields map[string]any `json:"additionalModelRequestFields"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Fields["top_k"] != float64(42) {
			t.Fatalf("additional model fields lost: %#v", body.Fields)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, BedrockAdditionalModelRequestFields: json.RawMessage(`{"top_k":42}`)}
	if _, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.BedrockAdditionalModelRequestFields = json.RawMessage(`null`)
	if _, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request); err == nil {
		t.Fatal("invalid direct additional model fields accepted")
	}
}

func TestBedrockConverseForwardsGuardrailAndPreservesTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Guardrail *openai.BedrockGuardrailConfig `json:"guardrailConfig"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Guardrail == nil || body.Guardrail.GuardrailIdentifier != "guardrail123" || body.Guardrail.GuardrailVersion != "2" || body.Guardrail.Trace != "enabled" {
			t.Fatalf("guardrail config lost: %+v", body.Guardrail)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"blocked"}]}},"stopReason":"guardrail_intervened","trace":{"guardrail":{"actionReason":"policy"}},"usage":{"inputTokens":3,"outputTokens":1,"totalTokens":4}}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, BedrockGuardrailConfig: &openai.BedrockGuardrailConfig{GuardrailIdentifier: "guardrail123", GuardrailVersion: "2", Trace: "enabled"}}
	chat, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request)
	if err != nil || chat.Choices[0].FinishReason != "content_filter" || len(chat.Choices[0].Message.NativeContent) != 2 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	native, err := openai.BedrockFromChat(chat)
	if err != nil || !strings.Contains(string(native.Trace), `"actionReason":"policy"`) || native.StopReason != "guardrail_intervened" {
		t.Fatalf("native=%+v err=%v", native, err)
	}
}

func TestBedrockConverseRejectsUnrequestedGuardrailTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","trace":{"guardrail":{}},"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	if _, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request); err == nil {
		t.Fatal("unrequested guardrail trace accepted")
	}
}

func TestBedrockConversePreservesRequestedAdditionalResponseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Paths []string `json:"additionalModelResponseFieldPaths"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Paths) != 1 || body.Paths[0] != "/stop_sequence" {
			t.Fatalf("paths=%v", body.Paths)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"additionalModelResponseFields":{"stop_sequence":"DONE","nested":{"value":"scan me"}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, BedrockAdditionalModelResponseFieldPaths: []string{"/stop_sequence"}}
	chat, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request)
	if err != nil || len(chat.Choices[0].Message.NativeContent) != 1 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	native, err := openai.BedrockFromChat(chat)
	if err != nil || !strings.Contains(string(native.AdditionalModelResponseFields), `"stop_sequence":"DONE"`) || native.Usage.TotalTokens != 2 {
		t.Fatalf("native=%+v err=%v", native, err)
	}
}

func TestBedrockConverseForwardsStructuredOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OutputConfig *bedrockOutputConfig `json:"outputConfig"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.OutputConfig == nil {
			t.Fatal("outputConfig missing")
		}
		format := body.OutputConfig.TextFormat
		var schema map[string]any
		if format.Type != "json_schema" || format.Structure.JSONSchema.Name != "answer" || json.Unmarshal([]byte(format.Structure.JSONSchema.Schema), &schema) != nil || schema["type"] != "object" {
			t.Fatalf("outputConfig=%+v", body.OutputConfig)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"{\"value\":\"ok\"}"}]}},"stopReason":"end_turn","usage":{"inputTokens":2,"outputTokens":3,"totalTokens":5}}`)
	}))
	defer server.Close()
	strict := true
	request := openai.ChatCompletionRequest{
		Model: "model", Messages: []openai.Message{{Role: "user", Content: "extract"}},
		ResponseFormat: &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "answer", Schema: map[string]any{"type": "object"}, Strict: &strict}},
	}
	response, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request)
	if err != nil || openai.ContentText(response.Choices[0].Message.Content) != `{"value":"ok"}` || response.Usage.TotalTokens != 5 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	strict = false
	if _, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request); err == nil {
		t.Fatal("non-strict response format was silently strengthened")
	}
}

func TestBedrockRejectsUnrequestedAdditionalResponseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"additionalModelResponseFields":{"secret":"unexpected"},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	if _, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request); err == nil {
		t.Fatal("unrequested additional response fields accepted")
	}
}

func TestBedrockNativeControlsFailClosedOnOtherAdapters(t *testing.T) {
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, BedrockPerformanceLatency: "optimized"}
	if err := validateChatAdapter(Demo{}, request); err == nil {
		t.Fatal("Bedrock native control was dropped by another adapter")
	}
	request.BedrockPerformanceLatency = ""
	request.BedrockAdditionalModelResponseFieldPaths = []string{"/stop_sequence"}
	if err := validateChatAdapter(Demo{}, request); err == nil {
		t.Fatal("Bedrock additional response paths were dropped by another adapter")
	}
	request.BedrockAdditionalModelResponseFieldPaths = nil
	request.BedrockRequestMetadata = map[string]string{"tenant": "customer"}
	if err := validateChatAdapter(Demo{}, request); err == nil {
		t.Fatal("Bedrock request metadata was dropped by another adapter")
	}
	request.BedrockRequestMetadata = nil
	request.BedrockAdditionalModelRequestFields = json.RawMessage(`{"top_k":42}`)
	if err := validateChatAdapter(Demo{}, request); err == nil {
		t.Fatal("Bedrock additional model request fields were dropped by another adapter")
	}
	request.BedrockAdditionalModelRequestFields = nil
	request.BedrockGuardrailConfig = &openai.BedrockGuardrailConfig{GuardrailIdentifier: "guardrail123", GuardrailVersion: "1"}
	if err := validateChatAdapter(Demo{}, request); err == nil {
		t.Fatal("Bedrock guardrail config was dropped by another adapter")
	}
}

func TestBedrockConverseForwardsUserImageInOrder(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []bedrockMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		content := body.Messages[0].Content
		if len(content) != 3 || content[0].Text != "before" || content[1].Image == nil || content[1].Image.Format != "png" || content[1].Image.Source.Bytes != data || content[2].Text != "after" {
			t.Fatalf("content=%+v", content)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":5,"outputTokens":1,"totalTokens":6}}`)
	}))
	defer server.Close()
	content := []any{
		map[string]any{"type": "text", "text": "before"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + data}},
		map[string]any{"type": "text", "text": "after"},
	}
	response, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: content}}})
	if err != nil || response.Usage.TotalTokens != 6 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseForwardsValidatedNativeDocument(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-test"))
	prompt := "summarize"
	after := "after"
	request, err := (openai.BedrockConverseRequest{Messages: []openai.BedrockMessage{{Role: "user", Content: []openai.BedrockContentBlock{
		{Text: &prompt},
		{Document: &openai.BedrockDocument{Format: "pdf", Name: "Report", Source: openai.BedrockDocumentSource{Bytes: data}}},
		{Text: &after},
	}}}}).ChatRequest("model", "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []bedrockMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		content := body.Messages[0].Content
		if len(content) != 3 || content[0].Text != "summarize" || content[1].Document == nil || content[1].Document.Name != "Report" || content[1].Document.Source.Bytes != data || content[2].Text != "after" {
			t.Fatalf("content=%+v", content)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":5,"outputTokens":1,"totalTokens":6}}`)
	}))
	defer server.Close()
	response, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request)
	if err != nil || response.Usage.TotalTokens != 6 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseUsesSigV4TemporaryCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKID/20260102/us-east-1/bedrock/aws4_request") || r.Header.Get("X-Amz-Security-Token") != "session" || r.Header.Get("X-Amz-Date") != "20260102T030405Z" || r.Header.Get("X-Amz-Content-Sha256") == "" {
			t.Fatalf("headers=%v", r.Header)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	credential := `{"access_key_id":"AKID","secret_access_key":"secret","session_token":"session"}`
	client := NewBedrockWithAuth(server.URL, credential, " AWS_SIGV4 ", "us-east-1")
	client.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || response.Usage.TotalTokens != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseUsesAmbientEnvironmentCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ENVKEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "environment-secret")
	t.Setenv("AWS_SESSION_TOKEN", "environment-session")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=ENVKEY/") || r.Header.Get("X-Amz-Security-Token") != "environment-session" {
			t.Fatalf("headers=%v", r.Header)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	client := NewBedrockWithAuth(server.URL, "", "aws_sigv4", "us-east-1")
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || response.Usage.TotalTokens != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseFailsClosedWithoutAmbientCredentials(t *testing.T) {
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI"} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewBedrockWithAuth(server.URL, "", "aws_sigv4", "us-east-1")
	_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	var failure *Error
	if !errors.As(err, &failure) || failure.StatusCode != http.StatusServiceUnavailable || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestBedrockRejectsUnrepresentableParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewBedrock(server.URL, "key")
	seed := int64(1)
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Seed: &seed}
	_, err := client.ChatCompletions(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "seed") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestBedrockRejectsInconsistentUsage(t *testing.T) {
	response := bedrockResponse{StopReason: "end_turn"}
	response.Output.Message.Content = []bedrockContentBlock{{Text: "hello"}}
	response.Usage = &struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	}{InputTokens: 2, OutputTokens: 3, TotalTokens: 4}
	if _, err := bedrockToChat(response, "model"); err == nil {
		t.Fatal("inconsistent usage accepted")
	}
}

func TestBedrockRejectsUnsupportedOutputImage(t *testing.T) {
	response := bedrockResponse{StopReason: "end_turn"}
	image := bedrockImage{Format: "png"}
	image.Source.Bytes = "data"
	response.Output.Message.Content = []bedrockContentBlock{{Image: &image}}
	response.Usage = &struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	}{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}
	if _, err := bedrockToChat(response, "model"); err == nil {
		t.Fatal("unsupported output image was silently discarded")
	}
}

func TestBedrockConversePreservesCitations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"До: "},{"citationsContent":{"content":[{"text":"ответ"}],"citations":[{"title":"Report","source":"upload","sourceContent":[{"text":"источник"}],"location":{"documentPage":{"documentIndex":1,"start":3,"end":4}}},{"title":"Web","location":{"web":{"domain":"example.com","url":"https://example.com/source"}}}]}}]}},"stopReason":"end_turn","usage":{"inputTokens":2,"outputTokens":3,"totalTokens":5}}`)
	}))
	defer server.Close()

	response, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}}})
	if err != nil {
		t.Fatal(err)
	}
	message := response.Choices[0].Message
	if openai.ContentText(message.Content) != "До: ответ" || len(message.Annotations) != 2 {
		t.Fatalf("message=%+v", message)
	}
	source := message.Annotations[0].SourceCitation
	web := message.Annotations[1].URLCitation
	if source == nil || source.StartIndex != 4 || source.EndIndex != 9 || source.LocationType != "document_page" || source.DocumentIndex == nil || *source.DocumentIndex != 1 || len(source.SourceContent) != 1 || source.SourceContent[0] != "источник" {
		t.Fatalf("source citation=%+v", source)
	}
	if web == nil || web.StartIndex != 4 || web.EndIndex != 9 || web.URL != "https://example.com/source" {
		t.Fatalf("web citation=%+v", web)
	}

	native, err := openai.BedrockFromChat(response)
	if err != nil || len(native.Output.Message.Content) != 1 || native.Output.Message.Content[0].CitationsContent == nil || len(native.Output.Message.Content[0].CitationsContent.Citations) != 2 {
		t.Fatalf("native=%+v err=%v", native, err)
	}
}

func TestBedrockRejectsMalformedCitationLocations(t *testing.T) {
	zero, one := 0, 1
	validUsage := &struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	}{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	for name, location := range map[string]bedrockCitationLocation{
		"missing": {},
		"union": {
			DocumentPage: &bedrockDocumentLocation{DocumentIndex: &zero, Start: &zero, End: &one},
			Web:          &bedrockWebLocation{URL: "https://example.com"},
		},
		"incomplete": {DocumentChar: &bedrockDocumentLocation{DocumentIndex: &zero, Start: &zero}},
	} {
		t.Run(name, func(t *testing.T) {
			text := "answer"
			response := bedrockResponse{StopReason: "end_turn", Usage: validUsage}
			response.Output.Message.Content = []bedrockContentBlock{{CitationsContent: &bedrockCitationsContent{
				Content: []bedrockCitationText{{Text: &text}}, Citations: []bedrockCitation{{Title: "Source", Location: location}},
			}}}
			if _, err := bedrockToChat(response, "model"); err == nil {
				t.Fatal("malformed citation location accepted")
			}
		})
	}
}

func TestBedrockMapsEverySourceCitationLocation(t *testing.T) {
	zero, one, two := 0, 1, 2
	tests := map[string]struct {
		location bedrockCitationLocation
		kind     string
	}{
		"character": {location: bedrockCitationLocation{DocumentChar: &bedrockDocumentLocation{DocumentIndex: &zero, Start: &one, End: &two}}, kind: "document_char"},
		"chunk":     {location: bedrockCitationLocation{DocumentChunk: &bedrockDocumentLocation{DocumentIndex: &zero, Start: &one, End: &two}}, kind: "document_chunk"},
		"page":      {location: bedrockCitationLocation{DocumentPage: &bedrockDocumentLocation{DocumentIndex: &zero, Start: &one, End: &two}}, kind: "document_page"},
		"search":    {location: bedrockCitationLocation{SearchResultLocation: &bedrockSearchResultLocation{SearchResultIndex: &zero, Start: &one, End: &two}}, kind: "search_result"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			annotation, err := bedrockCitationAnnotation(bedrockCitation{Title: "Source", Location: test.location}, 3, 8)
			if err != nil || annotation.SourceCitation == nil || annotation.SourceCitation.LocationType != test.kind || annotation.SourceCitation.LocationStart != 1 || annotation.SourceCitation.LocationEnd != 2 {
				t.Fatalf("annotation=%+v err=%v", annotation, err)
			}
			if err := openai.ValidateChatAnnotations([]openai.ChatAnnotation{annotation}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBedrockMapsDocumentedStopReasons(t *testing.T) {
	for reason, expectedFinish := range map[string]string{
		"guardrail_intervened":          "content_filter",
		"model_context_window_exceeded": "length",
	} {
		t.Run(reason, func(t *testing.T) {
			response := bedrockResponse{StopReason: reason}
			response.Output.Message.Content = []bedrockContentBlock{{Text: "partial"}}
			response.Usage = &struct {
				InputTokens  int `json:"inputTokens"`
				OutputTokens int `json:"outputTokens"`
				TotalTokens  int `json:"totalTokens"`
			}{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}
			chat, err := bedrockToChat(response, "model")
			if err != nil || chat.Choices[0].FinishReason != expectedFinish || len(chat.Choices[0].Message.NativeContent) != 1 {
				t.Fatalf("chat=%+v err=%v", chat, err)
			}
			native, err := openai.BedrockFromChat(chat)
			if err != nil || native.StopReason != reason {
				t.Fatalf("native=%+v err=%v", native, err)
			}
		})
	}
}

func TestBedrockPreservesMalformedOutputStopReasonsAndUsage(t *testing.T) {
	for _, reason := range []string{"malformed_model_output", "malformed_tool_use"} {
		t.Run(reason, func(t *testing.T) {
			response := bedrockResponse{StopReason: reason}
			response.Output.Message.Content = []bedrockContentBlock{{Text: "partial"}}
			response.Usage = &struct {
				InputTokens  int `json:"inputTokens"`
				OutputTokens int `json:"outputTokens"`
				TotalTokens  int `json:"totalTokens"`
			}{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}
			chat, err := bedrockToChat(response, "model")
			if err != nil || chat.Usage.TotalTokens != 3 || chat.Choices[0].FinishReason != "error" {
				t.Fatalf("chat=%+v err=%v", chat, err)
			}
			native, err := openai.BedrockFromChat(chat)
			if err != nil || native.StopReason != reason || native.Usage.TotalTokens != 3 {
				t.Fatalf("native=%+v err=%v", native, err)
			}
		})
	}
}
