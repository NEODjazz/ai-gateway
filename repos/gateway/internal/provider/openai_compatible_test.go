package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestProviderURLDoesNotDuplicateV1(t *testing.T) {
	got := providerURL("https://example.test/openai/v1", "chat/completions")
	want := "https://example.test/openai/v1/chat/completions"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestOpenAICompatibleForwardsBackgroundResponses(t *testing.T) {
	var upstream map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.ResponseResponse{ID: "resp_background", Model: "m", Status: "queued"})
	}))
	defer server.Close()
	store := true
	response, err := NewOpenAICompatible(server.URL, "", false).Responses(t.Context(), openai.ResponseRequest{Model: "m", Input: "hello", Store: &store, Background: true})
	if err != nil || response.Status != "queued" || string(upstream["background"]) != "true" || string(upstream["store"]) != "true" {
		t.Fatalf("response=%+v upstream=%s err=%v", response, upstream, err)
	}
}

func TestOpenAICompatiblePreservesWebSearchAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"resp-web","object":"response","status":"completed","model":"model","output":[{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","queries":["latest news"],"sources":[{"type":"url","url":"https://example.com/news"}]}}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL, "", false).Responses(t.Context(), openai.ResponseRequest{Model: "model", Input: "latest news", Tools: []openai.ResponseTool{{Type: "web_search"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 || response.Output[0].Type != "web_search_call" || !json.Valid(response.Output[0].Action) || !strings.Contains(string(response.Output[0].Action), `"https://example.com/news"`) {
		t.Fatalf("web search action was not preserved: %+v", response.Output)
	}
}

func TestOpenAICompatiblePreservesHostedToolOutputPayloads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"resp-tools","object":"response","status":"completed","model":"model","output":[{"id":"ci_1","type":"code_interpreter_call","status":"completed","container_id":"cntr_1","code":"print(1)","outputs":[{"type":"logs","logs":"1"}]},{"id":"fs_1","type":"file_search_call","status":"completed","results":[{"file_id":"file_1","filename":"facts.txt","score":0.9,"text":"fact"}]},{"id":"mcp_1","type":"mcp_call","status":"completed","name":"lookup","server_label":"documents","approval_request_id":"approval_1","arguments":"{}","output":"found","error":null},{"id":"mcpl_1","type":"mcp_list_tools","server_label":"documents","tools":[{"name":"lookup","description":"Lookup","input_schema":{"type":"object"}}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL, "", false).Responses(t.Context(), openai.ResponseRequest{Model: "model", Input: "run tools"})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 4 || response.Output[0].ContainerID != "cntr_1" || response.Output[0].Code != "print(1)" || len(response.Output[0].Outputs) != 1 || !strings.Contains(string(response.Output[0].Outputs[0]), `"logs":"1"`) || len(response.Output[1].Results) != 1 || !strings.Contains(string(response.Output[1].Results[0]), `"file_id":"file_1"`) || response.Output[2].ServerLabel != "documents" || response.Output[2].ApprovalRequestID != "approval_1" || string(response.Output[2].Output) != `"found"` || string(response.Output[2].Error) != "null" || len(response.Output[3].Tools) != 1 {
		t.Fatalf("hosted tool payloads were not preserved: %+v", response.Output)
	}
	encoded, err := json.Marshal(response)
	if err != nil || !strings.Contains(string(encoded), `"results":[{"file_id":"file_1"`) || !strings.Contains(string(encoded), `"output":"found"`) {
		t.Fatalf("hosted tool payloads were not returned to the client: %s err=%v", encoded, err)
	}
}

func TestOpenAICompatibleForwardsCustomToolsAndPreservesCalls(t *testing.T) {
	var upstream map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprint(w, `{"id":"resp-custom","object":"response","status":"completed","model":"model","output":[{"id":"ct_1","type":"custom_tool_call","status":"completed","call_id":"call_1","name":"query","input":"status:open"}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`)
	}))
	defer server.Close()

	request := openai.ResponseRequest{Model: "model", Input: "find open items", Tools: []openai.ResponseTool{{Type: "custom", Name: "query", Format: &openai.ResponseCustomToolFormat{Type: "grammar", Syntax: "regex", Definition: `status:(open|closed)`}}}, ToolChoice: map[string]any{"type": "custom", "name": "query"}}
	response, err := NewOpenAICompatible(server.URL, "", false).Responses(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(upstream["tools"]), `"type":"custom"`) || !strings.Contains(string(upstream["tools"]), `"syntax":"regex"`) || !strings.Contains(string(upstream["tool_choice"]), `"name":"query"`) {
		t.Fatalf("custom tool contract was not forwarded: %s", upstream)
	}
	if len(response.Output) != 1 || response.Output[0].Type != "custom_tool_call" || response.Output[0].Name != "query" || response.Output[0].CallID != "call_1" || response.Output[0].Input != "status:open" {
		t.Fatalf("custom tool call was not preserved: %+v", response.Output)
	}
}

func TestOpenAICompatibleForwardsComputerLoopAndPreservesActions(t *testing.T) {
	var upstream map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprint(w, `{"id":"resp-computer","object":"response","status":"completed","model":"model","output":[{"id":"computer_1","type":"computer_call","status":"completed","call_id":"call_2","actions":[{"type":"click","button":"left","x":12,"y":34},{"type":"screenshot"}],"pending_safety_checks":[{"id":"check_1","code":"domain"}]}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}`)
	}))
	defer server.Close()

	input := []any{map[string]any{
		"type": "computer_call_output", "call_id": "call_1",
		"output": map[string]any{"type": "computer_screenshot", "image_url": "data:image/png;base64,iVBORw0KGgo="},
	}}
	request := openai.ResponseRequest{Model: "model", PreviousResponse: "resp_previous", Input: input, Tools: []openai.ResponseTool{{Type: "computer"}}, ToolChoice: map[string]any{"type": "computer"}}
	response, err := NewOpenAICompatible(server.URL, "", false).Responses(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if string(upstream["tools"]) != `[{"type":"computer"}]` || !strings.Contains(string(upstream["input"]), `"computer_call_output"`) || string(upstream["tool_choice"]) != `{"type":"computer"}` {
		t.Fatalf("computer loop was not forwarded: %s", upstream)
	}
	if len(response.Output) != 1 || response.Output[0].CallID != "call_2" || len(response.Output[0].Actions) != 2 || len(response.Output[0].PendingSafetyChecks) != 1 {
		t.Fatalf("computer output was not preserved: %+v", response.Output)
	}
}

