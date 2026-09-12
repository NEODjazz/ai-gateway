package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestGeminiInteractionsUsesNativeContract(t *testing.T) {
	seed := int64(7)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/interactions" || r.URL.RawQuery != "" || r.Header.Get("x-goog-api-key") != "secret" || r.Header.Get("Api-Revision") != "2026-05-20" {
			t.Errorf("request=%s %s?%s headers=%v", r.Method, r.URL.Path, r.URL.RawQuery, r.Header)
		}
		var body struct {
			Model            string                             `json:"model"`
			Input            any                                `json:"input"`
			GenerationConfig openai.InteractionGenerationConfig `json:"generation_config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "gemini-upstream" || body.Input != "hello" || body.GenerationConfig.Seed == nil || *body.GenerationConfig.Seed != seed || len(body.GenerationConfig.StopSequences) != 1 || body.GenerationConfig.ThinkingLevel != "high" {
			t.Errorf("body=%+v err=%v", body, err)
		}
		_, _ = fmt.Fprint(w, `{"id":"interaction_1","object":"interaction","model":"gemini-upstream","status":"completed","steps":[{"id":"step_1","type":"model_output","content":[{"type":"text","text":"hello"}]}],"usage":{"total_input_tokens":3,"total_output_tokens":2,"total_tokens":5}}`)
	}))
	defer server.Close()

	response, err := NewGemini(server.URL, "secret", false).Interactions(t.Context(), openai.InteractionRequest{
		Model: "gemini-upstream", Input: "hello",
		GenerationConfig: openai.InteractionGenerationConfig{Seed: &seed, StopSequences: []string{"END"}, ThinkingLevel: "high"},
	})
	if err != nil || response.ID != "interaction_1" || response.Usage.TotalTokens != 5 || len(response.Steps) != 1 || response.Steps[0].Content[0].Text != "hello" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiInteractionsUsesNativeAgentContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["agent"] != "research-agent" || body["model"] != nil || body["input"] != "hello" {
			t.Errorf("body=%#v err=%v", body, err)
		}
		_, _ = fmt.Fprint(w, `{"id":"interaction_agent","object":"interaction","agent":"research-agent","status":"completed","usage":{"total_tokens":3}}`)
	}))
	defer server.Close()
	response, err := NewGemini(server.URL, "secret", false).Interactions(t.Context(), openai.InteractionRequest{Agent: "research-agent", Input: "hello"})
	if err != nil || response.ID != "interaction_agent" || response.Agent != "research-agent" || response.Model != "" || response.Usage.TotalTokens != 3 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiStoredInteractionLifecycleUsesOwnerBinding(t *testing.T) {
	var creates, continues, retrieves, cancels, deletes atomic.Int32
	var otherDeploymentCalls atomic.Int32
	otherDeployment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherDeploymentCalls.Add(1)
		_, _ = fmt.Fprint(w, `{"id":"wrong_deployment","object":"interaction","model":"upstream","status":"completed","usage":{"total_tokens":1}}`)
	}))
	defer otherDeployment.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta/interactions":
			var body struct {
				Previous string `json:"previous_interaction_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Previous != "" {
				continues.Add(1)
				_, _ = fmt.Fprint(w, `{"id":"interaction_next","object":"interaction","model":"upstream","status":"completed","usage":{"total_tokens":1}}`)
				return
			}
			creates.Add(1)
			_, _ = fmt.Fprint(w, `{"id":"interaction_owned","object":"interaction","model":"upstream","status":"completed","usage":{"total_tokens":1}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta/interactions/interaction_owned":
			retrieves.Add(1)
			_, _ = fmt.Fprint(w, `{"id":"interaction_owned","object":"interaction","model":"upstream","status":"completed","usage":{"total_tokens":1}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta/interactions/interaction_owned:cancel":
			cancels.Add(1)
			_, _ = fmt.Fprint(w, `{"id":"interaction_owned","status":"cancelled"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1beta/interactions/interaction_owned":
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	backend := &ownershipTestStore{data: map[string][]byte{}}
	runtime := New(Config{
		Endpoints: []config.ProviderEndpointConfig{
			{Name: "deployment-b", Type: "gemini", BaseURL: otherDeployment.URL, APIKey: "secret", Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"interactions"}},
			{Name: "deployment-a", Type: "gemini", BaseURL: server.URL, APIKey: "secret", Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"interactions"}},
		},
		Modules:              modules.NewPipeline(nil),
		SessionStore:         backend,
		ResponseOwnershipTTL: time.Hour,
	}).(*Router)
	store := true
	shared := openai.ResponseRequest{Model: "public", Input: "hello", Store: &store}
	owner := modules.RequestContext{CredentialID: "owner", ResponseRequest: &shared, Request: openai.ChatCompletionRequest{Model: "public"}}
	created, err := runtime.Interactions(t.Context(), owner, openai.InteractionRequest{Provider: "deployment-a", Model: "public", Input: "hello", Store: &store})
	if err != nil || created.ID != "interaction_owned" || creates.Load() != 1 {
		t.Fatalf("created=%+v creates=%d err=%v", created, creates.Load(), err)
	}
	other := owner
	other.CredentialID = "other"
	otherContinuation := openai.ResponseRequest{Model: "public", Input: "again", PreviousResponse: created.ID}
	other.ResponseRequest = &otherContinuation
	if _, err := runtime.Interactions(t.Context(), other, openai.InteractionRequest{Model: "public", Input: "again", PreviousInteractionID: created.ID}); !errors.Is(err, ErrResponseNotFound) || continues.Load() != 0 {
		t.Fatalf("cross-owner continuation err=%v calls=%d", err, continues.Load())
	}
	continuation := openai.ResponseRequest{Model: "public", Input: "again", PreviousResponse: created.ID}
	owner.ResponseRequest = &continuation
	if next, err := runtime.Interactions(t.Context(), owner, openai.InteractionRequest{Model: "public", Input: "again", PreviousInteractionID: created.ID}); err != nil || next.ID != "interaction_next" || continues.Load() != 1 {
		t.Fatalf("continuation=%+v calls=%d err=%v", next, continues.Load(), err)
	}
	if otherDeploymentCalls.Load() != 0 {
		t.Fatalf("continuation escaped its deployment binding: calls=%d", otherDeploymentCalls.Load())
	}
	if _, err := runtime.RetrieveInteraction(t.Context(), other, created.ID); !errors.Is(err, ErrResponseNotFound) || retrieves.Load() != 0 {
		t.Fatalf("cross-owner retrieve err=%v calls=%d", err, retrieves.Load())
	}
	if got, err := runtime.RetrieveInteraction(t.Context(), owner, created.ID); err != nil || got.ID != created.ID {
		t.Fatalf("retrieve=%+v err=%v", got, err)
	}
	if got, err := runtime.CancelInteraction(t.Context(), owner, created.ID); err != nil || got.Status != "cancelled" || got.Model != "public" {
		t.Fatalf("cancel=%+v err=%v", got, err)
	}
	if err := runtime.DeleteInteraction(t.Context(), owner, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RetrieveInteraction(t.Context(), owner, created.ID); !errors.Is(err, ErrResponseNotFound) {
		t.Fatalf("deleted binding remained: %v", err)
	}
	if retrieves.Load() != 1 || cancels.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("lifecycle calls retrieve=%d cancel=%d delete=%d", retrieves.Load(), cancels.Load(), deletes.Load())
	}
}

func TestGeminiInteractionsStreamsBoundedNativeEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/interactions" || r.URL.Query().Get("alt") != "sse" || r.Header.Get("Api-Revision") != "2026-05-20" {
			t.Errorf("request=%s?%s headers=%v", r.URL.Path, r.URL.RawQuery, r.Header)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["stream"] != true {
			t.Errorf("body=%#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"interaction_1\",\"object\":\"interaction\",\"model\":\"gemini\",\"status\":\"in_progress\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"text\":\"hello\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"interaction_1\",\"object\":\"interaction\",\"model\":\"gemini\",\"status\":\"completed\",\"steps\":[{\"id\":\"step_1\",\"type\":\"model_output\",\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}],\"usage\":{\"total_input_tokens\":3,\"total_output_tokens\":2,\"total_tokens\":5}}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	var events []string
	response, err := NewGemini(server.URL, "secret", true).StreamInteractions(t.Context(), openai.InteractionRequest{Model: "gemini", Input: "hello", Stream: true}, func(event, payload string) error {
		events = append(events, event+":"+payload)
		return nil
	})
	if err != nil || response.ID != "interaction_1" || response.Usage.TotalTokens != 5 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if len(events) != 2 || !strings.HasPrefix(events[0], "interaction.created:") || !strings.HasPrefix(events[1], "step.delta:") {
		t.Fatalf("events=%v", events)
	}
}
