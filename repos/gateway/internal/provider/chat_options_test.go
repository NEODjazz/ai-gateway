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
						_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"logprobs\":{\"content\":[{\"token\":%q,\"logprob\":-0.5,\"bytes\":[1],\"top_logprobs\":[]}]}},{\"index\":1,\"delta\":{\"content\":%q}}]}\n\n", token, token, token)
					}
					_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				} else {
					_, _ = fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"one"},"logprobs":{"content":[{"token":"one","logprob":-0.5,"bytes":[1],"top_logprobs":[]}]}},{"index":1,"message":{"role":"assistant","content":"two"}}]}`)
				}
			}))
			defer server.Close()
			var request openai.ChatCompletionRequest
			if err := json.Unmarshal([]byte(`{"model":"test","reasoning_effort":"high","n":2,"logprobs":true,"top_logprobs":0,"frequency_penalty":0,"presence_penalty":-1,"logit_bias":{"10":-100}}`), &request); err != nil {
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
			for name, want := range map[string]string{"reasoning_effort": `"high"`, "n": "2", "logprobs": "true", "top_logprobs": "0", "frequency_penalty": "0", "presence_penalty": "-1", "logit_bias": `{"10":-100}`} {
				if string(received[name]) != want {
					t.Fatalf("%s=%s, want %s", name, received[name], want)
				}
			}
			if streaming {
				if string(received["stream_options"]) != `{"include_usage":true}` {
					t.Fatalf("stream usage was not requested: %s", received["stream_options"])
				}
			} else if _, present := received["stream_options"]; present {
				t.Fatal("stream_options sent for a non-streaming request")
			}
			wantCount := 1
			if streaming {
				wantCount = 2
				if len(payloads) != 2 || !strings.Contains(payloads[0], "logprobs") {
					t.Fatal("SSE logprobs lost")
				}
			}
			if len(response.Choices) != 2 || response.Choices[0].Logprobs == nil || len(response.Choices[0].Logprobs.Content) != wantCount {
				t.Fatalf("response logprobs lost: %+v", response)
			}
		})
	}
}

func TestCompatibleChatRejectsIncompleteMultiChoiceResponse(t *testing.T) {
	choices := 2
	request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{N: &choices}, Model: "test"}
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if streaming {
					_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"one\"}}]}\n\ndata: [DONE]\n\n")
					return
				}
				_, _ = fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"one"}}]}`)
			}))
			defer server.Close()
			client := NewOpenAICompatible(server.URL, "", streaming)
			var err error
			if streaming {
				_, err = client.StreamChatCompletions(t.Context(), request, func(string) error { return nil })
			} else {
				_, err = client.ChatCompletions(t.Context(), request)
			}
			if err == nil || !strings.Contains(err.Error(), "choice count") {
				t.Fatalf("incomplete multi-choice response accepted: %v", err)
			}
		})
	}
}

func TestRequestedChatChoiceIndicesAreExact(t *testing.T) {
	choices := 2
	request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{N: &choices}}
	if err := validateRequestedChatChoices(request, openai.ChatCompletionResponse{Choices: []openai.Choice{{Index: 1}, {Index: 0}}}); err != nil {
		t.Fatal(err)
	}
	for _, response := range []openai.ChatCompletionResponse{
		{Choices: []openai.Choice{{Index: 0}}},
		{Choices: []openai.Choice{{Index: 0}, {Index: 0}}},
		{Choices: []openai.Choice{{Index: 0}, {Index: 2}}},
	} {
		if err := validateRequestedChatChoices(request, response); err == nil {
			t.Fatalf("invalid choices accepted: %+v", response.Choices)
		}
	}
}

func TestChatCompletionJSONResponseIsBoundedAndExact(t *testing.T) {
	var response openai.ChatCompletionResponse
	if err := decodeChatCompletionResponse(strings.NewReader(`{"choices":[]} {}`), &response); err == nil {
		t.Fatal("trailing chat completion JSON accepted")
	}
	reader := &embeddingLimitReader{}
	if err := decodeChatCompletionResponse(reader, &response); err == nil || reader.read != maxChatCompletionResponseBytes+1 {
		t.Fatalf("unbounded chat completion response: bytes=%d err=%v", reader.read, err)
	}
}

func TestCompatibleChatRejectsInvalidUsageBeforeDelivery(t *testing.T) {
	invalid := `{"prompt_tokens":2,"completion_tokens":3,"total_tokens":4}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"one"}}],"usage":`+invalid+`}`)
	}))
	defer server.Close()
	if _, err := NewOpenAICompatible(server.URL, "", false).ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "test"}); err == nil {
		t.Fatal("invalid JSON usage accepted")
	}
	wrote := false
	payload := `data: {"choices":[],"usage":` + invalid + `}` + "\n\n"
	if _, err := streamChatCompletionData(strings.NewReader(payload), "test", func(string) error { wrote = true; return nil }); err == nil || wrote {
		t.Fatalf("invalid SSE usage delivered: err=%v wrote=%v", err, wrote)
	}
}

func TestGenerationControlsAreRejectedByNativeAdapters(t *testing.T) {
	for _, body := range []string{`{"reasoning_effort":"high"}`, `{"n":2}`, `{"logprobs":false}`, `{"top_logprobs":0}`, `{"frequency_penalty":0}`, `{"presence_penalty":0}`, `{"logit_bias":{"1":0}}`} {
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
