package modules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestRemoteAuthIsTheOnlyModuleReceivingBearerToken(t *testing.T) {
	const token = "client-bearer-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request AuthRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Token != token {
			t.Fatalf("auth did not receive token: %q", request.Token)
		}
		_ = json.NewEncoder(w).Encode(AuthResponse{UserID: "user-1", TeamID: "team-1", OrganizationID: "org-1", CredentialID: "fingerprint", CredentialAlias: "clinical-prod", Tags: []string{"hipaa"}, AccessGroupIDs: []string{"regulated"}, AllowedModels: []string{"gpt-*"}, AllowedTools: []string{"mcp.weather.*"}, RateLimitRPM: 5, RateLimitTPM: 100})
	}))
	defer server.Close()

	req := RequestContext{APIKey: token}
	if err := NewRemoteAuthModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.CredentialID != "fingerprint" {
		t.Fatalf("unexpected credential id: %q", req.CredentialID)
	}
	if req.CredentialAlias != "clinical-prod" || len(req.Tags) != 1 || req.Tags[0] != "hipaa" || len(req.AccessGroupIDs) != 1 || req.AccessGroupIDs[0] != "regulated" {
		t.Fatalf("credential policy matching metadata was not propagated: %+v", req)
	}
	if req.APIKey != "" {
		t.Fatal("auth did not clear bearer token after successful authorization")
	}
	if req.TeamID != "team-1" || req.OrganizationID != "org-1" || req.RateLimitRPM != 5 || req.RateLimitTPM != 100 || len(req.AllowedModels) != 1 || len(req.AllowedTools) != 1 {
		t.Fatalf("auth policy was not propagated: %+v", req)
	}
}

func TestRemoteAnonymizerDoesNotReceiveBearerOrIdentity(t *testing.T) {
	server := contractServer(t, AnonymizeResponse{Messages: []openai.Message{{Role: "user", Content: "masked"}}})
	defer server.Close()
	req := sensitiveContext()
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.UserID != "user-1" || req.CredentialID != "safe-fingerprint" {
		t.Fatalf("anonymizer changed protected identity fields: %+v", req)
	}
}

func TestRemoteAnonymizerAppliesEffectiveMode(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request AnonymizeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if strings.Join(request.Rules, ",") != "email,phone" {
			t.Fatalf("rules=%v", request.Rules)
		}
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Messages: request.Messages})
	}))
	defer server.Close()
	module := NewRemoteAnonymizerModule(true, server.URL)
	req := RequestContext{Metadata: map[string]string{"provider.modules.anonymizer.mode": "custom", "provider.modules.anonymizer.rules": "email,phone"}}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	req.Metadata["provider.modules.anonymizer.mode"] = "disabled"
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestRemoteAnonymizerRejectsEmptyEffectiveRules(t *testing.T) {
	req := RequestContext{Metadata: map[string]string{"provider.modules.anonymizer.mode": "custom"}}
	if err := NewRemoteAnonymizerModule(true, "http://127.0.0.1:1").Handle(context.Background(), &req); err == nil {
		t.Fatal("expected empty custom rule set to fail closed")
	}
}

func TestRemoteAnonymizerListsConfiguredRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/rules" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{"email", "phone"}})
	}))
	defer server.Close()
	rules, err := NewRemoteAnonymizerModule(true, server.URL+"/anonymize").RuleNames(context.Background())
	if err != nil || strings.Join(rules, ",") != "email,phone" {
		t.Fatalf("rules=%v err=%v", rules, err)
	}
}

func TestRemoteAnonymizerPreservesToolCallContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request AnonymizeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) != 1 || len(request.Messages[0].ToolCalls) != 1 {
			t.Fatalf("tool call was not sent to anonymizer: %+v", request)
		}
		request.Messages[0].ToolCalls[0].Function.Arguments = `{"email":"{{EMAIL_1}}"}`
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Messages: request.Messages, Replacements: map[string]string{"{{EMAIL_1}}": "user@example.com"}})
	}))
	defer server.Close()
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{
		Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call-1", Type: "function", Function: openai.FunctionCall{Name: "send", Arguments: `{"email":"user@example.com"}`}}},
	}}}}
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if got := req.Request.Messages[0].ToolCalls[0].Function.Arguments; got != `{"email":"{{EMAIL_1}}"}` {
		t.Fatalf("unexpected masked tool arguments: %s", got)
	}
}

func TestRemoteAnonymizerReceivesOnlyTextProjectionForImages(t *testing.T) {
	image := "data:image/png;base64,aW1hZ2Utc2VjcmV0"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request AnonymizeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(request)
		if strings.Contains(string(encoded), "aW1hZ2Utc2VjcmV0") {
			t.Fatal("anonymizer received base64 image payload")
		}
		request.Messages[0].Content.([]any)[0].(map[string]any)["text"] = "masked"
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Messages: request.Messages})
	}))
	defer server.Close()
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "secret"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": image}},
	}}}}}
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	content := req.Request.Messages[0].Content.([]any)
	if content[0].(map[string]any)["text"] != "masked" {
		t.Fatalf("masked text was not merged: %+v", content)
	}
	if content[1].(map[string]any)["image_url"].(map[string]any)["url"] != image {
		t.Fatal("original image payload was not restored")
	}
}