func TestOpenAICompatibleStreamsComputerActions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.created\n"+`data: {"type":"response.created","response":{"id":"resp-computer","object":"response","status":"in_progress","model":"model","output":[]}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_item.done\n"+`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"computer_1","type":"computer_call","status":"completed","call_id":"call_1","actions":[{"type":"move","x":4,"y":5}],"pending_safety_checks":[]}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\n"+`data: {"type":"response.completed","response":{"id":"resp-computer","object":"response","status":"completed","model":"model","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL, "", true).StreamResponses(t.Context(), openai.ResponseRequest{Model: "model", Input: "open", Stream: true, Tools: []openai.ResponseTool{{Type: "computer"}}}, func(string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 || response.Output[0].CallID != "call_1" || len(response.Output[0].Actions) != 1 {
		t.Fatalf("streamed computer output was not preserved: %+v", response.Output)
	}
}

func TestOpenAICompatibleForwardsResponseImageGenerationAndPreservesResult(t *testing.T) {
	var upstream map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprint(w, `{"id":"resp-image","object":"response","status":"completed","model":"model","output":[{"id":"ig_1","type":"image_generation_call","status":"completed","result":"aW1hZ2U="}],"usage":{"input_tokens":4,"output_tokens":8,"total_tokens":12}}`)
	}))
	defer server.Close()

	compression, partialImages := 80, 2
	request := openai.ResponseRequest{Model: "model", Input: "draw a lighthouse", Tools: []openai.ResponseTool{{Type: "image_generation", Action: "generate", Background: "transparent", InputFidelity: "high", Model: "gpt-image", Moderation: "low", OutputCompression: &compression, OutputFormat: "webp", PartialImages: &partialImages, Quality: "high", Size: "1024x1536"}}, ToolChoice: map[string]any{"type": "image_generation"}}
	response, err := NewOpenAICompatible(server.URL, "", false).Responses(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	tools := string(upstream["tools"])
	if !strings.Contains(tools, `"type":"image_generation"`) || !strings.Contains(tools, `"background":"transparent"`) || !strings.Contains(tools, `"output_compression":80`) || !strings.Contains(tools, `"partial_images":2`) || string(upstream["tool_choice"]) != `{"type":"image_generation"}` {
		t.Fatalf("image generation contract was not forwarded: %s", upstream)
	}
	if len(response.Output) != 1 || response.Output[0].Type != "image_generation_call" || string(response.Output[0].Result) != `"aW1hZ2U="` {
		t.Fatalf("image generation result was not preserved: %+v", response.Output)
	}
}

