package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestEmbeddingProviderUsagePresence(t *testing.T) {
	for _, adapter := range []string{"compatible", "ollama"} {
		for _, tc := range []struct {
			name     string
			reported bool
			tokens   int
		}{{"missing", false, 99}, {"zero", true, 0}, {"positive", true, 7}} {
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				body := `{"object":"list","data":[{"index":0,"embedding":[1,2]}],"model":"m"`
				if adapter == "ollama" {
					body = `{"model":"m","embeddings":[[1,2]]`
				}
				if tc.reported {
					if adapter == "ollama" {
						body += fmt.Sprintf(`,"prompt_eval_count":%d`, tc.tokens)
					} else {
						body += fmt.Sprintf(`,"usage":{"prompt_tokens":%d,"total_tokens":%d}`, tc.tokens, tc.tokens)
					}
				}
				body += "}"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) }))
				defer server.Close()
				var client EmbeddingClient = NewOpenAICompatible(server.URL, "", false)
				if adapter == "ollama" {
					client = NewOllama(server.URL, false)
				}
				result, err := client.Embeddings(context.Background(), openai.EmbeddingRequest{Model: "m", Input: "input"})
				if err != nil {
					t.Fatal(err)
				}
				mergeEmbeddingUsage(&result, &openai.Usage{PromptTokens: 99, TotalTokens: 99})
				if result.Usage.PromptTokens != tc.tokens || result.Usage.TotalTokens != tc.tokens {
					t.Fatalf("usage replaced or lost: %+v", result.Usage)
				}
			})
		}
	}
}

func TestEmbeddingProvidersRejectMalformedUsage(t *testing.T) {
	for _, tc := range []struct{ adapter, body string }{
		{"compatible", `{"usage":{}}`},
		{"compatible", `{"usage":{"prompt_tokens":1}}`},
		{"compatible", `{"usage":{"prompt_tokens":-1,"total_tokens":0}}`},
		{"compatible", `{"usage":{"prompt_tokens":2,"total_tokens":1}}`},
		{"compatible", `{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`},
		{"ollama", `{"prompt_eval_count":-1}`},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, tc.body) }))
		var client EmbeddingClient = NewOpenAICompatible(server.URL, "", false)
		if tc.adapter == "ollama" {
			client = NewOllama(server.URL, false)
		}
		_, err := client.Embeddings(context.Background(), openai.EmbeddingRequest{Model: "m", Input: "input"})
		server.Close()
		if err == nil {
			t.Fatalf("invalid usage accepted: %s", tc.body)
		}
	}
}