func TestRemoteBillingReceivesCredentialIDButNotBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, found := body["api_key"]; found {
			t.Fatal("billing request contains api_key")
		}
		if body["credential_id"] != "safe-fingerprint" {
			t.Fatalf("unexpected credential id: %v", body["credential_id"])
		}
		if body["team_id"] != "team-1" {
			t.Fatalf("unexpected team id: %v", body["team_id"])
		}
		if body["organization_id"] != "org-1" {
			t.Fatalf("unexpected organization id: %v", body["organization_id"])
		}
		if tags, ok := body["tags"].([]any); !ok || len(tags) != 2 || tags[0] != "production" {
			t.Fatalf("unexpected billing tags: %v", body["tags"])
		}
		if body["phase"] != "reserve" {
			t.Fatalf("expected reserve phase, got %v", body["phase"])
		}
		for _, forbidden := range []string{"request", "response", "completion_request", "completion_response", "response_request", "responses_response", "messages", "anonymization_values"} {
			if _, found := body[forbidden]; found {
				t.Fatalf("billing request contains prompt or full context field %q", forbidden)
			}
		}
		_ = json.NewEncoder(w).Encode(UsageResponse{})
	}))
	defer server.Close()
	req := sensitiveContext()
	req.OrganizationID = "org-1"
	req.Tags = []string{"production", "cost-center-a"}
	if err := NewRemoteBillingModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteBillingLifecyclePhases(t *testing.T) {
	phases := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request UsageRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		phases = append(phases, request.Phase)
		_ = json.NewEncoder(w).Encode(UsageResponse{})
	}))
	defer server.Close()
	module := NewRemoteBillingModule(true, server.URL)
	req := sensitiveContext()
	req.RequestID = "req-lifecycle"
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	req.Response = &openai.ChatCompletionResponse{}
	if err := module.HandlePostResponse(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	req.Response = nil
	if err := module.HandleFailure(context.Background(), &req, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 3 || phases[0] != "reserve" || phases[1] != "commit" || phases[2] != "cancel" {
		t.Fatalf("unexpected lifecycle phases: %v", phases)
	}
}

func TestRemoteBillingCarriesOnlyValidatedRuntimePricingFields(t *testing.T) {
	req := sensitiveContext()
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["model_catalog.version"] = "runtime-v2"
	req.Metadata["model_catalog.pricing_key"] = "endpoint/model"
	req.Metadata["model_catalog.input_cost_per_1m"] = "1.5"
	req.Metadata["model_catalog.output_cost_per_1m"] = "3"
	req.Metadata["model_catalog.training_cost_per_1m"] = "5"
	req.Metadata["model_catalog.search_cost_per_1k"] = "10"
	req.Metadata["model_catalog.character_cost_per_1m"] = "15"
	req.Metadata["model_catalog.page_cost_per_1k"] = "100"
	req.Metadata["model_catalog.audio_cost_per_minute"] = "0.12"
	req.Metadata["model_catalog.video_cost_per_second"] = "0.25"
	req.Metadata["model_catalog.image_cost_per_unit"] = "0.4"
	req.InputCharacters = 4096
	req.InputPages = 4
	req.InputAudioMilliseconds = 90000
	req.VideoSeconds = 12
	req.ToolRequests = 3
	req.TrainingTokens = 1000
	req.Metadata["model_catalog.currency"] = "USD"
	req.Metadata["provider.id"] = "azure-open-ai"
	req.Metadata["provider.first_token_latency_ms"] = "87"
	req.Metadata["provider.retry_count"] = "2"
	req.Metadata["provider.fallback_count"] = "1"
	req.Metadata["provider.cache.kind"] = "semantic"
	req.Metadata["provider.original_model"] = "gpt-5.6-luna"
	req.Request.Model = "fallback-group"
	req.Response = &openai.ChatCompletionResponse{Model: "gpt-5.6-luna-2026-07-09", Usage: openai.Usage{PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11, PromptTokensDetails: &openai.PromptTokenDetails{CachedTokens: 6, CacheCreationTokens: 2}, CompletionTokensDetails: &openai.CompletionTokenDetails{AcceptedPredictionTokens: 2, RejectedPredictionTokens: 1}}}
	request := billingRequest(&req)
	if request.CatalogVersion != "runtime-v2" || request.PricingKey != "endpoint/model" || request.InputCostPer1M != "1.5" || request.TrainingCostPer1M != "5" || request.TrainingTokens != 1000 || request.SearchCostPer1K != "10" || request.CharacterCostPer1M != "15" || request.PageCostPer1K != "100" || request.AudioCostPerMinute != "0.12" || request.VideoCostPerSecond != "0.25" || request.ImageCostPerUnit != "0.4" || request.InputCharacters != 4096 || request.InputPages != 4 || request.InputAudioMilliseconds != 90000 || request.VideoSeconds != 12 || request.ToolRequests != 3 || request.Currency != "USD" {
		t.Fatalf("pricing snapshot=%+v", request)
	}
	if request.ProviderID != "azure-open-ai" || request.Model != "gpt-5.6-luna" || request.UpstreamModel != "gpt-5.6-luna-2026-07-09" {
		t.Fatalf("usage identity=%+v", request)
	}
	if request.FirstTokenLatencyMS != "87" || request.RetryCount != 2 || request.FallbackCount != 1 || request.CacheKind != "semantic" || request.UsageEstimated {
		t.Fatalf("usage observability=%+v", request)
	}
	if request.CacheReadInputTokens != 6 || request.CacheWriteInputTokens != 2 {
		t.Fatalf("cache token usage=%+v", request)
	}
	if request.InputTokens != 8 || request.OutputTokens != 3 || request.TotalTokens != 11 {
		t.Fatalf("prediction details were added twice to billing totals: %+v", request)
	}
}

func TestRemoteBillingAcceptsExactCostOnlyFromXAI(t *testing.T) {
	ticks := int64(37_756_000)
	req := sensitiveContext()
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["provider.endpoint.type"] = "xai"
	req.Response = &openai.ChatCompletionResponse{Usage: openai.Usage{PromptTokens: 1, TotalTokens: 1, ProviderCostUSDTicks: &ticks}}
	if got := billingRequest(&req).ProviderCostUSDTicks; got == nil || *got != ticks {
		t.Fatalf("trusted ticks=%v", got)
	}
	req.Metadata["provider.endpoint.type"] = "openai-compatible"
	if got := billingRequest(&req).ProviderCostUSDTicks; got != nil {
		t.Fatalf("untrusted provider cost was forwarded: %d", *got)
	}
}

func TestRemoteBillingUsesOnlyTrainingTokensForFineTuning(t *testing.T) {
	req := sensitiveContext()
	req.Request.Model = "base-model"
	req.TrainingTokens = 2500
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["gateway.api_type"] = "fine_tuning"
	request := billingRequest(&req)
	if request.APIType != "fine_tuning" || request.InputTokens != 0 || request.OutputTokens != 0 || request.TotalTokens != 0 || request.TrainingTokens != 2500 || !request.UsageEstimated {
		t.Fatalf("request=%+v", request)
	}
	req.Metadata["gateway.fine_tuning_usage_exact"] = "true"
	if exact := billingRequest(&req); exact.UsageEstimated || exact.TrainingTokens != 2500 {
		t.Fatalf("exact request=%+v", exact)
	}
}

func TestRemoteBillingUsesOnlyDurationForVideo(t *testing.T) {
	req := sensitiveContext()
	req.VideoSeconds = 8
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["gateway.api_type"] = "video"
	reserve := billingRequest(&req)
	if reserve.APIType != "video" || reserve.VideoSeconds != 8 || reserve.InputTokens != 0 || reserve.OutputTokens != 0 || reserve.TotalTokens != 0 || !reserve.UsageEstimated {
		t.Fatalf("reserve=%+v", reserve)
	}
	req.Metadata["gateway.video_usage_exact"] = "true"
	ticks := int64(500_000_000)
	req.VideoProviderCostUSDTicks = &ticks
	req.Metadata["provider.endpoint.type"] = "xai"
	commit := billingRequest(&req)
	if commit.VideoSeconds != 8 || commit.UsageEstimated || commit.ProviderCostUSDTicks == nil || *commit.ProviderCostUSDTicks != ticks {
		t.Fatalf("commit=%+v", commit)
	}
	req.Metadata["provider.endpoint.type"] = "openai-compatible"
	if untrusted := billingRequest(&req); untrusted.ProviderCostUSDTicks != nil {
		t.Fatalf("untrusted provider cost=%v", *untrusted.ProviderCostUSDTicks)
	}
}

func TestRemoteBillingClassifiesCachedContentAndCacheWriteTokens(t *testing.T) {
	req := RequestContext{
		Request:  openai.ChatCompletionRequest{Model: "gemini"},
		Response: &openai.ChatCompletionResponse{Usage: openai.Usage{PromptTokens: 23, TotalTokens: 23, PromptTokensDetails: &openai.PromptTokenDetails{CacheWriteTokens: 23}}},
		Metadata: map[string]string{"gateway.api_type": "cached_content"},
	}
	request := billingRequest(&req)
	if request.APIType != "cached_content" || request.InputTokens != 23 || request.TotalTokens != 23 || request.CacheWriteInputTokens != 23 || request.UsageEstimated {
		t.Fatalf("cached content billing=%+v", request)
	}
}

func TestRemoteBillingUsesRealtimeReserveAndExactUsage(t *testing.T) {
	req := sensitiveContext()
	req.Request.Model = "realtime-model"
	req.Usage = &openai.Usage{PromptTokens: 31, CompletionTokens: 42, TotalTokens: 73}
	if req.Metadata == nil {
		req.Metadata = map[string]string{}
	}
	req.Metadata["gateway.api_type"] = "realtime"
	reserve := billingRequest(&req)
	if reserve.APIType != "realtime" || reserve.InputTokens != 31 || reserve.OutputTokens != 42 || reserve.TotalTokens != 73 || !reserve.UsageEstimated {
		t.Fatalf("reserve=%+v", reserve)
	}
	req.Usage = &openai.Usage{PromptTokens: 9, CompletionTokens: 4, TotalTokens: 13}
	req.Metadata["gateway.realtime_usage_exact"] = "true"
	commit := billingRequest(&req)
	if commit.APIType != "realtime" || commit.InputTokens != 9 || commit.OutputTokens != 4 || commit.TotalTokens != 13 || commit.UsageEstimated {
		t.Fatalf("commit=%+v", commit)
	}
	req.Usage = &openai.Usage{}
	zero := billingRequest(&req)
	if zero.TotalTokens != 0 || zero.UsageEstimated {
		t.Fatalf("exact zero usage was replaced: %+v", zero)
	}
}

func TestRemoteBillingSettlesAudioSpeechWithExactCharacters(t *testing.T) {
	request := openai.AudioSpeechRequest{Provider: "speech", Model: "tts", Input: "Привет 👋", Voice: "alloy"}
	req := RequestContext{
		RequestID: "speech-request", Request: openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model},
		AudioSpeechRequest: &request, InputCharacters: request.InputCharacters(), Metadata: map[string]string{"gateway.api_type": "audio_speech"},
	}
	reserve := billingRequest(&req)
	if reserve.Phase != "reserve" || reserve.APIType != "audio_speech" || reserve.InputCharacters != 8 || reserve.OutputTokens != 0 || reserve.TotalTokens != openai.AudioSpeechReserveTokens(request) {
		t.Fatalf("reserve=%+v", reserve)
	}
	req.AudioSpeechResponse = &openai.AudioSpeechResponse{Data: []byte("audio"), ContentType: "audio/mpeg", Model: "tts-versioned"}
	commit := billingRequest(&req)
	if commit.Phase != "commit" || commit.InputCharacters != 8 || commit.UpstreamModel != "tts-versioned" || !commit.UsageEstimated {
		t.Fatalf("commit=%+v", commit)
	}
	usage := openai.AudioSpeechUsage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}
	req.AudioSpeechResponse = &openai.AudioSpeechResponse{ContentType: "audio/mpeg", Model: "tts-streamed", Usage: &usage}
	commit = billingRequest(&req)
	if commit.InputCharacters != 8 || commit.UpstreamModel != "tts-streamed" || commit.InputTokens != 3 || commit.OutputTokens != 2 || commit.TotalTokens != 5 || commit.UsageEstimated {
		t.Fatalf("streaming commit=%+v", commit)
	}
}