func TestOpenAICompatibleStreamsResponseImageGenerationEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.created\n"+`data: {"type":"response.created","response":{"id":"resp-image","object":"response","status":"in_progress","model":"model","output":[]}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: response.image_generation_call.in_progress\n"+`data: {"type":"response.image_generation_call.in_progress","output_index":0,"item_id":"ig_1","sequence_number":1}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: response.image_generation_call.generating\n"+`data: {"type":"response.image_generation_call.generating","output_index":0,"item_id":"ig_1","sequence_number":2}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: response.image_generation_call.partial_image\n"+`data: {"type":"response.image_generation_call.partial_image","output_index":0,"item_id":"ig_1","sequence_number":3,"partial_image_index":0,"partial_image_b64":"cGFydGlhbA=="}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_item.done\n"+`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ig_1","type":"image_generation_call","status":"completed","result":"ZmluYWw="}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: response.image_generation_call.completed\n"+`data: {"type":"response.image_generation_call.completed","output_index":0,"item_id":"ig_1","sequence_number":4}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\n"+`data: {"type":"response.completed","response":{"id":"resp-image","object":"response","status":"completed","model":"model","usage":{"input_tokens":2,"output_tokens":4,"total_tokens":6}}}`+"\n\n")
	}))
	defer server.Close()

	var events, payloads []string
	response, err := NewOpenAICompatible(server.URL, "", true).StreamResponses(t.Context(), openai.ResponseRequest{Model: "model", Input: "draw", Stream: true, Tools: []openai.ResponseTool{{Type: "image_generation"}}}, func(event, payload string) error {
		events, payloads = append(events, event), append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 7 || events[3] != "response.image_generation_call.partial_image" || !strings.Contains(payloads[3], `"partial_image_b64":"cGFydGlhbA=="`) {
		t.Fatalf("image generation stream events were not preserved: events=%v payloads=%v", events, payloads)
	}
	if len(response.Output) != 1 || response.Output[0].ID != "ig_1" || string(response.Output[0].Result) != `"ZmluYWw="` || response.Usage.TotalTokens != 6 {
		t.Fatalf("streamed image generation result was not collected: %+v", response)
	}
}

func TestOpenAICompatibleStreamsHostedToolOutputPayloads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.created\n"+`data: {"type":"response.created","response":{"id":"resp-tools","object":"response","status":"in_progress","model":"model","output":[]}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: response.output_item.done\n"+`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"ci_1","type":"code_interpreter_call","status":"completed","container_id":"cntr_1","code":"print(1)","outputs":[{"type":"logs","logs":"1"}]}}`+"\n\n")
		_, _ = fmt.Fprint(w, "event: response.completed\n"+`data: {"type":"response.completed","response":{"id":"resp-tools","object":"response","status":"completed","model":"model","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL, "", true).StreamResponses(t.Context(), openai.ResponseRequest{Model: "model", Input: "run tools", Stream: true}, func(string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 || response.Output[0].ContainerID != "cntr_1" || len(response.Output[0].Outputs) != 1 || !strings.Contains(string(response.Output[0].Outputs[0]), `"logs":"1"`) {
		t.Fatalf("streamed hosted tool payload was not preserved: %+v", response.Output)
	}
}

func TestOpenAICompatibleForwardsMaxCompletionTokensWithoutLegacyParameters(t *testing.T) {
	var upstream map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "chat-modern", Model: "gpt-modern"})
	}))
	defer server.Close()

	maxCompletionTokens := 256
	_, err := NewOpenAICompatible(server.URL, "provider-key", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "gpt-modern", Messages: []openai.Message{{Role: "user", Content: "hello"}}, MaxCompletionTokens: &maxCompletionTokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(upstream["max_completion_tokens"]) != "256" {
		t.Fatalf("max_completion_tokens was not forwarded: %s", upstream["max_completion_tokens"])
	}
	if _, found := upstream["max_tokens"]; found {
		t.Fatal("legacy max_tokens must be omitted")
	}
	if _, found := upstream["temperature"]; found {
		t.Fatal("unset temperature must be omitted")
	}
}

func TestOpenAICompatiblePreservesChatCacheTokenDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"chat-cache",
			"object":"chat.completion",
			"model":"cached-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":21,
				"completion_tokens":3,
				"total_tokens":24,
				"prompt_tokens_details":{"cached_tokens":13,"cache_write_tokens":5,"audio_tokens":2,"image_tokens":3,"text_tokens":16},
				"completion_tokens_details":{"accepted_prediction_tokens":1,"audio_tokens":2,"reasoning_tokens":3,"rejected_prediction_tokens":4,"text_tokens":5}
			}
		}`))
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "cached-model"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage.PromptTokensDetails == nil {
		t.Fatal("expected prompt token details")
	}
	if response.Usage.PromptTokensDetails.CachedTokens != 13 || response.Usage.PromptTokensDetails.CacheWriteTokens != 5 {
		t.Fatalf("unexpected cache token details: %+v", response.Usage.PromptTokensDetails)
	}
	if response.Usage.PromptTokensDetails.AudioTokens != 2 || response.Usage.PromptTokensDetails.ImageTokens != 3 || response.Usage.PromptTokensDetails.TextTokens != 16 || response.Usage.CompletionTokensDetails == nil || response.Usage.CompletionTokensDetails.AcceptedPredictionTokens != 1 || response.Usage.CompletionTokensDetails.RejectedPredictionTokens != 4 || response.Usage.CompletionTokensDetails.TextTokens != 5 {
		t.Fatalf("completion modality details lost: %+v", response.Usage)
	}
}

