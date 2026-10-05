package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestLemonadeChatStreamNormalizesTimestampPerRequest(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		created := calls.Add(1) * 10
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"id\":\"test\",\"model\":\"native\",\"created\":%d,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"}}]}\n\n", created)
		fmt.Fprintf(w, "data: {\"id\":\"test\",\"model\":\"native\",\"created\":%d,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n", created+1)
	}))
	t.Cleanup(server.Close)
	client := NewLemonade(server.URL, "", true)
	request := openai.ChatCompletionRequest{Model: "native", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Stream: true}
	for _, mode := range []string{"stream", "stream again", "buffer upstream stream"} {
		t.Run(mode, func(t *testing.T) {
			expected := (calls.Load() + 1) * 10
			var response openai.ChatCompletionResponse
			var err error
			if mode == "buffer upstream stream" {
				response, err = client.ChatCompletions(t.Context(), request)
			} else {
				writes := 0
				response, err = client.StreamChatCompletions(t.Context(), request, func(payload string) error {
					var chunk struct {
						Created int64 `json:"created"`
					}
					if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
						return err
					}
					if chunk.Created != expected {
						t.Errorf("forwarded timestamp=%d want=%d", chunk.Created, expected)
					}
					writes++
					return nil
				})
				if writes != 2 {
					t.Errorf("writes=%d want=2", writes)
				}
			}
			if err != nil || response.Created != expected || !response.UsageReported || response.Usage.TotalTokens != 3 || response.Choices[0].Message.Content != "answer" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
}

func TestLemonadeChatStreamStillRejectsInvalidIdentityAndUsage(t *testing.T) {
	for _, test := range []struct{ name, fields, want string }{
		{"id", `"id":"changed","model":"native","created":11`, "changed chat completion ID"},
		{"model", `"id":"test","model":"changed","created":11`, "changed chat completion model"},
		{"negative timestamp", `"id":"test","model":"native","created":-1`, "invalid chat completion timestamp"},
		{"invalid timestamp type", `"id":"test","model":"native","created":"bad"`, "cannot unmarshal"},
		{"incomplete usage", `"id":"test","model":"native","created":11,"usage":{"prompt_tokens":2,"total_tokens":2}`, "incomplete token usage"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"test\",\"model\":\"native\",\"created\":10,\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"answer\"}}]}\n\n")
				fmt.Fprintf(w, "data: {%s,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", test.fields)
			}))
			t.Cleanup(server.Close)
			writes := 0
			_, err := NewLemonade(server.URL, "", true).StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "native", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, func(string) error { writes++; return nil })
			if err == nil || !strings.Contains(err.Error(), test.want) || writes != 1 {
				t.Fatalf("writes=%d err=%v", writes, err)
			}
		})
	}
}

func TestLemonadeTimestampNormalizationPreservesPayloadAndIsolation(t *testing.T) {
	first, second := newLemonadeChatStreamNormalizer(), newLemonadeChatStreamNormalizer()
	for _, payload := range []string{`{"choices":[]}`, `{"created":null,"choices":[]}`, `{"created":0,"choices":[]}`} {
		if got, err := first(payload); err != nil || got != payload {
			t.Fatalf("initial payload=%s err=%v", got, err)
		}
	}
	if _, err := second(`{"created":99,"choices":[]}`); err != nil {
		t.Fatal(err)
	}
	payload := `{"created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"count\":9007199254740993}"}}]}}],"extension":9007199254740993,"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":1}}}`
	got, err := first(payload)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(got), &after); err != nil {
		t.Fatal(err)
	}
	if string(after["created"]) != "0" {
		t.Fatalf("timestamp=%s", after["created"])
	}
	for _, key := range []string{"choices", "extension", "usage"} {
		if string(before[key]) != string(after[key]) {
			t.Fatalf("field changed: %s", key)
		}
	}
	got, err = second(`{"created":100,"choices":[]}`)
	if err != nil || !strings.Contains(got, `"created":99`) {
		t.Fatalf("independent request timestamp lost: %s err=%v", got, err)
	}
}

func TestGenericChatStreamStillRejectsChangingTimestamp(t *testing.T) {
	stream := "data: {\"id\":\"test\",\"model\":\"native\",\"created\":10,\"choices\":[]}\n\ndata: {\"id\":\"test\",\"model\":\"native\",\"created\":11,\"choices\":[]}\n\ndata: [DONE]\n\n"
	if _, err := streamChatCompletionData(strings.NewReader(stream), "native", nil); err == nil || !strings.Contains(err.Error(), "changed chat completion timestamp") {
		t.Fatalf("generic timestamp validation changed: %v", err)
	}
}
