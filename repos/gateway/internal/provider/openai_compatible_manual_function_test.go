package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestOpenAICompatibleForwardsManualFunctionResults(t *testing.T) {
	const arguments = `{"id":9007199254740993,"amount":0.1234567890123456789012345}`
	parameters := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}, "required": []string{"id"}}
	for _, mode := range []string{"chat", "responses_api", "responses_browser"} {
		for index, output := range []string{"", "rows: 9007199254740993\nquoted: \"exact\"\n<not-json>", `{"isError":true,"error":"User declined tool invocation"}`} {
			t.Run(fmt.Sprintf("%s/result_%d", mode, index), func(t *testing.T) {
				received := make(chan map[string]json.RawMessage, 1)
				path := "/v1/responses"
				if mode == "chat" {
					path = "/v1/chat/completions"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.URL.Path != path {
						t.Errorf("unexpected function transport: %s %s", r.Method, r.URL.Path)
						http.Error(w, "unexpected transport", http.StatusBadRequest)
						return
					}
					var request map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode function continuation: %v", err)
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					received <- request
					w.Header().Set("Content-Type", "application/json")
					response := `{"id":"resp_done","object":"response","status":"completed","model":"model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Function result received"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}`
					if mode == "chat" {
						response = `{"id":"chat_done","object":"chat.completion","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"Function result received"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`
					}
					if _, err := fmt.Fprint(w, response); err != nil {
						t.Errorf("write function response: %v", err)
					}
				}))
				t.Cleanup(server.Close)
				adapter := NewOpenAICompatible(server.URL, "", false)
				var expectedHistory any
				var expectedTools any
				if mode == "chat" {
					messages := []openai.Message{
						{Role: "user", Content: "Review function"},
						{Role: "assistant", Content: "", ToolCalls: []openai.ToolCall{{ID: "call_manual", Type: "function", Function: openai.FunctionCall{Name: "query", Arguments: arguments}}}},
						{Role: "tool", ToolCallID: "call_manual", Content: output},
					}
					tools := []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "query", Parameters: parameters}}}
					expectedHistory, expectedTools = messages, tools
					response, err := adapter.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: messages, Tools: tools})
					if err != nil {
						t.Fatal(err)
					}
					if response.ID != "chat_done" || response.Usage.TotalTokens != 7 {
						t.Fatalf("Chat result or actual usage changed: %+v", response)
					}
				} else {
					input := []any{}
					previous := "resp_previous"
					if mode == "responses_browser" {
						previous = ""
						input = append(input, map[string]any{"role": "user", "content": "Review function"}, map[string]any{"type": "function_call", "call_id": "call_manual", "name": "query", "arguments": arguments})
					}
					input = append(input, map[string]any{"type": "function_call_output", "call_id": "call_manual", "output": output})
					tools := []openai.ResponseTool{{Type: "function", Name: "query", Parameters: parameters}}
					expectedHistory, expectedTools = input, tools
					response, err := adapter.Responses(t.Context(), openai.ResponseRequest{Model: "model", PreviousResponse: previous, Input: input, Tools: tools})
					if err != nil {
						t.Fatal(err)
					}
					if response.ID != "resp_done" || response.Usage.TotalTokens != 7 {
						t.Fatalf("Responses result or actual usage changed: %+v", response)
					}
				}
				upstream := <-received
				historyField := "input"
				if mode == "chat" {
					historyField = "messages"
				}
				for field, expected := range map[string]any{historyField: expectedHistory, "tools": expectedTools} {
					var actual any
					if err := json.Unmarshal(upstream[field], &actual); err != nil {
						t.Fatalf("decode %s: %v", field, err)
					}
					actualJSON, err := json.Marshal(actual)
					if err != nil {
						t.Fatal(err)
					}
					expectedJSON, err := json.Marshal(expected)
					if err != nil {
						t.Fatal(err)
					}
					// Normalize object key order without parsing JSON inside tool text.
					var normalized any
					if err := json.Unmarshal(expectedJSON, &normalized); err != nil {
						t.Fatal(err)
					}
					expectedJSON, err = json.Marshal(normalized)
					if err != nil {
						t.Fatal(err)
					}
					if string(actualJSON) != string(expectedJSON) {
						t.Fatalf("%s changed: got %s, want %s", field, actualJSON, expectedJSON)
					}
				}
				if mode == "responses_api" && string(upstream["previous_response_id"]) != `"resp_previous"` {
					t.Fatalf("API continuation lost response identity: %s", upstream["previous_response_id"])
				}
				if mode != "responses_api" && len(upstream["previous_response_id"]) != 0 {
					t.Fatalf("unexpected API continuation in %s", mode)
				}
			})
		}
	}
}
