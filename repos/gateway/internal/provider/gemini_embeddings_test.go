package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestGeminiEmbeddingsBatchAndUsage(t *testing.T) {
	for _, usage := range []string{"", `,"usageMetadata":{"promptTokenCount":12}`, `,"usageMetadata":{"promptTokenCount":0}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1beta/models/embed:batchEmbedContents" || r.Header.Get("x-goog-api-key") != "fake-key" || r.URL.RawQuery != "" {
				t.Error("invalid native endpoint/auth")
			}
			var body struct {
				Requests []struct {
					Model      string        `json:"model"`
					Content    geminiContent `json:"content"`
					Dimensions int           `json:"outputDimensionality"`
				} `json:"requests"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Requests) != 2 {
				t.Error("input count lost")
			} else {
				for i, want := range []string{"one", "two"} {
					if body.Requests[i].Content.Parts[0].Text != want || body.Requests[i].Model != "models/embed" || body.Requests[i].Dimensions != 2 {
						t.Error("native input/dimensions lost")
					}
				}
			}
			_, _ = fmt.Fprint(w, `{"embeddings":[{"values":[1,2]},{"values":[3,4]}]`+usage+`}`)
		}))
		dimensions := 2
		response, err := NewGemini(server.URL, "fake-key", false).Embeddings(context.Background(), openai.EmbeddingRequest{Model: "embed", Input: []string{"one", "two"}, Dimensions: &dimensions})
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Data) != 2 || response.Data[1].Index != 1 || response.Data[1].Embedding[0] != 3 {
			t.Fatal("embedding order lost")
		}
		before := response.Usage
		mergeEmbeddingUsage(&response, &openai.Usage{PromptTokens: 999, TotalTokens: 999})
		if response.Usage.PromptTokens != before.PromptTokens || response.Usage.TotalTokens != before.TotalTokens {
			t.Fatal("reported usage overwritten")
		}
		if usage == "" && response.Usage.PromptTokens <= 0 {
			t.Fatal("missing local estimate")
		}
		if strings.Contains(usage, ":12") && response.Usage.PromptTokens != 12 {
			t.Fatal("provider usage lost")
		}
		if strings.Contains(usage, ":0") && response.Usage.PromptTokens != 0 {
			t.Fatal("reported zero lost")
		}
	}
}
func TestGeminiEmbeddingsRejectInvalidResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"embeddings":[{"values":[]}]}`, `{"embeddings":[{"values":[1]}]}`, `{"embeddings":[{"values":[1,2]},{"values":[3,4]}]}`, `{"embeddings":[{"values":[1,2]}],"usageMetadata":{}}`, `{"embeddings":[{"values":[1,2]}],"usageMetadata":{"promptTokenCount":-1}}`, `{"embeddings":[{"values":[1,2]}],"error":{"code":500}}`, `{"embeddings":[{"values":[1e999,2]}]}`, strings.Repeat("x", (32<<20)+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) }))
		dimension := 2
		_, err := NewGemini(server.URL, "", false).Embeddings(context.Background(), openai.EmbeddingRequest{Model: "embed", Input: "one", Dimensions: &dimension})
		server.Close()
		if err == nil {
			t.Fatalf("invalid response accepted (length=%d)", len(body))
		}
	}
}
func TestGeminiEmbeddingsRejectParametersAndRedirects(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := NewGemini(server.URL, "fake-key", false)
	for _, request := range []openai.EmbeddingRequest{{Model: "embed", Input: []int{1}}, {Model: "embed", Input: make([]string, 101)}, {Model: "embed", Input: "one", User: "user"}, {Model: "embed", Input: "one", EncodingFormat: "base64"}, {Model: "../other", Input: "one"}} {
		if _, err := client.Embeddings(context.Background(), request); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if calls != 0 {
		t.Fatal("unsupported request reached upstream")
	}
	_, err := client.Embeddings(context.Background(), openai.EmbeddingRequest{Model: "embed", Input: "one"})
	if err == nil || calls != 1 {
		t.Fatal("redirect followed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Embeddings(ctx, openai.EmbeddingRequest{Model: "embed", Input: "one"}); err == nil {
		t.Fatal("cancellation ignored")
	}
}