func TestRemoteBillingSettlesStandaloneSearchAsOneUnit(t *testing.T) {
	search := openai.SearchRequest{Provider: "search", SearchToolName: "web-search", Query: "gateway"}
	req := RequestContext{
		RequestID: "search-request", Request: openai.ChatCompletionRequest{Provider: search.Provider, Model: "web-search"},
		SearchRequest: &search, Metadata: map[string]string{"gateway.api_type": "search"},
	}
	reserve := billingRequest(&req)
	if reserve.Phase != "reserve" || reserve.APIType != "search" || reserve.Model != "web-search" || reserve.SearchRequests != 1 || reserve.SearchRequestsEstimated || reserve.InputTokens != 0 || reserve.OutputTokens != 0 || reserve.TotalTokens != 0 || reserve.UsageEstimated {
		t.Fatalf("reserve=%+v", reserve)
	}
	req.SearchResponse = &openai.SearchResponse{Object: "search", Model: "web-search-v2", Usage: openai.Usage{SearchRequests: 1}}
	commit := billingRequest(&req)
	if commit.Phase != "commit" || commit.UpstreamModel != "web-search-v2" || commit.SearchRequests != 1 || commit.SearchRequestsEstimated || commit.TotalTokens != 0 || commit.UsageEstimated {
		t.Fatalf("commit=%+v", commit)
	}
}

func TestRemoteBillingCarriesMCPToolAccounting(t *testing.T) {
	req := RequestContext{RequestID: "tool-call", ToolRequests: 1, Metadata: map[string]string{"gateway.api_type": "mcp_tools_call"}}
	request := billingRequest(&req)
	if request.APIType != "mcp_tools_call" || request.ToolRequests != 1 {
		t.Fatalf("tool accounting=%+v", request)
	}
}

func TestRemoteBillingReservesAndSettlesOCRPages(t *testing.T) {
	ocr := openai.OCRRequest{Provider: "mistral", Model: "ocr-document", Document: openai.OCRDocument{Type: "document_url", DocumentURL: "https://example.test/document.pdf"}, Pages: "0,2-4"}
	req := RequestContext{
		RequestID: "ocr-request", Request: openai.ChatCompletionRequest{Provider: ocr.Provider, Model: ocr.Model},
		OCRRequest: &ocr, InputPages: ocr.ReservePages(), Metadata: map[string]string{"gateway.api_type": "ocr"},
	}
	reserve := billingRequest(&req)
	if reserve.Phase != "reserve" || reserve.APIType != "ocr" || reserve.Model != "ocr-document" || reserve.InputPages != 4 || reserve.OutputTokens != 0 || reserve.TotalTokens != ocr.InputTokens() || !reserve.UsageEstimated {
		t.Fatalf("reserve=%+v", reserve)
	}
	req.OCRResponse = &openai.OCRResponse{Model: "ocr-document-2026", Pages: []json.RawMessage{json.RawMessage(`{"index":0,"markdown":"text"}`)}, UsageInfo: openai.OCRUsageInfo{PagesProcessed: 1}}
	commit := billingRequest(&req)
	if commit.Phase != "commit" || commit.UpstreamModel != "ocr-document-2026" || commit.InputPages != 1 || commit.TotalTokens != ocr.InputTokens() || !commit.UsageEstimated {
		t.Fatalf("commit=%+v", commit)
	}
}

