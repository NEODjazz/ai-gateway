package modules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
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
		_ = json.NewEncoder(w).Encode(AuthResponse{UserID: "user-1", TeamID: "team-1", CredentialID: "fingerprint", AllowedModels: []string{"gpt-*"}, RateLimitRPM: 5, RateLimitTPM: 100})
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
	if req.TeamID != "team-1" || req.RateLimitRPM != 5 || req.RateLimitTPM != 100 || len(req.AllowedModels) != 1 {
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
		Roles:        []string{"developer"},
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{{Role: "user", Content: "hello"}},
		},
	}
}