func TestOpenAICompatiblePreservesReasoningContent(t *testing.T) {
	var upstream openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"chat-reasoning","object":"chat.completion","model":"reasoning-model","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"new plan","content":"answer"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "reasoning-model",
		Messages: []openai.Message{
			{Role: "user", Content: "question"},
			{Role: "assistant", ReasoningContent: "prior plan", Content: "prior answer"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream.Messages) != 2 || upstream.Messages[1].ReasoningContent != "prior plan" {
		t.Fatalf("request reasoning_content was not preserved: %+v", upstream.Messages)
	}
	if got := response.Choices[0].Message.ReasoningContent; got != "new plan" {
		t.Fatalf("response reasoning_content=%q", got)
	}
}

func TestOpenAICompatiblePreservesResponseCacheTokenDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"resp-cache",
			"object":"response",
			"status":"completed",
			"model":"cached-model",
			"output":[],
			"usage":{
				"input_tokens":21,
				"output_tokens":3,
				"total_tokens":24,
				"input_tokens_details":{"cached_tokens":13,"cache_creation_tokens":5,"audio_tokens":2,"image_tokens":3,"text_tokens":16},
				"output_tokens_details":{"accepted_prediction_tokens":1,"audio_tokens":2,"reasoning_tokens":3,"rejected_prediction_tokens":4,"text_tokens":5}
			}
		}`))
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL, "", false).Responses(context.Background(), openai.ResponseRequest{Model: "cached-model"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage.InputTokensDetails == nil {
		t.Fatal("expected input token details")
	}
	if response.Usage.InputTokensDetails.CachedTokens != 13 || response.Usage.InputTokensDetails.CacheCreationTokens != 5 {
		t.Fatalf("unexpected cache token details: %+v", response.Usage.InputTokensDetails)
	}
	if response.Usage.InputTokensDetails.AudioTokens != 2 || response.Usage.InputTokensDetails.ImageTokens != 3 || response.Usage.InputTokensDetails.TextTokens != 16 || response.Usage.OutputTokensDetails == nil || response.Usage.OutputTokensDetails.AcceptedPredictionTokens != 1 || response.Usage.OutputTokensDetails.RejectedPredictionTokens != 4 || response.Usage.OutputTokensDetails.TextTokens != 5 {
		t.Fatalf("response modality details lost: %+v", response.Usage)
	}
}

func TestOpenAICompatibleCompactsResponseWithoutLosingOpaqueOutput(t *testing.T) {
	var upstream map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/compact" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatalf("unexpected compact request: path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"cmp_1","object":"response.compaction","created_at":7,"output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"compaction","encrypted_content":"opaque","future":{"kept":true}}],"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}`))
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL+"/v1", "provider-key", false).CompactResponse(t.Context(), openai.ResponseCompactRequest{
		Provider: "route-only", Model: "gpt-compact", Input: []any{map[string]any{"role": "user", "content": "hello"}}, Instructions: "shorten",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := upstream["provider"]; found || string(upstream["model"]) != `"gpt-compact"` || string(upstream["instructions"]) != `"shorten"` {
		t.Fatalf("unexpected upstream payload: %+v", upstream)
	}
	if response.Usage.TotalTokens != 15 || len(response.Output) != 2 || !strings.Contains(string(response.Output[1]), `"future":{"kept":true}`) {
		t.Fatalf("opaque compacted response was not preserved: %+v output=%s", response, response.Output[1])
	}
}

func TestOpenAICompatibleForwardsNativeCompletionParameters(t *testing.T) {
	var upstream map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatalf("unexpected completion request: path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"cmpl_1","object":"text_completion","created":7,"model":"instruct","choices":[{"index":0,"text":" done","finish_reason":"stop","logprobs":{"text_offset":[0],"token_logprobs":[-0.1],"tokens":[" done"],"top_logprobs":[{" done":-0.1}]}}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
	}))
	defer server.Close()
	bestOf, n, maxTokens, logprobs := 2, 1, 9, 1
	echo := true
	response, err := NewOpenAICompatible(server.URL+"/v1", "provider-key", true).Completions(t.Context(), openai.CompletionRequest{
		Provider: "route-only", Model: "instruct", Prompt: []any{[]any{10, 11}, []any{12}}, BestOf: &bestOf, N: &n, MaxTokens: &maxTokens,
		Logprobs: &logprobs, Echo: &echo, Suffix: "suffix", Stop: []any{"END"}, User: "user-1", Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := upstream["provider"]; found || string(upstream["prompt"]) != `[[10,11],[12]]` || string(upstream["stream"]) != "false" || string(upstream["best_of"]) != "2" || string(upstream["suffix"]) != `"suffix"` {
		t.Fatalf("unexpected upstream payload: %+v", upstream)
	}
	if response.Usage.TotalTokens != 4 || response.Choices[0].Logprobs == nil || response.Choices[0].Logprobs.Tokens[0] != " done" {
		t.Fatalf("unexpected completion response: %+v", response)
	}
}

func TestOpenAICompatibleStreamsNativeCompletionsAndCollectsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		_, hasStreamOptions := request["stream_options"]
		if r.URL.Path != "/v1/completions" || string(request["stream"]) != "true" || string(request["prompt"]) != `"complete"` || hasStreamOptions {
			t.Fatalf("unexpected stream request: path=%s request=%v", r.URL.Path, request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"cmpl_stream\",\"object\":\"text_completion\",\"created\":7,\"model\":\"instruct\",\"choices\":[{\"index\":0,\"text\":\"hel\",\"finish_reason\":null,\"logprobs\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"cmpl_stream\",\"object\":\"text_completion\",\"created\":7,\"model\":\"instruct\",\"choices\":[{\"index\":0,\"text\":\"lo\",\"finish_reason\":\"stop\",\"logprobs\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"cmpl_stream\",\"object\":\"text_completion\",\"created\":7,\"model\":\"instruct\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	var payloads []string
	response, err := NewOpenAICompatible(server.URL+"/v1", "", true).StreamCompletions(t.Context(), openai.CompletionRequest{Model: "instruct", Prompt: "complete", Stream: true}, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 3 || len(response.Choices) != 1 || response.Choices[0].Text != "hello" || response.Choices[0].FinishReason != "stop" || response.Usage.TotalTokens != 5 {
		t.Fatalf("unexpected completion stream: payloads=%d response=%+v", len(payloads), response)
	}
}

func TestCompletionStreamRejectsInvalidChunkBeforeForwarding(t *testing.T) {
	for _, payload := range []string{
		`{"id":"cmpl","object":"text_completion","created":1,"model":"m","choices":[{"index":128,"text":"bad","finish_reason":"stop"}]}`,
		`{"id":"cmpl","object":"text_completion","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":4}}`,
	} {
		callbacks := 0
		stream := strings.NewReader("data: " + payload + "\n\ndata: [DONE]\n\n")
		_, err := streamCompletionData(stream, openai.CompletionRequest{Model: "m", Prompt: "x"}, func(string) error { callbacks++; return nil })
		if err == nil || callbacks != 0 {
			t.Fatalf("invalid chunk forwarded: err=%v callbacks=%d payload=%s", err, callbacks, payload)
		}
	}
}

func TestOpenAICompatibleRejectsInvalidCompactedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"cmp_1","object":"response.compaction","output":[null],"usage":{"input_tokens":-1}}`))
	}))
	defer server.Close()
	if _, err := NewOpenAICompatible(server.URL, "", false).CompactResponse(t.Context(), openai.ResponseCompactRequest{Model: "m", Input: "x"}); err == nil {
		t.Fatal("invalid compacted response was accepted")
	}
}

func TestOpenAICompatibleCapturesSafeUpstreamParameterError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"unsupported_parameter","param":"max_tokens","message":"secret request content"}}`))
	}))
	defer server.Close()

	_, err := NewOpenAICompatible(server.URL, "provider-key", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "model"})
	var providerErr *Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("expected provider error, got %v", err)
	}
	if providerErr.Class != FailureClientRequest || providerErr.StatusCode != http.StatusBadRequest || providerErr.UpstreamCode != "unsupported_parameter" || providerErr.Param != "max_tokens" {
		t.Fatalf("unexpected safe diagnostics: %+v", providerErr)
	}
	if strings.Contains(providerErr.Error(), "secret request content") {
		t.Fatal("raw upstream message leaked")
	}
}

