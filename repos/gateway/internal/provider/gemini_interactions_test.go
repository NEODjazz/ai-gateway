package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
