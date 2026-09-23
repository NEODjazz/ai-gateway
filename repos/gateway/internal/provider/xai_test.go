package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var _ Client = XAI{}
var _ StreamingClient = XAI{}
var _ EmbeddingClient = XAI{}
var _ ImageGenerationClient = XAI{}
var _ ImageEditClient = XAI{}
var _ AudioTranscriptionClient = XAI{}
var _ AudioTranscriptionDurationReserver = XAI{}
var _ AudioSpeechClient = XAI{}
var _ VideoClient = XAI{}
var _ VideoExtensionClient = XAI{}
var _ responseRetrieveClient = XAI{}
var _ responseInputItemsClient = XAI{}
var _ responseDeleteClient = XAI{}
var _ ResponseCompactClient = XAI{}

func TestXAIChatAndResponsesContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xai-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "grok-4.7" || body["service_tier"] != "priority" {
			t.Fatalf("request=%#v", body)
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			if body["reasoning_effort"] != "xhigh" {
				t.Fatalf("chat request=%#v", body)
			}
			_, _ = fmt.Fprint(w, `{"id":"chat","object":"chat.completion","model":"grok","service_tier":"priority","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		case "/v1/responses":
			reasoning, ok := body["reasoning"].(map[string]any)
			if !ok || reasoning["effort"] != "high" || body["prompt_cache_key"] != "conversation" || body["max_output_tokens"] != float64(64) || body["max_tool_calls"] != float64(3) || body["parallel_tool_calls"] != false || body["temperature"] != 0.7 || body["top_p"] != 0.9 || body["store"] != false || body["previous_response_id"] != "resp_prior" {
				t.Fatalf("response request=%#v", body)
			}
			_, _ = fmt.Fprint(w, `{"id":"resp","object":"response","model":"grok","created_at":100,"completed_at":101,"background":false,"store":false,"previous_response_id":"resp_prior","service_tier":"priority","status":"completed","max_output_tokens":64,"max_tool_calls":3,"parallel_tool_calls":false,"temperature":0.7,"top_p":0.9,"top_logprobs":0,"frequency_penalty":0,"presence_penalty":0,"truncation":"disabled","reasoning":{"effort":"high"},"text":{"format":{"type":"text"}},"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":"auto","citations":["https://x.ai/news","https://x.com/xai/status/1"],"output":[{"id":"message","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3,"num_sources_used":4,"num_server_side_tools_used":2,"server_side_tool_usage_details":{"x_posts_fetched":6,"x_users_fetched":1}}}`)
		default:
			t.Fatalf("path=%q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewXAI(server.URL+"/v1", "xai-key", true)
	chat, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "grok-4.7", Messages: []openai.Message{{Role: "user", Content: "hello"}},
		ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "xhigh", ServiceTier: "priority"},
	})
	if err != nil || chat.Usage.TotalTokens != 3 || chat.ServiceTier != "priority" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	effort := "high"
	maxToolCalls := 3
	maxOutputTokens := 64
	parallelToolCalls := false
	store := false
	temperature := 0.7
	topP := 0.9
	response, err := client.Responses(t.Context(), openai.ResponseRequest{
		Model: "grok-4.7", Input: "hello", ServiceTier: "priority", PromptCacheKey: "conversation",
		Reasoning: &openai.ResponseReasoning{Effort: &effort}, Text: map[string]any{"format": map[string]any{"type": "text"}}, Tools: []openai.ResponseTool{{Type: "function", Name: "lookup", Parameters: map[string]any{"type": "object"}}}, ToolChoice: "auto", MaxOutputTokens: &maxOutputTokens, MaxToolCalls: &maxToolCalls, ParallelToolCalls: &parallelToolCalls, Temperature: &temperature, TopP: &topP, Store: &store, PreviousResponse: "resp_prior",
	})
	if err != nil || response.CreatedAt != 100 || response.CompletedAt != 101 || response.Background == nil || *response.Background || response.Store == nil || *response.Store || response.PreviousResponseID == nil || *response.PreviousResponseID != "resp_prior" || response.ServiceTier != "priority" || response.MaxOutputTokens == nil || *response.MaxOutputTokens != 64 || response.MaxToolCalls == nil || *response.MaxToolCalls != 3 || response.ParallelToolCalls == nil || *response.ParallelToolCalls || response.Temperature == nil || *response.Temperature != 0.7 || response.TopP == nil || *response.TopP != 0.9 || response.TopLogprobs == nil || *response.TopLogprobs != 0 || response.FrequencyPenalty == nil || *response.FrequencyPenalty != 0 || response.PresencePenalty == nil || *response.PresencePenalty != 0 || response.Truncation == nil || *response.Truncation != "disabled" || response.Reasoning == nil || response.Reasoning.Effort == nil || *response.Reasoning.Effort != "high" || len(response.Tools) != 1 || response.Tools[0].Name != "lookup" || response.Text == nil || response.ToolChoice != "auto" || !slices.Equal(response.Citations, []string{"https://x.ai/news", "https://x.com/xai/status/1"}) || response.Usage.TotalTokens != 3 || response.OutputText != "ok" || response.Usage.NumSourcesUsed == nil || *response.Usage.NumSourcesUsed != 4 || response.Usage.NumServerSideToolsUsed == nil || *response.Usage.NumServerSideToolsUsed != 2 || response.Usage.ServerSideToolUsageDetails == nil || response.Usage.ServerSideToolUsageDetails.XPostsFetched == nil || *response.Usage.ServerSideToolUsageDetails.XPostsFetched != 6 || response.Usage.ServerSideToolUsageDetails.XUsersFetched == nil || *response.Usage.ServerSideToolUsageDetails.XUsersFetched != 1 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestXAIRejectsUnsupportedParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewXAI(server.URL, "key", true)

	invalidChat := openai.ChatCompletionRequest{Model: "grok", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "auto"}}
	if _, err := client.ChatCompletions(t.Context(), invalidChat); !xaiFailure(err, "service_tier", "invalid_request") || called {
		t.Fatalf("chat err=%v called=%v", err, called)
	}
	penalty := 0.5
	topLogprobs := 8
	invalidEffort := "max"
	metadata := map[string]string{"ticket": "42"}
	truncation := "auto"
	tests := []struct {
		param string
		code  string
		req   openai.ResponseRequest
	}{
		{param: "background", code: "unsupported_parameter", req: openai.ResponseRequest{Background: true}},
		{param: "frequency_penalty", code: "unsupported_parameter", req: openai.ResponseRequest{FrequencyPenalty: &penalty}},
		{param: "presence_penalty", code: "unsupported_parameter", req: openai.ResponseRequest{PresencePenalty: &penalty}},
		{param: "top_logprobs", code: "unsupported_parameter", req: openai.ResponseRequest{TopLogprobs: &topLogprobs}},
		{param: "reasoning.effort", code: "invalid_request", req: openai.ResponseRequest{Reasoning: &openai.ResponseReasoning{Effort: &invalidEffort}}},
		{param: "service_tier", code: "invalid_request", req: openai.ResponseRequest{ServiceTier: "flex"}},
		{param: "metadata", code: "unsupported_parameter", req: openai.ResponseRequest{Metadata: metadata}},
		{param: "truncation", code: "unsupported_parameter", req: openai.ResponseRequest{Truncation: &truncation}},
		{param: "safety_identifier", code: "unsupported_parameter", req: openai.ResponseRequest{SafetyIdentifier: "user"}},
		{param: "text.verbosity", code: "unsupported_parameter", req: openai.ResponseRequest{Text: map[string]any{"verbosity": "high"}}},
	}
	for _, test := range tests {
		t.Run(test.param, func(t *testing.T) {
			test.req.Model = "grok"
			test.req.Input = "hello"
			_, err := client.Responses(t.Context(), test.req)
			if !xaiFailure(err, test.param, test.code) || called {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

func TestXAIChatParameterPolicy(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewXAI(server.URL, "key", true)
	logprobs := true
	falseLogprobs := false
	topEight := 8
	topNine := 9
	if err := client.ValidateChatParameters(openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &logprobs, TopLogprobs: &topEight}}); err != nil {
		t.Fatalf("valid parameters: %v", err)
	}
	tests := []struct {
		name    string
		options openai.ChatGenerationOptions
		param   string
		code    string
	}{
		{name: "top logprobs limit", options: openai.ChatGenerationOptions{Logprobs: &logprobs, TopLogprobs: &topNine}, param: "top_logprobs", code: "invalid_request"},
		{name: "top logprobs requires logprobs", options: openai.ChatGenerationOptions{Logprobs: &falseLogprobs, TopLogprobs: &topEight}, param: "top_logprobs", code: "invalid_request"},
		{name: "logit bias", options: openai.ChatGenerationOptions{LogitBias: map[string]int{"1": 2}}, param: "logit_bias", code: "unsupported_parameter"},
		{name: "web fetch", options: openai.ChatGenerationOptions{WebFetchOptions: &openai.ChatWebFetchOptions{AllowedDomains: []string{"example.com"}, MaxContentTokens: 10}}, param: "web_fetch_options", code: "unsupported_parameter"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := openai.ChatCompletionRequest{Model: "grok", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: test.options}
			_, err := client.ChatCompletions(t.Context(), request)
			if !xaiFailure(err, test.param, test.code) || called {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

func TestXAIReasoningEffortIsModelScoped(t *testing.T) {
	client := NewXAI("http://unused.invalid", "", false)
	tests := []struct {
		model, effort   string
		chat, responses bool
	}{
		{model: "grok-4.5", effort: "low", chat: true, responses: true},
		{model: "grok-4.5", effort: "xhigh"},
		{model: "grok-4.6", effort: "xhigh", chat: true, responses: true},
		{model: "grok-4.7-latest", effort: "xhigh", chat: true, responses: true},
		{model: "grok-4.7", effort: "none"},
		{model: "grok-4.20-multi-agent", effort: "xhigh", responses: true},
		{model: "unknown", effort: "high"},
	}
	for _, test := range tests {
		t.Run(test.model+"/"+test.effort, func(t *testing.T) {
			chat := openai.ChatCompletionRequest{Model: test.model, Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: test.effort}}
			chatErr := client.ValidateChatParameters(chat)
			if test.chat && chatErr != nil || !test.chat && !xaiFailure(chatErr, "reasoning_effort", "invalid_request") {
				t.Fatalf("chat err=%v expected support=%v", chatErr, test.chat)
			}
			responses := openai.ResponseRequest{Model: test.model, Input: "hello", Reasoning: &openai.ResponseReasoning{Effort: &test.effort}}
			responseErr := client.ValidateResponseParameters(responses)
			if test.responses && responseErr != nil || !test.responses && !xaiFailure(responseErr, "reasoning.effort", "invalid_request") {
				t.Fatalf("Responses err=%v expected support=%v", responseErr, test.responses)
			}
		})
	}
}

func TestXAIRejectsSilentlyIgnoredLogprobs(t *testing.T) {
	client := NewXAI("http://unused.invalid", "", false)
	logprobs := true
	topLogprobs := 4
	for _, test := range []struct {
		model string
		param string
		opts  openai.ChatGenerationOptions
	}{
		{model: "grok-4.20", param: "logprobs", opts: openai.ChatGenerationOptions{Logprobs: &logprobs}},
		{model: "grok-4.20-0309-reasoning", param: "top_logprobs", opts: openai.ChatGenerationOptions{Logprobs: &logprobs, TopLogprobs: &topLogprobs}},
		{model: "grok-4.3", param: "logprobs", opts: openai.ChatGenerationOptions{Logprobs: &logprobs}},
		{model: "grok-4.5", param: "logprobs", opts: openai.ChatGenerationOptions{Logprobs: &logprobs}},
		{model: "grok-4.6-latest", param: "logprobs", opts: openai.ChatGenerationOptions{Logprobs: &logprobs}},
		{model: "grok-4.7", param: "logprobs", opts: openai.ChatGenerationOptions{Logprobs: &logprobs}},
	} {
		t.Run(test.model+"/"+test.param, func(t *testing.T) {
			request := openai.ChatCompletionRequest{Model: test.model, Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: test.opts}
			if err := client.ValidateChatParameters(request); !xaiFailure(err, test.param, "invalid_request") {
				t.Fatalf("error=%v", err)
			}
		})
	}
	request := openai.ChatCompletionRequest{Model: "custom-model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &logprobs, TopLogprobs: &topLogprobs}}
	if err := client.ValidateChatParameters(request); err != nil {
		t.Fatalf("unknown model should retain passthrough parameters: %v", err)
	}
}

func TestXAICapabilityProfilePublishesReasoningByModel(t *testing.T) {
	for _, profile := range ManagedProviderCapabilityProfiles() {
		if profile.Type != "xai" {
			continue
		}
		if len(profile.ChatParameters.ReasoningEffort) != 0 || len(profile.ResponseParameters.ReasoningEffort) != 0 {
			t.Fatalf("provider-wide reasoning policy=%+v %+v", profile.ChatParameters, profile.ResponseParameters)
		}
		wantChat := []ProviderChatModelParameterPolicy{
			{Model: "grok-4.20", SupportedOptions: []string{}, UnsupportedOptions: []string{"logprobs", "top_logprobs"}, ReasoningEffort: []string{}, ReasoningFormat: []string{}},
			{Model: "grok-4.3", SupportedOptions: []string{}, UnsupportedOptions: []string{"logprobs", "top_logprobs"}, ReasoningEffort: []string{}, ReasoningFormat: []string{}},
			{Model: "grok-4.5", SupportedOptions: []string{"reasoning_effort"}, UnsupportedOptions: []string{"logprobs", "top_logprobs"}, ReasoningEffort: []string{"low", "medium", "high"}, ReasoningFormat: []string{}},
			{Model: "grok-4.6", SupportedOptions: []string{"reasoning_effort"}, UnsupportedOptions: []string{"logprobs", "top_logprobs"}, ReasoningEffort: []string{"low", "medium", "high", "xhigh"}, ReasoningFormat: []string{}},
			{Model: "grok-4.7", SupportedOptions: []string{"reasoning_effort"}, UnsupportedOptions: []string{"logprobs", "top_logprobs"}, ReasoningEffort: []string{"low", "medium", "high", "xhigh"}, ReasoningFormat: []string{}},
		}
		if fmt.Sprint(profile.ChatModelParameters) != fmt.Sprint(wantChat) {
			t.Fatalf("chat model policies=%+v want=%+v", profile.ChatModelParameters, wantChat)
		}
		wantResponses := []ProviderResponseModelParameterPolicy{
			{Model: "grok-4.5", SupportedOptions: []string{"reasoning"}, ReasoningEffort: []string{"low", "medium", "high"}},
			{Model: "grok-4.6", SupportedOptions: []string{"reasoning"}, ReasoningEffort: []string{"low", "medium", "high", "xhigh"}},
			{Model: "grok-4.7", SupportedOptions: []string{"reasoning"}, ReasoningEffort: []string{"low", "medium", "high", "xhigh"}},
			{Model: "grok-4.20-multi-agent", SupportedOptions: []string{"reasoning"}, ReasoningEffort: []string{"low", "medium", "high", "xhigh"}},
		}
		if fmt.Sprint(profile.ResponseModelParameters) != fmt.Sprint(wantResponses) {
			t.Fatalf("Responses model policies=%+v want=%+v", profile.ResponseModelParameters, wantResponses)
		}
		return
	}
	t.Fatal("xAI profile is missing")
}

func TestXAIResponseResourceLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xai-key" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp_1":
			_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","model":"grok","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp_1/input_items":
			if r.URL.Query().Get("after") != "item_1" || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("order") != "asc" {
				t.Fatalf("query=%v", r.URL.Query())
			}
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"item_2","type":"message"}],"has_more":false}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/responses/resp_1":
			_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","deleted":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses/compact":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "grok" || body["input"] != "history" {
				t.Fatalf("compact body=%#v err=%v", body, err)
			}
			_, _ = fmt.Fprint(w, `{"id":"cmp_1","object":"response.compaction","created_at":1,"output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()
	client := NewXAI(server.URL+"/v1", "xai-key", true)
	response, err := client.RetrieveResponse(t.Context(), "resp_1")
	if err != nil || response.ID != "resp_1" || response.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	items, err := client.ListResponseInputItems(t.Context(), "resp_1", ResponseInputItemsOptions{After: "item_1", Limit: 1, Order: "asc"})
	if err != nil || len(items.Data) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	deleted, err := client.DeleteResponse(t.Context(), "resp_1")
	if err != nil || !deleted.Deleted || deleted.Object != "response" {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
	compacted, err := client.CompactResponse(t.Context(), openai.ResponseCompactRequest{Model: "grok", Input: "history"})
	if err != nil || compacted.ID != "cmp_1" || compacted.Usage.TotalTokens != 12 {
		t.Fatalf("compacted=%+v err=%v", compacted, err)
	}
}

func TestXAIEmbeddingContract(t *testing.T) {
	const encodedVector = "AACAPwAAAEA="
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer xai-key" {
			t.Fatalf("unexpected request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		input, ok := body["input"].([]any)
		if !ok || len(input) != 2 || body["model"] != "v1" || body["encoding_format"] != "base64" || body["dimensions"] != float64(2) || body["user"] != "user-1" {
			t.Fatalf("embedding request=%#v", body)
		}
		_, _ = fmt.Fprint(w, `{"object":"list","model":"v1","data":[{"object":"embedding","index":0,"embedding":"`+encodedVector+`"},{"object":"embedding","index":1,"embedding":"`+encodedVector+`"}],"usage":{"prompt_tokens":3,"total_tokens":3}}`)
	}))
	defer server.Close()
	dimensions := 2
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "xai-native", Type: "xai", BaseURL: server.URL + "/v1", APIKey: "xai-key",
		Models: []string{"embed-public"}, ModelAliases: map[string]string{"embed-public": "v1"}, Capabilities: []string{"embeddings"},
	}}}).(*Router)
	request := openai.EmbeddingRequest{
		Model: "embed-public", Input: []any{[]any{11.0, 12.0}, []any{13.0}}, EncodingFormat: "base64", Dimensions: &dimensions, User: "user-1",
	}
	response, err := router.Embeddings(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, EmbeddingRequest: &request})
	if err != nil || !response.UsageReported || response.Usage.TotalTokens != 3 || len(response.Data) != 2 || response.Data[0].EmbeddingBase64 != encodedVector {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestXAIImageGenerationAndEditContracts(t *testing.T) {
	const ticks = int64(200_000_001)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer xai-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/v1/images/generations":
			if body["resolution"] != "2k" || body["aspect_ratio"] != "19.5:9" || body["quality"] != "medium" || body["n"] != float64(2) {
				t.Fatalf("generation body=%#v", body)
			}
			_, _ = fmt.Fprintf(w, `{"data":[{"url":"https://images.example/1.jpeg","mime_type":"image/jpeg"},{"url":"https://images.example/2.jpeg","mime_type":"image/jpeg"}],"usage":{"cost_in_usd_ticks":%d}}`, ticks)
		case "/v1/images/edits":
			image, ok := body["image"].(map[string]any)
			if !ok || image["type"] != "image_url" || image["url"] != "data:image/png;base64,iVBORw0KGgpmaXh0dXJl" || body["images"] != nil {
				t.Fatalf("edit body=%#v", body)
			}
			_, _ = fmt.Fprintf(w, `{"data":[{"url":"https://images.example/edit.jpeg","mime_type":"image/jpeg"}],"usage":{"cost_in_usd_ticks":%d}}`, ticks)
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewXAI(server.URL+"/v1", "xai-key", false)
	n := 2
	generated, err := client.GenerateImage(t.Context(), openai.ImageGenerationRequest{Model: "grok-imagine-image-2.0", Prompt: "draw", N: &n, Quality: "medium", Resolution: "2K", AspectRatio: "19.5:9"})
	if err != nil || len(generated.Data) != 2 || generated.Usage == nil || generated.Usage.ProviderCostUSDTicks == nil || *generated.Usage.ProviderCostUSDTicks != ticks {
		t.Fatalf("generated=%+v err=%v", generated, err)
	}
	image, err := openai.ParseDataImageURL("data:image/png;base64,iVBORw0KGgpmaXh0dXJl")
	if err != nil {
		t.Fatal(err)
	}
	edited, err := client.EditImage(t.Context(), openai.ImageEditRequest{Model: "grok-imagine-image-2.0", Prompt: "edit", Images: []openai.ImageAttachment{image}, Quality: "auto"})
	if err != nil || len(edited.Data) != 1 || edited.Usage == nil || edited.Usage.ProviderCostUSDTicks == nil || *edited.Usage.ProviderCostUSDTicks != ticks {
		t.Fatalf("edited=%+v err=%v", edited, err)
	}
}

func TestXAIImagesFailClosedBeforeAndAfterHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = fmt.Fprint(w, `{"data":[{"url":"https://images.example/1.jpeg"}],"usage":{"input_tokens":1,"total_tokens":1}}`)
	}))
	defer server.Close()
	client := NewXAI(server.URL, "key", false)
	if _, err := client.GenerateImage(t.Context(), openai.ImageGenerationRequest{Model: "image", Prompt: "draw", Quality: "high"}); !xaiFailure(err, "quality", "invalid_request") || calls != 0 {
		t.Fatalf("unsupported quality err=%v calls=%d", err, calls)
	}
	if _, err := client.GenerateImage(t.Context(), openai.ImageGenerationRequest{Model: "image", Prompt: "draw"}); err == nil || calls != 1 {
		t.Fatalf("missing exact cost err=%v calls=%d", err, calls)
	}
}

func TestXAITranscriptionContract(t *testing.T) {
	audio := mistralWAVAttachment(1250)
	audio.Filename = "sample.wav"
	expectedAudio, err := base64.StdEncoding.DecodeString(audio.Data)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/stt" || r.Header.Get("Authorization") != "Bearer xai-key" {
			t.Fatalf("unexpected request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		languageOffset := bytes.Index(rawBody, []byte(`name="language"`))
		fileOffset := bytes.Index(rawBody, []byte(`name="file"`))
		if languageOffset < 0 || fileOffset < 0 || languageOffset >= fileOffset {
			t.Fatalf("multipart fields are not before file: language=%d file=%d", languageOffset, fileOffset)
		}
		r.Body = io.NopCloser(bytes.NewReader(rawBody))
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("language") != "en" || !slices.Equal(r.MultipartForm.Value["keyterm"], []string{"Codex", "Grok"}) || r.FormValue("model") != "" {
			t.Fatalf("form=%v", r.MultipartForm.Value)
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		contents, _ := io.ReadAll(file)
		if header.Filename != "sample.wav" || string(contents) != string(expectedAudio) {
			t.Fatalf("file=%q contents=%d", header.Filename, len(contents))
		}
		_, _ = fmt.Fprint(w, `{"text":"hello world","language":"en","duration":1.251,"words":[{"text":"hello","start":0,"end":0.5},{"text":"world","start":0.5,"end":1.251}]}`)
	}))
	defer server.Close()
	client := NewXAI(server.URL+"/v1", "xai-key", false)
	request := openai.AudioTranscriptionRequest{Model: "speech", File: audio, Language: "en", Keywords: []string{"Codex", "Grok"}}
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "xai-speech", Type: "xai", BaseURL: server.URL + "/v1", APIKey: "xai-key",
		Models: []string{"speech"}, ModelAliases: map[string]string{"speech": "ignored-by-native-stt"}, Capabilities: []string{"audio_transcription"},
	}}}).(*Router)
	response, err := router.TranscribeAudio(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model}, AudioTranscriptionRequest: &request})
	if err != nil || response.Text != "hello world" || response.Usage == nil || response.Usage.Type != "duration" || response.Usage.InputAudioMilliseconds != 1251 || len(response.Words) != 2 || response.Words[0].Word != "hello" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if reserved, err := client.ReserveAudioMilliseconds(request); err != nil || reserved != 1250 {
		t.Fatalf("reserved=%d err=%v", reserved, err)
	}
}

func TestXAITranscriptionRejectsUnsupportedParametersAndInvalidResponse(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = fmt.Fprint(w, `{"text":"hello","language":"en","duration":0}`)
	}))
	defer server.Close()
	client := NewXAI(server.URL, "key", false)
	base := openai.AudioTranscriptionRequest{Model: "speech", File: mistralWAVAttachment(1250)}
	temperature := 0.1
	tests := []struct {
		name  string
		param string
		code  string
		apply func(*openai.AudioTranscriptionRequest)
	}{
		{name: "prompt", param: "prompt", code: "unsupported_parameter", apply: func(r *openai.AudioTranscriptionRequest) { r.Prompt = "context" }},
		{name: "format", param: "response_format", code: "unsupported_parameter", apply: func(r *openai.AudioTranscriptionRequest) { r.ResponseFormat = "json" }},
		{name: "temperature", param: "temperature", code: "unsupported_parameter", apply: func(r *openai.AudioTranscriptionRequest) { r.Temperature = &temperature }},
		{name: "timestamps", param: "timestamp_granularities", code: "unsupported_parameter", apply: func(r *openai.AudioTranscriptionRequest) {
			r.ResponseFormat = "verbose_json"
			r.TimestampGranularities = []string{"word"}
		}},
		{name: "long keyword", param: "keywords", code: "invalid_request", apply: func(r *openai.AudioTranscriptionRequest) { r.Keywords = []string{strings.Repeat("x", 51)} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.apply(&request)
			_, err := client.TranscribeAudio(t.Context(), request)
			if !xaiFailure(err, test.param, test.code) || calls != 0 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
	if _, err := client.TranscribeAudio(t.Context(), base); err == nil || calls != 1 {
		t.Fatalf("invalid response err=%v calls=%d", err, calls)
	}
}

func TestXAISpeechContractAndRouting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/tts" || r.Header.Get("Authorization") != "Bearer xai-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		format, ok := body["output_format"].(map[string]any)
		if !ok || body["text"] != "hello" || body["voice_id"] != "eve" || body["language"] != "en" || body["model"] != nil || body["speed"] != 1.25 || format["codec"] != "wav" {
			t.Fatalf("body=%#v", body)
		}
		w.Header().Set("Content-Type", "audio/wav; rate=24000")
		_, _ = w.Write([]byte("RIFFaudio"))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "xai-voice", Type: "xai", BaseURL: server.URL + "/v1", APIKey: "xai-key",
		Models: []string{"speech"}, ModelAliases: map[string]string{"speech": "ignored-by-native-tts"}, Capabilities: []string{"audio_speech"},
	}}}).(*Router)
	speed := 1.25
	request := openai.AudioSpeechRequest{Model: "speech", Input: "hello", Voice: "eve", Language: "en", ResponseFormat: "wav", Speed: &speed, StreamFormat: "audio"}
	response, err := router.GenerateSpeech(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: request.Model, Messages: []openai.Message{{Role: "user", Content: request.Input}}}, AudioSpeechRequest: &request})
	if err != nil || string(response.Data) != "RIFFaudio" || response.ContentType != "audio/wav" || response.Model != "ignored-by-native-tts" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestXAISpeechDefaultsLanguageAndRejectsUnsupportedParameters(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["language"] != "auto" || body["output_format"] != nil {
			t.Fatalf("body=%#v err=%v", body, err)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()
	client := NewXAI(server.URL, "key", false)
	base := openai.AudioSpeechRequest{Model: "speech", Input: "hello", Voice: "eve"}
	tooFast := 1.51
	for _, test := range []struct {
		name, param, code string
		apply             func(*openai.AudioSpeechRequest)
	}{
		{name: "instructions", param: "instructions", code: "unsupported_parameter", apply: func(r *openai.AudioSpeechRequest) { r.Instructions = "whisper" }},
		{name: "format", param: "response_format", code: "invalid_request", apply: func(r *openai.AudioSpeechRequest) { r.ResponseFormat = "opus" }},
		{name: "speed", param: "speed", code: "invalid_request", apply: func(r *openai.AudioSpeechRequest) { r.Speed = &tooFast }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.apply(&request)
			_, err := client.GenerateSpeech(t.Context(), request)
			if !xaiFailure(err, test.param, test.code) || calls != 0 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
	response, err := client.GenerateSpeech(t.Context(), base)
	if err != nil || string(response.Data) != "audio" || calls != 1 {
		t.Fatalf("response=%+v err=%v calls=%d", response, err, calls)
	}
}

func TestXAIVideoGenerationRetrievalAndContentContracts(t *testing.T) {
	const ticks = int64(500_000_000)
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos/generations":
			if r.Header.Get("Authorization") != "Bearer xai-key" || r.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("headers=%v", r.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			image, ok := body["image"].(map[string]any)
			if !ok || body["model"] != "grok-video" || body["prompt"] != "a cat" || body["duration"] != float64(8) || body["aspect_ratio"] != "9:16" || body["resolution"] != "720p" || image["url"] != "https://images.example/cat.png" {
				t.Fatalf("body=%#v", body)
			}
			_, _ = io.WriteString(w, `{"request_id":"video_request_1"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/video_request_1":
			if r.Header.Get("Authorization") != "Bearer xai-key" {
				t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
			}
			_, _ = fmt.Fprintf(w, `{"status":"done","video":{"url":%q,"duration":8},"model":"grok-video","usage":{"cost_in_usd_ticks":%d},"progress":100}`, server.URL+"/asset.mp4", ticks)
		case r.Method == http.MethodGet && r.URL.Path == "/asset.mp4":
			if r.Header.Get("Authorization") != "" {
				t.Fatal("provider credential leaked to content host")
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = io.WriteString(w, "video-bytes")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos/edits":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			video, ok := body["video"].(map[string]any)
			if !ok || body["prompt"] != "add snow" || body["model"] != nil || video["url"] != server.URL+"/asset.mp4" {
				t.Fatalf("edit body=%#v", body)
			}
			_, _ = io.WriteString(w, `{"request_id":"video_edit_1"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos/extensions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			video, ok := body["video"].(map[string]any)
			if !ok || body["prompt"] != "continue forward" || body["duration"] != float64(12) || body["model"] != nil || video["url"] != server.URL+"/asset.mp4" {
				t.Fatalf("extension body=%#v", body)
			}
			_, _ = io.WriteString(w, `{"request_id":"video_extension_1"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewXAI(server.URL+"/v1", "xai-key", false)
	client.compatible.client = server.Client()
	client.contentClient = server.Client()
	created, err := client.CreateVideo(t.Context(), openai.VideoCreateRequest{Model: "grok-video", Prompt: "a cat", Seconds: "8", Size: "720x1280", InputReference: &openai.VideoInputReference{ImageURL: "https://images.example/cat.png"}})
	if err != nil || created.ID != "video_request_1" || created.Status != "queued" || created.Seconds != "8" || created.Size != "720x1280" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	retrieved, err := client.RetrieveVideo(t.Context(), created.ID)
	if err != nil || retrieved.Status != "completed" || retrieved.Progress != 100 || retrieved.ProviderCostUSDTicks == nil || *retrieved.ProviderCostUSDTicks != ticks || retrieved.ContentURL == "" {
		t.Fatalf("retrieved=%+v err=%v", retrieved, err)
	}
	content, err := client.DownloadVideoContent(t.Context(), created.ID, "video")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Body.Close()
	data, err := io.ReadAll(content.Body)
	if err != nil || string(data) != "video-bytes" || content.ContentType != "video/mp4" {
		t.Fatalf("content=%q type=%q err=%v", data, content.ContentType, err)
	}
	edited, err := client.RemixVideo(t.Context(), created.ID, openai.VideoRemixRequest{Prompt: "add snow"})
	if err != nil || edited.ID != "video_edit_1" || edited.Status != "queued" || edited.RemixedFromVideoID == nil || *edited.RemixedFromVideoID != created.ID || edited.Seconds != "8" {
		t.Fatalf("edited=%+v err=%v", edited, err)
	}
	extended, err := client.ExtendVideo(t.Context(), created.ID, openai.VideoExtendRequest{Prompt: "continue forward", Seconds: "12"})
	if err != nil || extended.ID != "video_extension_1" || extended.Status != "queued" || extended.RemixedFromVideoID == nil || *extended.RemixedFromVideoID != created.ID || extended.Seconds != "12" {
		t.Fatalf("extended=%+v err=%v", extended, err)
	}
	deleted, err := client.DeleteVideo(t.Context(), created.ID)
	if err != nil || !deleted.Deleted || deleted.ID != created.ID {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
}

func TestXAIVideoRoutingAliasAndValidation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "grok-imagine-video" || body["duration"] != float64(4) || body["aspect_ratio"] != "16:9" || body["resolution"] != "720p" {
			t.Fatalf("body=%#v err=%v", body, err)
		}
		_, _ = io.WriteString(w, `{"request_id":"video_alias_1"}`)
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{
		Name: "xai-video", Type: "xai", BaseURL: server.URL + "/v1", APIKey: "key", Models: []string{"video-public"},
		ModelAliases: map[string]string{"video-public": "grok-imagine-video"}, Capabilities: []string{"video"},
	}}}).(*Router)
	video, _, err := router.CreateVideo(t.Context(), modules.RequestContext{}, openai.VideoCreateRequest{Model: "video-public", Prompt: "a cat"}, nil)
	if err != nil || video.Model != "video-public" || video.Size != "1280x720" || calls != 1 {
		t.Fatalf("video=%+v calls=%d err=%v", video, calls, err)
	}
	client := NewXAI(server.URL, "key", false)
	for _, request := range []openai.VideoCreateRequest{
		{Model: "m", Prompt: "x", Size: "1792x1024"},
		{Model: "m", Prompt: "x", InputReference: &openai.VideoInputReference{FileID: "file_1"}},
		{Model: "m", Prompt: "x", InputReference: &openai.VideoInputReference{ImageURL: "http://images.example/a.png"}},
	} {
		if _, err := client.CreateVideo(t.Context(), request); err == nil || calls != 1 {
			t.Fatalf("request=%+v err=%v calls=%d", request, err, calls)
		}
	}
}

func TestXAIVideoRejectsInvalidTerminalResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"done","video":{"url":"https://videos.example/a.mp4","duration":4},"model":"grok","progress":100}`)
	}))
	defer server.Close()
	client := NewXAI(server.URL+"/v1", "key", false)
	if _, err := client.RetrieveVideo(t.Context(), "video_1"); err == nil {
		t.Fatal("completed response without exact provider cost was accepted")
	}
}

func TestXAIRejectsUnsupportedEmbeddingParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewXAI(server.URL, "key", false)
	tests := []struct {
		name    string
		request openai.EmbeddingRequest
		param   string
	}{
		{name: "metadata", request: openai.EmbeddingRequest{Metadata: map[string]string{"tenant": "one"}}, param: "metadata"},
		{name: "input type", request: openai.EmbeddingRequest{InputType: "query"}, param: "input_type"},
		{name: "output dtype", request: openai.EmbeddingRequest{OutputDType: "int8"}, param: "output_dtype"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.request.Model = "v1"
			test.request.Input = "hello"
			_, err := client.Embeddings(t.Context(), test.request)
			if !xaiFailure(err, test.param, "unsupported_parameter") || called {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}

func xaiFailure(err error, param, code string) bool {
	var failure *Error
	return errors.As(err, &failure) && failure.Provider == "xai" && failure.Param == param && failure.UpstreamCode == code
}