func TestOpenAICompatibleRetriesLegacyMaxTokensAsMaxCompletionTokens(t *testing.T) {
	requests := make([]openAICompatibleChatRequest, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openAICompatibleChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
		if len(requests) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"unsupported_parameter","param":"max_tokens","message":"use max_completion_tokens"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chat-modern\",\"model\":\"gpt-modern\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	maxTokens := 256
	response, err := NewOpenAICompatible(server.URL, "provider-key", true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "gpt-modern", Messages: []openai.Message{{Role: "user", Content: "hello"}}, MaxTokens: &maxTokens,
	}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("upstream requests=%d, want compatibility retry", len(requests))
	}
	if requests[0].MaxTokens == nil || *requests[0].MaxTokens != maxTokens || requests[0].MaxCompletionTokens != nil {
		t.Fatalf("first request did not preserve client parameters: %+v", requests[0])
	}
	if requests[1].MaxTokens != nil || requests[1].MaxCompletionTokens == nil || *requests[1].MaxCompletionTokens != maxTokens {
		t.Fatalf("retry did not translate max_tokens: %+v", requests[1])
	}
	if !requests[1].Stream || openai.ContentText(response.Choices[0].Message.Content) != "OK" {
		t.Fatalf("streaming compatibility retry failed: request=%+v response=%+v", requests[1], response)
	}
}

func TestOpenAICompatibleForwardsVisionContent(t *testing.T) {
	var upstream openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{ID: "chat-vision", Model: "vision-model"})
	}))
	defer server.Close()
	content := []any{
		map[string]any{"type": "text", "text": "describe"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
	}
	_, err := NewOpenAICompatible(server.URL, "provider-key", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "vision-model", Messages: []openai.Message{{Role: "user", Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream.Messages) != 1 {
		t.Fatalf("vision message missing: %+v", upstream.Messages)
	}
	encoded, _ := json.Marshal(upstream.Messages[0].Content)
	if !strings.Contains(string(encoded), "iVBORw0KGgo=") {
		t.Fatalf("image content was not forwarded: %s", encoded)
	}
}

func TestOpenAICompatibleEmbeddings(t *testing.T) {
	var upstream openAICompatibleEmbeddingRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatalf("missing provider authorization")
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.EmbeddingResponse{Object: "list", Model: "embed-model", Data: []openai.Embedding{{Object: "embedding", Embedding: []float64{0.1, 0.2}, Index: 0}}, Usage: openai.Usage{PromptTokens: 2, TotalTokens: 2}})
	}))
	defer server.Close()
	dimensions := 2
	response, err := NewOpenAICompatible(server.URL, "provider-key", false).Embeddings(context.Background(), openai.EmbeddingRequest{Model: "embed-model", Input: []any{"hello"}, EncodingFormat: "float", Dimensions: &dimensions, User: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Model != "embed-model" || upstream.Dimensions == nil || *upstream.Dimensions != 2 || upstream.User != "user-1" {
		t.Fatalf("embedding request was not forwarded: %+v", upstream)
	}
	if len(response.Data) != 1 || len(response.Data[0].Embedding) != 2 || response.Usage.TotalTokens != 2 {
		t.Fatalf("unexpected embedding response: %+v", response)
	}
}

func TestOpenAICompatibleEmbeddingsForwardsTokenArrays(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstream openAICompatibleEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(upstream.Input)
		if string(encoded) != `[[11,12],[13]]` {
			t.Fatalf("token boundaries changed: %s", encoded)
		}
		_, _ = w.Write([]byte(`{"object":"list","model":"embed-model","data":[{"object":"embedding","index":0,"embedding":[0.1]},{"object":"embedding","index":1,"embedding":[0.2]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`))
	}))
	defer server.Close()
	response, err := NewOpenAICompatible(server.URL, "", false).Embeddings(t.Context(), openai.EmbeddingRequest{
		Model: "embed-model", Input: []any{[]any{11.0, 12.0}, []any{13.0}},
	})
	if err != nil || len(response.Data) != 2 || response.Usage.PromptTokens != 3 {
		t.Fatalf("unexpected token embedding response: %+v err=%v", response, err)
	}
}

