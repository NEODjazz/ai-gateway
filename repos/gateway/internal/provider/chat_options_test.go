package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestCompatibleChatGenerationOptionsRoundTrip(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var received map[string]json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
					return
				}
				if streaming {
					for _, token := range []string{"one", "two"} {
						_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"logprobs\":{\"content\":[{\"token\":%q,\"logprob\":-0.5,\"bytes\":[1],\"top_logprobs\":[]}]}}]}\n\n", token, token)
					}
					_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				} else {
					_, _ = fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"one"},"logprobs":{"content":[{"token":"one","logprob":-0.5,"bytes":[1],"top_logprobs":[]}]}}]}`)
				}
			}))
			defer server.Close()
			var request openai.ChatCompletionRequest
			if err := json.Unmarshal([]byte(`{"model":"test","reasoning_effort":"high","logprobs":true,"top_logprobs":0,"frequency_penalty":0,"presence_penalty":-1,"logit_bias":{"10":-100}}`), &request); err != nil {
				t.Fatal(err)
			}
			client := NewOpenAICompatible(server.URL, "", true)
			var response openai.ChatCompletionResponse
			var err error
			var payloads []string
			if streaming {
				response, err = client.StreamChatCompletions(context.Background(), request, func(s string) error { payloads = append(payloads, s); return nil })
			} else {
				response, err = client.ChatCompletions(context.Background(), request)
			}
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]string{"reasoning_effort": `"high"`, "logprobs": "true", "top_logprobs": "0", "frequency_penalty": "0", "presence_penalty": "-1", "logit_bias": `{"10":-100}`} {
				if string(received[name]) != want {
					t.Fatalf("%s=%s, want %s", name, received[name], want)
				}
			}
			wantCount := 1
			if streaming {
				wantCount = 2
				if len(payloads) != 2 || !strings.Contains(payloads[0], "logprobs") {
					t.Fatal("SSE logprobs lost")
				}
			}
			if len(response.Choices) != 1 || response.Choices[0].Logprobs == nil || len(response.Choices[0].Logprobs.Content) != wantCount {
				t.Fatalf("response logprobs lost: %+v", response)
			}
		})
	}
}

func TestGenerationControlsAreRejectedByNativeAdapters(t *testing.T) {
	for _, body := range []string{`{"reasoning_effort":"high"}`, `{"logprobs":false}`, `{"top_logprobs":0}`, `{"frequency_penalty":0}`, `{"presence_penalty":0}`, `{"logit_bias":{"1":0}}`} {
		var request openai.ChatCompletionRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		for _, client := range []Client{NewAnthropic("http://unused.invalid", "", false), NewOllama("http://unused.invalid", false), Demo{}} {
			if err := validateChatAdapter(client, request); err == nil {
				t.Fatalf("%T silently accepted %s", client, body)
			}
		}
	}
}

func TestLogprobsDisableSemanticCacheAndScopeExactCache(t *testing.T) {
	request := modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}
	before := providerCacheKey("chat", request)
	enabled := true
	request.Request.Logprobs = &enabled
	if _, _, ok := semanticRequest(request, Endpoint{Name: "test"}); ok {
		t.Fatal("semantic reuse of log probabilities allowed")
	}
	if providerCacheKey("chat", request) == before {
		t.Fatal("exact cache ignored logprobs")
	}
}