func TestRemoteBillingReservesAndCommitsProviderSearchUsage(t *testing.T) {
	req := sensitiveContext()
	req.Request.WebSearchOptions = &openai.ChatWebSearchOptions{SearchContextSize: "medium"}
	reserved := billingRequest(&req)
	if reserved.SearchRequests != openai.WebSearchMaxUses || !reserved.SearchRequestsEstimated {
		t.Fatalf("search reserve=%+v", reserved)
	}
	req.Response = &openai.ChatCompletionResponse{Usage: openai.Usage{SearchRequests: 2}}
	committed := billingRequest(&req)
	if committed.SearchRequests != 2 || committed.SearchRequestsEstimated {
		t.Fatalf("search commit=%+v", committed)
	}
}

func TestRemoteBillingReservesAndCommitsGoogleMapsUsage(t *testing.T) {
	req := sensitiveContext()
	req.Request.GeminiGoogleMaps = true
	reserved := billingRequest(&req)
	if reserved.SearchRequests != 1 || !reserved.SearchRequestsEstimated {
		t.Fatalf("Maps reserve=%+v", reserved)
	}
	req.Response = &openai.ChatCompletionResponse{Usage: openai.Usage{SearchRequests: 1}}
	committed := billingRequest(&req)
	if committed.SearchRequests != 1 || committed.SearchRequestsEstimated {
		t.Fatalf("Maps commit=%+v", committed)
	}
}

func TestRemoteBillingReservesAndCommitsCodeExecutionUsage(t *testing.T) {
	req := sensitiveContext()
	req.Request.AnthropicCodeExecution = true
	if reserved := billingRequest(&req); reserved.ToolRequests != 1 {
		t.Fatalf("code execution reserve=%+v", reserved)
	}
	req.Response = &openai.ChatCompletionResponse{Usage: openai.Usage{ToolRequests: 3, ToolRequestsReported: true}}
	if committed := billingRequest(&req); committed.ToolRequests != 3 {
		t.Fatalf("code execution commit=%+v", committed)
	}
}

func TestRemoteBillingMarksFallbackTokenCountAsEstimated(t *testing.T) {
	req := sensitiveContext()
	req.Metadata = map[string]string{}
	req.Response = &openai.ChatCompletionResponse{Model: "model-without-usage"}
	request := billingRequest(&req)
	if !request.UsageEstimated || request.TotalTokens != request.PromptTokensEstimated {
		t.Fatalf("expected explicitly estimated usage, got %+v", request)
	}
	req.Metadata["provider.cache.status"] = "hit"
	request = billingRequest(&req)
	if request.UsageEstimated || request.TotalTokens != 0 {
		t.Fatalf("cache hit must be exact zero upstream usage, got %+v", request)
	}
}

func TestImageGenerationBillingReserveAndSettlement(t *testing.T) {
	n := 3
	req := &RequestContext{
		Request:                openai.ChatCompletionRequest{Model: "image-model"},
		ImageGenerationRequest: &openai.ImageGenerationRequest{Model: "image-model", Prompt: "draw", N: &n},
		Metadata:               map[string]string{"gateway.api_type": "image_generation"},
	}
	reserved := billingRequest(req)
	if reserved.APIType != "image_generation" || reserved.OutputImages != 3 || reserved.OutputTokens != 3*openai.DefaultOutputTokenReserve || reserved.TotalTokens != reserved.InputTokens+reserved.OutputTokens {
		t.Fatalf("reserve=%+v", reserved)
	}
	req.ImageGenerationResponse = &openai.ImageGenerationResponse{Data: []openai.ImageData{{URL: "one"}, {URL: "two"}}, Usage: &openai.ImageUsage{InputTokens: 7, OutputTokens: 11, TotalTokens: 18}}
	settled := billingRequest(req)
	if settled.Phase != "commit" || settled.OutputImages != 2 || settled.InputTokens != 7 || settled.OutputTokens != 11 || settled.TotalTokens != 18 || settled.UsageEstimated {
		t.Fatalf("settlement=%+v", settled)
	}
	req.ImageGenerationResponse = &openai.ImageGenerationResponse{Usage: &openai.ImageUsage{}}
	settled = billingRequest(req)
	if settled.InputTokens != 0 || settled.OutputTokens != 0 || settled.TotalTokens != 0 || settled.UsageEstimated {
		t.Fatalf("provider-reported zero usage must remain exact: %+v", settled)
	}
	req.ImageGenerationResponse = &openai.ImageGenerationResponse{Data: []openai.ImageData{{URL: "https://example.test/one"}, {URL: "https://example.test/two"}}}
	settled = billingRequest(req)
	if settled.OutputImages != 2 || !settled.UsageEstimated {
		t.Fatalf("unit-priced image response must preserve exact output count and estimated token state: %+v", settled)
	}
}

func TestResponsesImageGenerationBillingReserveAndSettlement(t *testing.T) {
	maxToolCalls := 3
	req := &RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model"},
		ResponseRequest: &openai.ResponseRequest{
			Model: "model", Input: "draw", MaxToolCalls: &maxToolCalls,
			Tools: []openai.ResponseTool{{Type: "image_generation"}},
		},
	}
	reserved := billingRequest(req)
	if reserved.APIType != "responses" || reserved.OutputImages != 3 {
		t.Fatalf("Responses image reserve=%+v", reserved)
	}
	req.ResponsesResponse = &openai.ResponseResponse{
		ID: "resp_image", Model: "model", Status: "completed",
		Output: []openai.ResponseOutputItem{
			{Type: "image_generation_call", Status: "completed", Result: json.RawMessage(`"b25l"`)},
			{Type: "image_generation_call", Status: "failed", Result: json.RawMessage(`null`)},
			{Type: "message", Status: "completed"},
			{Type: "image_generation_call", Status: "completed", Result: json.RawMessage(`"dHdv"`)},
		},
		Usage: openai.ResponseUsage{InputTokens: 7, OutputTokens: 11, TotalTokens: 18},
	}
	settled := billingRequest(req)
	if settled.Phase != "commit" || settled.OutputImages != 2 || settled.TotalTokens != 18 || settled.UsageEstimated {
		t.Fatalf("Responses image settlement=%+v", settled)
	}
	req.ResponsesResponse = nil
	req.ResponseRequest.MaxToolCalls = nil
	if defaultReserve := billingRequest(req); defaultReserve.OutputImages != 1 {
		t.Fatalf("default Responses image reserve=%+v", defaultReserve)
	}
}