func TestOpenAICompatibleEmbeddingsPreservesBase64Output(t *testing.T) {
	const encodedVector = "AACAPwAAAEA="
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstream openAICompatibleEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		if upstream.EncodingFormat != "base64" {
			t.Fatalf("encoding format was not forwarded: %+v", upstream)
		}
		_, _ = w.Write([]byte(`{"object":"list","model":"embed-model","data":[{"object":"embedding","index":0,"embedding":"` + encodedVector + `"}],"usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer server.Close()
	dimensions := 2
	response, err := NewOpenAICompatible(server.URL, "", false).Embeddings(t.Context(), openai.EmbeddingRequest{
		Model: "embed-model", Input: "hello", EncodingFormat: "base64", Dimensions: &dimensions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].EmbeddingBase64 != encodedVector || len(response.Data[0].Embedding) != 0 || !response.UsageReported {
		t.Fatalf("base64 response was not preserved: %+v", response)
	}
}

func TestOpenAICompatibleRerankUsesProviderCredentialAndConfiguredPath(t *testing.T) {
	var upstream openAICompatibleRerankRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatalf("unexpected authorization: %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.RerankResponse{ID: "r-1", Results: []openai.RerankResult{{Index: 0, RelevanceScore: 0.8}}})
	}))
	defer server.Close()
	topN := 1
	response, err := NewOpenAICompatibleWithRerankPath(server.URL+"/v1", "provider-key", false, "/rerank").Rerank(context.Background(), openai.RerankRequest{Provider: "must-not-leak", Model: "reranker", Query: "q", Documents: []any{"doc"}, TopN: &topN})
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Model != "reranker" || upstream.Query != "q" || len(upstream.Documents) != 1 || response.ID != "r-1" {
		t.Fatalf("request=%+v response=%+v", upstream, response)
	}
}

func TestOpenAICompatibleRerankDefaultsToV1Path(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(openai.RerankResponse{Results: []openai.RerankResult{{Index: 0, RelevanceScore: 1}}})
	}))
	defer server.Close()
	_, err := NewOpenAICompatible(server.URL, "", false).Rerank(context.Background(), openai.RerankRequest{Model: "m", Query: "q", Documents: []any{"d"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProviderURLAddsV1ForRootBaseURL(t *testing.T) {
	got := providerURL("https://example.test", "responses")
	want := "https://example.test/v1/responses"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestOpenAICompatibleDoesNotForwardStreamWhenDisabled(t *testing.T) {
	var upstreamRequest openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID:     "chatcmpl-test",
			Object: "chat.completion",
			Model:  upstreamRequest.Model,
			Choices: []openai.Choice{
				{Index: 0, Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatible(server.URL, "", false)
	response, err := provider.ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:  "test-model",
		Stream: true,
		Messages: []openai.Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if upstreamRequest.Stream {
		t.Fatal("expected upstream stream to be disabled")
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "ok" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestOpenAICompatibleCollectsChatStreamWhenEnabled(t *testing.T) {
	var upstreamRequest openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"plan ","content":"hel"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","model":"test-model","choices":[{"index":0,"delta":{"reasoning_content":"more","content":"lo"},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAICompatible(server.URL, "", true)
	response, err := provider.ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:  "test-model",
		Stream: true,
		Messages: []openai.Message{
			{Role: "user", Content: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !upstreamRequest.Stream {
		t.Fatal("expected upstream stream to be enabled")
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("expected collected stream content, got %+v", response.Choices[0].Message.Content)
	}
	if got := response.Choices[0].Message.ReasoningContent; got != "plan more" {
		t.Fatalf("expected collected stream reasoning_content, got %q", got)
	}
}

func TestOpenAICompatibleRejectsOversizedStreamingReasoningContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		remaining := openai.MaxChatReasoningContentBytes + 1
		for remaining > 0 {
			size := min(remaining, 32<<10)
			delta := map[string]any{"role": "assistant", "reasoning_content": strings.Repeat("x", size)}
			choice := map[string]any{"index": 0, "delta": delta}
			chunk, err := json.Marshal(map[string]any{"id": "chatcmpl-test", "model": "test-model", "choices": []any{choice}})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			remaining -= size
		}
	}))
	defer server.Close()

	_, err := NewOpenAICompatible(server.URL, "", true).ChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "test-model", Stream: true})
	if err == nil || !strings.Contains(err.Error(), "reasoning_content stream exceeds limit") {
		t.Fatalf("oversized stream reasoning_content was not rejected: %v", err)
	}
}

func TestOpenAICompatibleStreamsChatPayloadsWhenEnabled(t *testing.T) {
	var upstreamRequest openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"},"finish_reason":null}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-test","model":"test-model","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	var payloads []string
	provider := NewOpenAICompatible(server.URL, "", true)
	response, err := provider.StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model:  "test-model",
		Stream: true,
		Messages: []openai.Message{
			{Role: "user", Content: "hello"},
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
	if len(payloads) != 2 {
		t.Fatalf("expected two streamed payloads, got %d: %v", len(payloads), payloads)
	}
	if !strings.Contains(payloads[0], `"content":"hel"`) || !strings.Contains(payloads[1], `"content":"lo"`) {
		t.Fatalf("unexpected streamed payloads: %v", payloads)
	}
	if openai.ContentText(response.Choices[0].Message.Content) != "hello" {
		t.Fatalf("expected collected stream content, got %+v", response.Choices[0].Message.Content)
	}
}

func TestOpenAICompatibleStreamsResponsesWhenEnabled(t *testing.T) {
	var upstreamRequest openAICompatibleResponseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`event: response.created` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp-test","object":"response","status":"in_progress","model":"test-model","output":[]}}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.output_text.delta` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","response_id":"resp-test","delta":"hel"}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.output_text.delta` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","response_id":"resp-test","delta":"lo"}` + "\n\n"))
		_, _ = w.Write([]byte(`event: response.completed` + "\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp-test","object":"response","status":"completed","model":"test-model","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"output_text":"hello"}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	var events []string
	var payloads []string
	includeObfuscation := false
	maxResults := 12
	scoreThreshold := 0.4
	rewriteQuery := true
	webSearch := openai.ResponseTool{Type: "web_search", Filters: map[string]any{"allowed_domains": []string{"example.com"}}, SearchContextSize: "high", UserLocation: &openai.ResponseWebSearchLocation{Type: "approximate", Country: "RU", Timezone: "Europe/Moscow"}}
	provider := NewOpenAICompatible(server.URL, "", true)
	response, err := provider.StreamResponses(context.Background(), openai.ResponseRequest{
		Model: "test-model", Input: "hello", Stream: true, StreamOptions: &openai.ResponseStreamOptions{IncludeObfuscation: &includeObfuscation}, PreviousResponse: "resp-previous", SafetyIdentifier: "provider-user", PromptCacheKey: "tenant-thread",
		Tools: []openai.ResponseTool{
			{Type: "function", Name: "weather", Parameters: map[string]any{"type": "object"}},
			{Type: "mcp", ServerLabel: "weather-prod", ServerURL: "https://mcp.example.test", AllowedTools: []string{"forecast"}, RequireApproval: "never", Headers: map[string]string{"X-MCP-Key": "scoped"}},
			{Type: "code_interpreter", Container: map[string]any{"type": "auto", "file_ids": []string{"file_owned"}}},
			{Type: "file_search", VectorStoreIDs: []string{"vs_owned"}, Filters: map[string]any{"type": "eq", "key": "team", "value": "support"}, MaxNumResults: &maxResults, RankingOptions: &openai.FileSearchRankingOptions{Ranker: "auto", ScoreThreshold: &scoreThreshold}, RewriteQuery: &rewriteQuery},
			webSearch,
		},
		ToolChoice: "auto", Text: map[string]any{"format": map[string]any{"type": "json_object"}, "verbosity": "high"},
	}, func(event string, payload string) error {
		events = append(events, event)
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !upstreamRequest.Stream {
		t.Fatal("expected responses upstream stream to be enabled")
	}
	if upstreamRequest.StreamOptions == nil || upstreamRequest.StreamOptions.IncludeObfuscation == nil || *upstreamRequest.StreamOptions.IncludeObfuscation {
		t.Fatalf("responses stream options were not forwarded: %+v", upstreamRequest.StreamOptions)
	}
	textConfig, _ := upstreamRequest.Text.(map[string]any)
	container, _ := upstreamRequest.Tools[2].Container.(map[string]any)
	fileIDs, _ := container["file_ids"].([]any)
	fileSearch := upstreamRequest.Tools[3]
	forwardedWebSearch := upstreamRequest.Tools[4]
	filter, _ := fileSearch.Filters.(map[string]any)
	webFilter, _ := forwardedWebSearch.Filters.(map[string]any)
	webDomains, _ := webFilter["allowed_domains"].([]any)
	if upstreamRequest.PreviousResponse != "resp-previous" || upstreamRequest.SafetyIdentifier != "provider-user" || upstreamRequest.PromptCacheKey != "tenant-thread" || len(upstreamRequest.Tools) != 5 || upstreamRequest.Tools[0].Name != "weather" || upstreamRequest.Tools[1].ServerLabel != "weather-prod" || upstreamRequest.Tools[1].Headers["X-MCP-Key"] != "scoped" || len(fileIDs) != 1 || fileIDs[0] != "file_owned" || len(fileSearch.VectorStoreIDs) != 1 || fileSearch.VectorStoreIDs[0] != "vs_owned" || filter["key"] != "team" || fileSearch.MaxNumResults == nil || *fileSearch.MaxNumResults != 12 || fileSearch.RankingOptions == nil || fileSearch.RankingOptions.ScoreThreshold == nil || *fileSearch.RankingOptions.ScoreThreshold != 0.4 || fileSearch.RewriteQuery == nil || !*fileSearch.RewriteQuery || forwardedWebSearch.SearchContextSize != "high" || forwardedWebSearch.UserLocation == nil || forwardedWebSearch.UserLocation.Country != "RU" || len(webDomains) != 1 || webDomains[0] != "example.com" || textConfig["verbosity"] != "high" {
		t.Fatalf("responses tools/state/format were not forwarded: %+v", upstreamRequest)
	}
	if len(payloads) != 4 {
		t.Fatalf("expected four streamed payloads, got %d: %v", len(payloads), payloads)
	}
	if events[0] != "response.created" || events[1] != "response.output_text.delta" || events[3] != "response.completed" {
		t.Fatalf("unexpected events: %v", events)
	}
	if response.OutputText != "hello" {
		t.Fatalf("expected collected output_text, got %q", response.OutputText)
	}
}

func TestOpenAICompatibleForwardsResponseCacheIdentifiers(t *testing.T) {
	var upstreamRequest openAICompatibleResponseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.ResponseResponse{
			ID: "resp-test", Object: "response", Status: "completed", Model: "test-model",
			Output: []openai.ResponseOutputItem{{Type: "message", Status: "completed", Role: "assistant", Content: []openai.ResponseOutputContent{{Type: "output_text", Text: "ok"}}}},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatible(server.URL, "", false)
	input := []any{map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,JVBERi0xLjQK", "filename": "input.pdf"}}
	cacheOptions := &openai.PromptCacheOptions{Mode: "explicit", TTL: "30m", ComparisonResponseID: "resp_baseline"}
	if _, err := provider.Responses(context.Background(), openai.ResponseRequest{Model: "test-model", Input: input, SafetyIdentifier: "provider-user", PromptCacheKey: "tenant-thread", PromptCacheOptions: cacheOptions, PromptCacheRetention: "24h"}); err != nil {
		t.Fatal(err)
	}
	if upstreamRequest.SafetyIdentifier != "provider-user" || upstreamRequest.PromptCacheKey != "tenant-thread" || upstreamRequest.PromptCacheOptions == nil || upstreamRequest.PromptCacheOptions.Mode != "explicit" || upstreamRequest.PromptCacheOptions.TTL != "30m" || upstreamRequest.PromptCacheOptions.ComparisonResponseID != "resp_baseline" || upstreamRequest.PromptCacheRetention != "24h" {
		t.Fatalf("cache identifiers were not forwarded: %+v", upstreamRequest)
	}
	parts, ok := upstreamRequest.Input.([]any)
	if !ok || len(parts) != 1 || parts[0].(map[string]any)["type"] != "input_file" {
		t.Fatalf("file input was not forwarded: %#v", upstreamRequest.Input)
	}
}

func TestOpenAICompatibleForwardsToolsAndStructuredOutput(t *testing.T) {
	var upstream openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			ID: "chat-tools", Object: "chat.completion", Model: upstream.Model,
			Choices: []openai.Choice{{Index: 0, FinishReason: "tool_calls", Message: openai.Message{
				Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call-1", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Moscow"}`}}},
			}}},
		})
	}))
	defer server.Close()

	strict := true
	response, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{
		Model: "test-model", Messages: []openai.Message{{Role: "user", Content: "weather"}},
		Tools:      []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}}}},
		ToolChoice: "required", ParallelToolCalls: &strict,
		ResponseFormat: &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "weather_result", Schema: map[string]any{"type": "object"}, Strict: &strict}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(upstream.Tools) != 1 || upstream.Tools[0].Function.Name != "weather" || upstream.ToolChoice != "required" || upstream.ResponseFormat == nil {
		t.Fatalf("tool contract was not forwarded: %+v", upstream)
	}
	if len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("tool response was not preserved: %+v", response)
	}
}

