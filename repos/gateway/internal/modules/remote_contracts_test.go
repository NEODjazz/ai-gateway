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
		_ = json.NewEncoder(w).Encode(AuthResponse{UserID: "user-1", TeamID: "team-1", CredentialID: "fingerprint", AllowedModels: []string{"gpt-*"}, AllowedTools: []string{"mcp.weather.*"}, RateLimitRPM: 5, RateLimitTPM: 100})
	}))
	defer server.Close()

	req := RequestContext{APIKey: token}
	if err := NewRemoteAuthModule(true, server.URL).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.CredentialID != "fingerprint" {
		t.Fatalf("unexpected credential id: %q", req.CredentialID)
	}
	if req.APIKey != "" {
		t.Fatal("auth did not clear bearer token after successful authorization")
	}
	if req.TeamID != "team-1" || req.RateLimitRPM != 5 || req.RateLimitTPM != 100 || len(req.AllowedModels) != 1 || len(req.AllowedTools) != 1 {
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
	request := billingRequest(&req)
	if request.CatalogVersion != "runtime-v2" || request.PricingKey != "endpoint/model" || request.InputCostPer1M != "1.5" || request.Currency != "USD" {
		t.Fatalf("pricing snapshot=%+v", request)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("traceparent")
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
		for _, forbidden := range []string{"api_key", "credential_id", "user_id", "team_id", "request", "anonymization_values"} {
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

func TestRemoteBillingEmbeddingsContractContainsOnlyCounters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["api_type"] != "embeddings" || body["input_tokens"] != float64(3) || body["output_tokens"] != float64(0) {
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