func TestResponsesHostedToolBillingReserveAndSettlement(t *testing.T) {
	maxToolCalls := 4
	req := &RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model"},
		ResponseRequest: &openai.ResponseRequest{
			Model: "model", Input: "research", MaxToolCalls: &maxToolCalls,
			Tools: []openai.ResponseTool{
				{Type: "web_search"},
				{Type: "file_search", VectorStoreIDs: []string{"vs_1"}},
				{Type: "code_interpreter"},
				{Type: "mcp", ServerLabel: "documents", ServerURL: "https://mcp.example.test"},
				{Type: "function", Name: "client_function"},
				{Type: "custom", Name: "client_custom"},
			},
		},
	}
	reserved := billingRequest(req)
	if reserved.APIType != "responses" || reserved.ToolRequests != 4 || reserved.SearchRequests != 4 || !reserved.SearchRequestsEstimated {
		t.Fatalf("Responses hosted tool reserve=%+v", reserved)
	}
	req.ResponsesResponse = &openai.ResponseResponse{
		ID: "resp_tools", Model: "model", Status: "completed",
		Output: []openai.ResponseOutputItem{
			{Type: "web_search_call", Status: "completed"},
			{Type: "web_search_call", Status: "failed"},
			{Type: "file_search_call", Status: "completed"},
			{Type: "code_interpreter_call", Status: "completed"},
			{Type: "mcp_call", Status: "failed"},
			{Type: "mcp_list_tools", Status: "completed"},
			{Type: "function_call", Status: "completed"},
			{Type: "custom_tool_call", Status: "completed"},
			{Type: "image_generation_call", Status: "completed", Result: json.RawMessage(`"aW1hZ2U="`)},
		},
		Usage: openai.ResponseUsage{InputTokens: 7, OutputTokens: 11, TotalTokens: 18},
	}
	settled := billingRequest(req)
	if settled.Phase != "commit" || settled.ToolRequests != 3 || settled.SearchRequests != 2 || settled.SearchRequestsEstimated || settled.OutputImages != 1 || settled.TotalTokens != 18 || settled.UsageEstimated {
		t.Fatalf("Responses hosted tool settlement=%+v", settled)
	}
}

func TestResponsesWebSearchBillingUsesConservativeDefaultReserve(t *testing.T) {
	req := &RequestContext{
		Request:         openai.ChatCompletionRequest{Model: "model"},
		ResponseRequest: &openai.ResponseRequest{Model: "model", Input: "search", Tools: []openai.ResponseTool{{Type: "web_search_preview"}}},
	}
	reserved := billingRequest(req)
	if reserved.ToolRequests != 0 || reserved.SearchRequests != openai.WebSearchMaxUses || !reserved.SearchRequestsEstimated {
		t.Fatalf("Responses web search reserve=%+v", reserved)
	}

	zero := 0
	req.ResponseRequest.MaxToolCalls = &zero
	reserved = billingRequest(req)
	if reserved.SearchRequests != 0 || !reserved.SearchRequestsEstimated {
		t.Fatalf("Responses zero-call reserve=%+v", reserved)
	}
}

func TestResponsesClientToolsDoNotClaimProviderExecution(t *testing.T) {
	req := &RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model"},
		ResponseRequest: &openai.ResponseRequest{
			Model: "model", Input: "call", Tools: []openai.ResponseTool{{Type: "function", Name: "function"}, {Type: "custom", Name: "custom"}},
		},
		ResponsesResponse: &openai.ResponseResponse{Model: "model", Output: []openai.ResponseOutputItem{{Type: "function_call"}, {Type: "custom_tool_call"}}},
	}
	settled := billingRequest(req)
	if settled.ToolRequests != 0 || settled.SearchRequests != 0 || settled.SearchRequestsEstimated {
		t.Fatalf("client tools must not be reported as provider-executed: %+v", settled)
	}
}

func TestImageEditBillingReserveAndSettlement(t *testing.T) {
	n := 2
	attachment := openai.ImageAttachment{MediaType: "image/png", Data: "iVBORw0KGgpmaXh0dXJl"}
	req := &RequestContext{
		Request:          openai.ChatCompletionRequest{Model: "image-model"},
		ImageEditRequest: &openai.ImageEditRequest{Model: "image-model", Prompt: "edit", Images: []openai.ImageAttachment{attachment}, N: &n},
		Metadata:         map[string]string{"gateway.api_type": "image_edit"},
	}
	reserved := billingRequest(req)
	if reserved.APIType != "image_edit" || reserved.InputTokens != openai.ImageEditInputTokens(*req.ImageEditRequest) || reserved.OutputTokens != 2*openai.DefaultOutputTokenReserve || reserved.TotalTokens != reserved.InputTokens+reserved.OutputTokens {
		t.Fatalf("reserve=%+v", reserved)
	}
	req.ImageGenerationResponse = &openai.ImageGenerationResponse{Usage: &openai.ImageUsage{InputTokens: 5, OutputTokens: 13, TotalTokens: 18}}
	settled := billingRequest(req)
	if settled.Phase != "commit" || settled.APIType != "image_edit" || settled.InputTokens != 5 || settled.OutputTokens != 13 || settled.TotalTokens != 18 || settled.UsageEstimated {
		t.Fatalf("settlement=%+v", settled)
	}
}

func TestImageVariationBillingReserveAndSettlement(t *testing.T) {
	n := 2
	attachment := openai.ImageAttachment{MediaType: "image/png", Data: "iVBORw0KGgpmaXh0dXJl"}
	req := &RequestContext{
		Request:               openai.ChatCompletionRequest{Model: "image-model"},
		ImageVariationRequest: &openai.ImageVariationRequest{Model: "image-model", Image: attachment, N: &n},
		Metadata:              map[string]string{"gateway.api_type": "image_variation"},
	}
	reserved := billingRequest(req)
	if reserved.APIType != "image_variation" || reserved.InputTokens != openai.ImageVariationInputTokens(*req.ImageVariationRequest) || reserved.OutputTokens != 2*openai.DefaultOutputTokenReserve || reserved.TotalTokens != reserved.InputTokens+reserved.OutputTokens {
		t.Fatalf("reserve=%+v", reserved)
	}
	req.ImageGenerationResponse = &openai.ImageGenerationResponse{Usage: &openai.ImageUsage{InputTokens: 5, OutputTokens: 13, TotalTokens: 18}}
	settled := billingRequest(req)
	if settled.Phase != "commit" || settled.APIType != "image_variation" || settled.InputTokens != 5 || settled.OutputTokens != 13 || settled.TotalTokens != 18 || settled.UsageEstimated {
		t.Fatalf("settlement=%+v", settled)
	}
}