func TestOpenAICompatibleCollectsStreamingToolCallArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chat-tools\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\"}}]},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chat-tools\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"Moscow\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	response, err := NewOpenAICompatible(server.URL, "", true).StreamChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "test-model"}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.ID != "call-1" || call.Function.Name != "weather" || call.Function.Arguments != `{"city":"Moscow"}` {
		t.Fatalf("unexpected accumulated tool call: %+v", call)
	}
}

func TestChatStreamPreservesReportedUsage(t *testing.T) {
	payload := "data: {\"id\":\"chat-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chat-test\",\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"total_tokens\":120,\"prompt_tokens_details\":{\"cached_tokens\":80,\"audio_tokens\":2,\"image_tokens\":3,\"text_tokens\":95},\"completion_tokens_details\":{\"accepted_prediction_tokens\":7,\"audio_tokens\":2,\"reasoning_tokens\":3,\"rejected_prediction_tokens\":4,\"text_tokens\":11}}}\n\n" +
		"data: [DONE]\n\n"
	for _, streaming := range []bool{false, true} {
		var writer ChatCompletionStreamWriter
		var forwarded []string
		if streaming {
			writer = func(payload string) error { forwarded = append(forwarded, payload); return nil }
		}
		response, err := streamChatCompletionData(strings.NewReader(payload), "model", writer)
		if err != nil {
			t.Fatal(err)
		}
		if response.Usage.PromptTokens != 100 || response.Usage.CompletionTokens != 20 || response.Usage.TotalTokens != 120 || response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedTokens != 80 {
			t.Fatalf("reported stream usage lost: %+v", response.Usage)
		}
		if response.Usage.PromptTokensDetails.ImageTokens != 3 || response.Usage.CompletionTokensDetails == nil || response.Usage.CompletionTokensDetails.AcceptedPredictionTokens != 7 || response.Usage.CompletionTokensDetails.RejectedPredictionTokens != 4 || response.Usage.CompletionTokensDetails.TextTokens != 11 {
			t.Fatalf("stream token details lost: %+v", response.Usage)
		}
		if len(response.Choices) != 1 || response.Choices[0].Message.Content != "hello" {
			t.Fatalf("usage-only event changed content: %+v", response)
		}
		if streaming && (len(forwarded) != 2 || !strings.Contains(forwarded[1], "cached_tokens")) {
			t.Fatalf("usage event not forwarded: %v", forwarded)
		}
	}
}

