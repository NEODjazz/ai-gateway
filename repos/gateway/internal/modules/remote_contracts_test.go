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
		for _, forbidden := range []string{"request", "response", "response_request", "responses_response", "messages", "anonymization_values"} {
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
	req.Metadata["model_catalog.currency"] = "USD"
	req.Metadata["provider.id"] = "azure-open-ai"
	req.Metadata["provider.first_token_latency_ms"] = "87"
	req.Metadata["provider.retry_count"] = "2"
	req.Metadata["provider.fallback_count"] = "1"
	req.Metadata["provider.cache.kind"] = "semantic"
	req.Metadata["provider.original_model"] = "gpt-5.6-luna"
	req.Request.Model = "fallback-group"
	req.Response = &openai.ChatCompletionResponse{Model: "gpt-5.6-luna-2026-07-09", Usage: openai.Usage{PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11, PromptTokensDetails: &openai.PromptTokenDetails{CachedTokens: 6, CacheCreationTokens: 2}}}
	request := billingRequest(&req)
	if request.CatalogVersion != "runtime-v2" || request.PricingKey != "endpoint/model" || request.InputCostPer1M != "1.5" || request.Currency != "USD" {
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
	request := openai.EmbeddingRequest{Model: "embed", Input: []any{"user@example.com"}}
	req := sensitiveContext()
	req.Request.Messages = nil
	req.EmbeddingRequest = &request
	if err := NewRemoteAnonymizerModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if got := openai.EmbeddingInputText(req.EmbeddingRequest.Input); got != "masked" {
		t.Fatalf("unexpected anonymized embedding input: %q", got)
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