func TestAudioTranscriptionBillingReserveAndSettlement(t *testing.T) {
	attachment := openai.AudioAttachment{Filename: "sample.wav", MediaType: "audio/wav", Data: "UklGRi4uLi5XQVZFZGF0YQ=="}
	req := &RequestContext{
		Request:                   openai.ChatCompletionRequest{Model: "audio-model"},
		AudioTranscriptionRequest: &openai.AudioTranscriptionRequest{Model: "audio-model", File: attachment, Prompt: "names"},
		Metadata:                  map[string]string{"gateway.api_type": "audio_transcription"},
	}
	reserved := billingRequest(req)
	if reserved.APIType != "audio_transcription" || reserved.InputTokens != openai.AudioTranscriptionInputTokens(*req.AudioTranscriptionRequest) || reserved.OutputTokens != openai.DefaultOutputTokenReserve || reserved.TotalTokens != reserved.InputTokens+reserved.OutputTokens {
		t.Fatalf("reserve=%+v", reserved)
	}
	req.AudioTranscriptionResponse = &openai.AudioTranscriptionResponse{Text: "hello", Usage: &openai.AudioTranscriptionUsage{Type: "tokens", InputTokens: 5, OutputTokens: 2, TotalTokens: 7}}
	settled := billingRequest(req)
	if settled.Phase != "commit" || settled.APIType != "audio_transcription" || settled.InputTokens != 5 || settled.OutputTokens != 2 || settled.TotalTokens != 7 || settled.UsageEstimated {
		t.Fatalf("settlement=%+v", settled)
	}
}

func TestAudioTranslationBillingPreservesDurationUsage(t *testing.T) {
	attachment := openai.AudioAttachment{Filename: "sample.wav", MediaType: "audio/wav", Data: "UklGRi4uLi5XQVZFZGF0YQ=="}
	req := &RequestContext{
		Request:                   openai.ChatCompletionRequest{Model: "audio-model"},
		AudioTranscriptionRequest: &openai.AudioTranscriptionRequest{Model: "audio-model", File: attachment, Prompt: "names"},
		InputAudioMilliseconds:    10000,
		Metadata:                  map[string]string{"gateway.api_type": "audio_translation"},
	}
	reserved := billingRequest(req)
	if reserved.APIType != "audio_translation" || reserved.InputAudioMilliseconds != 10000 {
		t.Fatalf("reserve=%+v", reserved)
	}
	req.AudioTranscriptionResponse = &openai.AudioTranscriptionResponse{Text: "hello", Duration: 1, Usage: &openai.AudioTranscriptionUsage{Type: "duration", InputAudioMilliseconds: 10000}}
	settled := billingRequest(req)
	if settled.Phase != "commit" || settled.APIType != "audio_translation" || settled.InputAudioMilliseconds != 10000 || settled.InputTokens != 0 || settled.OutputTokens != 0 || settled.TotalTokens != 0 || settled.UsageEstimated {
		t.Fatalf("settlement=%+v", settled)
	}
}

func TestRemoteBillingCommitsCompactionUsageSeparately(t *testing.T) {
	req := sensitiveContext()
	req.ResponseRequest = &openai.ResponseRequest{Provider: "provider", Model: "compact-model", Input: "private input", Instructions: "private instructions"}
	req.Metadata = map[string]string{"gateway.api_type": "responses_compact"}
	reserved := billingRequest(&req)
	if reserved.APIType != "responses_compact" || reserved.PromptTokensEstimated != openai.ResponseInputTokens(*req.ResponseRequest) || reserved.TotalTokens <= reserved.InputTokens {
		t.Fatalf("unexpected compaction reserve: %+v", reserved)
	}
	req.CompactedResponse = &openai.CompactedResponse{ID: "cmp_1", Object: "response.compaction", Usage: openai.ResponseUsage{InputTokens: 14, OutputTokens: 3, TotalTokens: 17}}
	committed := billingRequest(&req)
	if committed.APIType != "responses_compact" || committed.Phase != "commit" || committed.InputTokens != 14 || committed.OutputTokens != 3 || committed.TotalTokens != 17 || committed.UsageEstimated {
		t.Fatalf("unexpected compaction commit: %+v", committed)
	}
}

func TestRemoteBillingAttributesInteractionsSeparately(t *testing.T) {
	req := sensitiveContext()
	maxOutputTokens := 9
	req.ResponseRequest = &openai.ResponseRequest{Provider: "provider", Model: "interaction-model", Input: "private input", MaxOutputTokens: &maxOutputTokens}
	req.Metadata = map[string]string{"gateway.api_type": "interactions"}
	reserved := billingRequest(&req)
	if reserved.APIType != "interactions" || reserved.OutputTokens != 9 || reserved.TotalTokens != reserved.InputTokens+9 {
		t.Fatalf("unexpected interaction reserve: %+v", reserved)
	}
	req.ResponsesResponse = &openai.ResponseResponse{ID: "resp_1", Model: "interaction-model", Status: "completed", Usage: openai.ResponseUsage{InputTokens: 7, OutputTokens: 5, TotalTokens: 12}}
	committed := billingRequest(&req)
	if committed.APIType != "interactions" || committed.Phase != "commit" || committed.InputTokens != 7 || committed.OutputTokens != 5 || committed.TotalTokens != 12 || committed.UsageEstimated {
		t.Fatalf("unexpected interaction commit: %+v", committed)
	}
}

func TestRemoteBillingPreservesNativeChatSurfaceAPIType(t *testing.T) {
	for _, apiType := range []string{"messages", "generate_content", "bedrock_converse"} {
		req := sensitiveContext()
		req.Metadata = map[string]string{"gateway.api_type": apiType}
		if request := billingRequest(&req); request.APIType != apiType {
			t.Fatalf("%s classified as %s", apiType, request.APIType)
		}
	}
	req := sensitiveContext()
	req.Metadata = map[string]string{"gateway.api_type": "client-controlled"}
	if request := billingRequest(&req); request.APIType != "chat_completions" {
		t.Fatalf("untrusted api type accepted: %s", request.APIType)
	}
}

func TestRemoteBillingReservesAllCompletionCandidatesAndCommitsUsage(t *testing.T) {
	maxTokens, bestOf := 100, 3
	req := sensitiveContext()
	req.CompletionRequest = &openai.CompletionRequest{Provider: "provider", Model: "instruct", Prompt: []string{"private prompt", "second prompt"}, MaxTokens: &maxTokens, BestOf: &bestOf}
	reserved := billingRequest(&req)
	input := openai.CompletionInputTokens(*req.CompletionRequest)
	if reserved.APIType != "completions" || reserved.InputTokens != input || reserved.OutputTokens != 600 || reserved.TotalTokens != input+600 {
		t.Fatalf("unexpected completion reserve: %+v", reserved)
	}
	req.CompletionResponse = &openai.CompletionResponse{Model: "instruct-v2", Usage: openai.Usage{PromptTokens: 11, CompletionTokens: 9, TotalTokens: 20}}
	committed := billingRequest(&req)
	if committed.Phase != "commit" || committed.InputTokens != 11 || committed.OutputTokens != 9 || committed.TotalTokens != 20 || committed.UpstreamModel != "instruct-v2" {
		t.Fatalf("unexpected completion commit: %+v", committed)
	}
	req.CompletionResponse = &openai.CompletionResponse{Model: "instruct-v2"}
	estimated := billingRequest(&req)
	if !estimated.UsageEstimated || estimated.InputTokens != input || estimated.OutputTokens != 600 || estimated.TotalTokens != input+600 {
		t.Fatalf("completion fallback lost reserved candidates: %+v", estimated)
	}
}