func TestChatStreamRejectsInvalidIndicesBeforeWriting(t *testing.T) {
	for _, index := range []int{-1, 128, math.MaxInt} {
		for _, tool := range []bool{false, true} {
			t.Run(fmt.Sprintf("index=%d/tool=%v", index, tool), func(t *testing.T) {
				choice := map[string]any{"index": index, "delta": map[string]any{}}
				if tool {
					choice = map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": index, "function": map[string]any{"arguments": "{}"}}}}}
				}
				encoded, err := json.Marshal(map[string]any{"choices": []any{choice}})
				if err != nil {
					t.Fatal(err)
				}
				wrote := false
				_, err = streamChatCompletionData(strings.NewReader("data: "+string(encoded)+"\n\n"), "test", func(string) error { wrote = true; return nil })
				if err == nil || wrote {
					t.Fatalf("invalid index accepted: err=%v wrote=%v", err, wrote)
				}
			})
		}
	}
}

func TestChatStreamRejectsChangingResponseEnvelope(t *testing.T) {
	first := `{"id":"chat-one","created":123,"model":"model-one","metadata":{"trace":"one"},"service_tier":"priority","system_fingerprint":"fp-one","choices":[]}`
	for name, second := range map[string]string{
		"id":                 `{"id":"chat-two","choices":[]}`,
		"created":            `{"created":124,"choices":[]}`,
		"model":              `{"model":"model-two","choices":[]}`,
		"metadata":           `{"metadata":{"trace":"two"},"choices":[]}`,
		"service tier":       `{"service_tier":"scale","choices":[]}`,
		"system fingerprint": `{"system_fingerprint":"fp-two","choices":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			callbacks := 0
			payload := "data: " + first + "\n\ndata: " + second + "\n\n"
			_, err := streamChatCompletionData(strings.NewReader(payload), "fallback", func(string) error { callbacks++; return nil })
			if err == nil || callbacks != 1 {
				t.Fatalf("changing envelope was forwarded: err=%v callbacks=%d", err, callbacks)
			}
		})
	}
}

func TestChatStreamRejectsInvalidResponseEnvelope(t *testing.T) {
	for name, payload := range map[string]string{
		"negative timestamp": `{"created":-1,"choices":[]}`,
		"oversized metadata": `{"metadata":{"trace":"` + strings.Repeat("x", 513) + `"},"choices":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			wrote := false
			_, err := streamChatCompletionData(strings.NewReader("data: "+payload+"\n\n"), "test", func(string) error { wrote = true; return nil })
			if err == nil || wrote {
				t.Fatalf("invalid response envelope was forwarded: err=%v wrote=%v", err, wrote)
			}
		})
	}
}

func TestChatStreamIndexBoundaries(t *testing.T) {
	payload := `data: {"choices":[{"index":127,"delta":{"tool_calls":[{"index":127,"function":{"arguments":"{}"}}]}}]}` + "\n\n"
	response, err := decodeChatCompletionStream(strings.NewReader(payload), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 128 || len(response.Choices[127].Message.ToolCalls) != 128 {
		t.Fatal("valid boundary index rejected")
	}
}

func TestCompatibleStreamPreservesToolSignatures(t *testing.T) {
	payload := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call","type":"function","function":{"name":"lookup","arguments":"{}"},"extra_content":{"google":{"thought_signature":"opaque"}}}]}}]}` + "\n\n"
	response, err := decodeChatCompletionStream(strings.NewReader(payload), "test")
	if err != nil {
		t.Fatal(err)
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.ExtraContent == nil || call.ExtraContent.Google == nil || call.ExtraContent.Google.ThoughtSignature != "opaque" {
		t.Fatal("tool signature was lost during accumulation")
	}
}