func TestRemoteBillingRerankPayloadContainsNoQueryOrDocuments(t *testing.T) {
	req := sensitiveContext()
	req.RerankRequest = &openai.RerankRequest{Provider: "p", Model: "reranker", Query: "private query", Documents: []any{"private document"}}
	request := billingRequest(&req)
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if request.APIType != "rerank" || request.PromptTokensEstimated == 0 {
		t.Fatalf("unexpected billing request: %+v", request)
	}
	if strings.Contains(string(payload), "private query") || strings.Contains(string(payload), "private document") {
		t.Fatalf("rerank text leaked to billing: %s", payload)
	}
}

func TestBillingRequestModerations(t *testing.T) {
	req := RequestContext{RequestID: "execution-1", ModerationRequest: &openai.ModerationRequest{Provider: "p", Model: "safe", Input: []any{"one", "two"}}}
	reserved := billingRequest(&req)
	if reserved.APIType != "moderations" || reserved.OutputTokens != 0 || reserved.InputTokens <= 0 || reserved.TotalTokens != reserved.InputTokens || reserved.Phase != "reserve" {
		t.Fatalf("unexpected moderation reserve: %+v", reserved)
	}
	req.ModerationResponse = &openai.ModerationResponse{ID: "modr-1", Model: "safe-upstream"}
	committed := billingRequest(&req)
	if committed.Phase != "commit" || committed.UpstreamModel != "safe-upstream" || committed.TotalTokens != reserved.InputTokens || !committed.UsageEstimated {
		t.Fatalf("unexpected moderation commit: %+v", committed)
	}
}

func TestRemoteBillingUsesScopedServiceSecretNotBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Service-Token") != "billing-secret" {
			t.Fatalf("headers=%v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(UsageResponse{})
	}))
	defer server.Close()
	req := sensitiveContext()
	if err := NewRemoteBillingModuleWithSecret(true, server.URL, "billing-secret").Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteBillingMapsBudgetRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "budget exceeded", http.StatusTooManyRequests)
	}))
	defer server.Close()
	if err := NewRemoteBillingModule(true, server.URL).Handle(context.Background(), &RequestContext{RequestID: "req-budget"}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected budget sentinel, got %v", err)
	}
}

func TestRemoteBillingMapsLifecycleConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "billing lifecycle conflict", http.StatusConflict)
	}))
	defer server.Close()
	if err := NewRemoteBillingModule(true, server.URL).Handle(context.Background(), &RequestContext{RequestID: "req-conflict"}); !errors.Is(err, ErrBillingConflict) {
		t.Fatalf("expected billing conflict sentinel, got %v", err)
	}
}

func TestRemoteModulesPropagateW3CTraceContext(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	}()

	var traceparent string
	var payloadTraceID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("traceparent")
		var request UsageRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		payloadTraceID = request.TraceID
		_ = json.NewEncoder(w).Encode(UsageResponse{})
	}))
	defer server.Close()
	ctx, span := otel.Tracer("test").Start(context.Background(), "parent")
	defer span.End()
	if err := NewRemoteBillingModule(true, server.URL).Handle(ctx, &RequestContext{RequestID: "trace-request"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(traceparent, "00-") {
		t.Fatalf("remote request did not carry W3C trace context: %q", traceparent)
	}
	if fields := strings.Split(traceparent, "-"); len(fields) != 4 || payloadTraceID != fields[1] {
		t.Fatalf("billing trace id %q does not match traceparent %q", payloadTraceID, traceparent)
	}
}

func contractServer(t *testing.T, response any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"api_key", "user_id", "roles", "credential_id"} {
			if _, found := body[forbidden]; found {
				t.Fatalf("module request contains forbidden field %q", forbidden)
			}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
}

func sensitiveContext() RequestContext {
	return RequestContext{
		APIKey:       "client-bearer-token",
		CredentialID: "safe-fingerprint",
		UserID:       "user-1",
		TeamID:       "team-1",
		Roles:        []string{"developer"},
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{{Role: "user", Content: "hello"}},
		},
	}
}

func TestRemoteAnonymizerUsesEmbeddingInputWithoutIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"api_key", "credential_id", "user_id", "team_id", "organization_id", "request", "anonymization_values"} {
			if _, found := body[forbidden]; found {
				t.Fatalf("anonymizer request contains %q", forbidden)
			}
		}
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Input: []any{"masked"}, Replacements: map[string]string{"{{EMAIL_1}}": "user@example.com"}})
	}))
	defer server.Close()
	request := openai.EmbeddingRequest{Model: "embed", Input: []string{"user@example.com"}}
	req := sensitiveContext()
	req.Request.Messages = nil
	req.EmbeddingRequest = &request
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if got := openai.EmbeddingInputText(req.EmbeddingRequest.Input); got != "masked" {
		t.Fatalf("unexpected anonymized embedding input: %q", got)
	}
	if _, ok := req.EmbeddingRequest.Input.([]string); !ok {
		t.Fatalf("embedding input type changed: %T", req.EmbeddingRequest.Input)
	}
}

func TestRemoteAnonymizerUsesImageGenerationPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body AnonymizeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Input != "private prompt" || len(body.Messages) != 0 {
			t.Fatalf("body=%+v err=%v", body, err)
		}
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Input: "masked prompt"})
	}))
	defer server.Close()
	request := openai.ImageGenerationRequest{Model: "image", Prompt: "private prompt"}
	req := RequestContext{ImageGenerationRequest: &request}
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(t.Context(), &req); err != nil {
		t.Fatal(err)
	}
	if request.Prompt != "masked prompt" {
		t.Fatalf("prompt=%q", request.Prompt)
	}
}

func TestRemoteAnonymizerUsesAudioTranscriptionHintsOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body AnonymizeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Input != "private speaker" || strings.Join(body.Keywords, ",") != "private@example.com,Acme" || strings.Join(body.SpeakerNames, ",") != "speaker@example.com" || len(body.Messages) != 0 {
			t.Fatalf("body=%+v err=%v", body, err)
		}
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Input: "masked speaker", Keywords: []string{"{{EMAIL_1}}", "Acme"}, SpeakerNames: []string{"{{EMAIL_2}}"}})
	}))
	defer server.Close()
	request := openai.AudioTranscriptionRequest{Model: "audio", Prompt: "private speaker", Keywords: []string{"private@example.com", "Acme"}, KnownSpeakerNames: []string{"speaker@example.com"}, File: openai.AudioAttachment{Filename: "sample.wav", MediaType: "audio/wav", Data: "UklGRi4uLi5XQVZFZGF0YQ=="}}
	req := RequestContext{AudioTranscriptionRequest: &request}
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(t.Context(), &req); err != nil {
		t.Fatal(err)
	}
	if request.Prompt != "masked speaker" || strings.Join(request.Keywords, ",") != "{{EMAIL_1}},Acme" || strings.Join(request.KnownSpeakerNames, ",") != "{{EMAIL_2}}" || request.File.Data == "" {
		t.Fatalf("request=%+v", request)
	}
}

func TestRemoteAnonymizerRejectsMissingTranscriptionKeywords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Input: "masked", Keywords: nil})
	}))
	defer server.Close()
	request := openai.AudioTranscriptionRequest{Model: "audio", Prompt: "private", Keywords: []string{"private"}}
	req := RequestContext{AudioTranscriptionRequest: &request}
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(t.Context(), &req); err == nil {
		t.Fatal("missing keyword projection was accepted")
	}
}

func TestRemoteAnonymizerRejectsMissingKnownSpeakerNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Input: "masked", SpeakerNames: nil})
	}))
	defer server.Close()
	request := openai.AudioTranscriptionRequest{Model: "audio", Prompt: "private", KnownSpeakerNames: []string{"Jane"}}
	req := RequestContext{AudioTranscriptionRequest: &request}
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(t.Context(), &req); err == nil {
		t.Fatal("missing known speaker name projection was accepted")
	}
}

func TestRemoteAnonymizerAcceptsTranscriptionKeywordsWithoutPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body AnonymizeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Input != nil || len(body.Keywords) != 1 {
			t.Fatalf("body=%+v err=%v", body, err)
		}
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Keywords: []string{"masked"}, SpeakerNames: nil})
	}))
	defer server.Close()
	request := openai.AudioTranscriptionRequest{Model: "audio", Keywords: []string{"private"}}
	req := RequestContext{AudioTranscriptionRequest: &request}
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(t.Context(), &req); err != nil || request.Keywords[0] != "masked" {
		t.Fatalf("request=%+v err=%v", request, err)
	}
}

func TestRemoteAnonymizerUsesImageEditPromptWithoutProjectingFiles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body AnonymizeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Input != "private edit" || len(body.Messages) != 0 {
			t.Fatalf("body=%+v err=%v", body, err)
		}
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Input: "masked edit"})
	}))
	defer server.Close()
	attachment := openai.ImageAttachment{MediaType: "image/png", Data: "iVBORw0KGgpmaXh0dXJl"}
	request := openai.ImageEditRequest{Model: "image", Prompt: "private edit", Images: []openai.ImageAttachment{attachment}}
	req := RequestContext{ImageEditRequest: &request}
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(t.Context(), &req); err != nil {
		t.Fatal(err)
	}
	if request.Prompt != "masked edit" || request.Images[0] != attachment {
		t.Fatalf("request=%+v", request)
	}
}

func TestRemoteAnonymizerUsesRerankProjectionWithoutIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"api_key", "credential_id", "user_id", "team_id", "organization_id", "request", "anonymization_values"} {
			if _, found := body[forbidden]; found {
				t.Fatalf("anonymizer request contains %q", forbidden)
			}
		}
		encoded, _ := json.Marshal(body)
		if !strings.Contains(string(encoded), "private@example.com") {
			t.Fatalf("rerank text projection missing: %s", encoded)
		}
		_ = json.NewEncoder(w).Encode(AnonymizeResponse{Query: "masked query", Documents: []any{"masked doc", map[string]any{"text": "masked object"}}, Replacements: map[string]string{"{{EMAIL_1}}": "private@example.com"}})
	}))
	defer server.Close()
	request := openai.RerankRequest{Model: "rerank", Query: "private@example.com", Documents: []any{"private doc", map[string]any{"text": "object doc", "id": "doc-1"}}}
	req := sensitiveContext()
	req.Request.Messages = nil
	req.RerankRequest = &request
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	object := req.RerankRequest.Documents[1].(map[string]any)
	if req.RerankRequest.Query != "masked query" || object["text"] != "masked object" || object["id"] != "doc-1" {
		t.Fatalf("unexpected merged rerank projection: %+v", req.RerankRequest)
	}
}

func TestRemoteBillingEmbeddingsContractContainsOnlyCounters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["api_type"] != "embeddings" || body["input_tokens"] != float64(openai.EstimateContextTokens("private embedding text")) || body["output_tokens"] != float64(0) {
			t.Fatalf("unexpected embedding usage contract: %+v", body)
		}
		encoded, _ := json.Marshal(body)
		if strings.Contains(string(encoded), "private embedding text") {
			t.Fatal("billing received embedding content")
		}
		_ = json.NewEncoder(w).Encode(UsageResponse{})
	}))
	defer server.Close()
	request := openai.EmbeddingRequest{Model: "embed", Input: "private embedding text"}
	req := sensitiveContext()
	req.Request.Messages = nil
	req.EmbeddingRequest = &request
	if err := NewRemoteBillingModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteBillingEmbeddingsUsesExactTokenIDCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["api_type"] != "embeddings" || body["input_tokens"] != float64(3) || body["total_tokens"] != float64(3) {
			t.Fatalf("token IDs were not counted exactly: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(UsageResponse{})
	}))
	defer server.Close()
	request := openai.EmbeddingRequest{Model: "embed", Input: []any{11.0, 12.0, 13.0}}
	req := sensitiveContext()
	req.Request.Messages = nil
	req.EmbeddingRequest = &request
	if err := NewRemoteBillingModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteBillingSandboxContractContainsOnlyCounters(t *testing.T) {
	request := openai.SandboxExecuteRequest{Provider: "runtime", Model: "python", Code: "print('private value')", Language: "python", Template: openai.DefaultSandboxTemplate, TimeoutSeconds: 30}
	req := sensitiveContext()
	req.Request = openai.ChatCompletionRequest{Provider: request.Provider, Model: request.Model, Messages: []openai.Message{{Role: "user", Content: request.Code}}}
	req.SandboxRequest = &request
	req.InputCharacters = len(request.Code)
	req.Metadata = map[string]string{"gateway.api_type": "sandbox"}

	reserved := billingRequest(&req)
	if reserved.Phase != "reserve" || reserved.APIType != "sandbox" || reserved.Provider != "runtime" || reserved.Model != "python" || reserved.InputTokens != request.InputTokens() || reserved.OutputTokens != 0 || reserved.TotalTokens != request.InputTokens() || reserved.InputCharacters != len(request.Code) || !reserved.UsageEstimated {
		t.Fatalf("unexpected sandbox reserve: %+v", reserved)
	}
	req.Response = &openai.ChatCompletionResponse{Model: "python-runtime", Usage: openai.Usage{}}
	committed := billingRequest(&req)
	if committed.Phase != "commit" || committed.APIType != "sandbox" || committed.UpstreamModel != "python-runtime" || committed.InputTokens != request.InputTokens() || committed.OutputTokens != 0 || committed.TotalTokens != request.InputTokens() || !committed.UsageEstimated {
		t.Fatalf("unexpected sandbox commit: %+v", committed)
	}

	encoded, err := json.Marshal(committed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), request.Code) {
		t.Fatal("billing received sandbox source code")
	}
}
