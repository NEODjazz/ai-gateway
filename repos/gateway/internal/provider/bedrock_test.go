package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func TestBedrockConverseMapsMessagesToolsAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/model/us.anthropic.claude-v1:0/converse" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatalf("path=%q authorization=%q", r.URL.EscapedPath(), r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages := body["messages"].([]any)
		if len(messages) != 3 || len(body["system"].([]any)) != 1 || body["inferenceConfig"].(map[string]any)["maxTokens"] != float64(32) {
			t.Fatalf("request=%#v", body)
		}
		toolConfig := body["toolConfig"].(map[string]any)
		if len(toolConfig["tools"].([]any)) != 1 {
			t.Fatalf("tool config=%#v", toolConfig)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"checking "},{"toolUse":{"toolUseId":"call_2","name":"weather","input":{"city":"Paris"}}}]}},"stopReason":"tool_use","usage":{"inputTokens":9,"outputTokens":4,"totalTokens":13}}`)
	}))
	defer server.Close()
	maxTokens := 32
	client := NewBedrock(server.URL, "provider-key")
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "us.anthropic.claude-v1:0", MaxCompletionTokens: &maxTokens,
		Messages: []openai.Message{
			{Role: "system", Content: "be concise"},
			{Role: "user", Content: "weather"},
			{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call_1", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Rome"}`}}}},
			{Role: "tool", ToolCallID: "call_1", Content: "sunny"},
		},
		Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}}}},
	})
	if err != nil || response.Usage.TotalTokens != 13 || response.Choices[0].FinishReason != "tool_calls" || openai.ContentText(response.Choices[0].Message.Content) != "checking " || response.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseForwardsUserImageInOrder(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []bedrockMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		content := body.Messages[0].Content
		if len(content) != 3 || content[0].Text != "before" || content[1].Image == nil || content[1].Image.Format != "png" || content[1].Image.Source.Bytes != data || content[2].Text != "after" {
			t.Fatalf("content=%+v", content)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":5,"outputTokens":1,"totalTokens":6}}`)
	}))
	defer server.Close()
	content := []any{
		map[string]any{"type": "text", "text": "before"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + data}},
		map[string]any{"type": "text", "text": "after"},
	}
	response, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: content}}})
	if err != nil || response.Usage.TotalTokens != 6 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseForwardsValidatedNativeDocument(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-test"))
	prompt := "summarize"
	after := "after"
	request, err := (openai.BedrockConverseRequest{Messages: []openai.BedrockMessage{{Role: "user", Content: []openai.BedrockContentBlock{
		{Text: &prompt},
		{Document: &openai.BedrockDocument{Format: "pdf", Name: "Report", Source: openai.BedrockDocumentSource{Bytes: data}}},
		{Text: &after},
	}}}}).ChatRequest("model", "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []bedrockMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		content := body.Messages[0].Content
		if len(content) != 3 || content[0].Text != "summarize" || content[1].Document == nil || content[1].Document.Name != "Report" || content[1].Document.Source.Bytes != data || content[2].Text != "after" {
			t.Fatalf("content=%+v", content)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":5,"outputTokens":1,"totalTokens":6}}`)
	}))
	defer server.Close()
	response, err := NewBedrock(server.URL, "key").ChatCompletions(t.Context(), request)
	if err != nil || response.Usage.TotalTokens != 6 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseUsesSigV4TemporaryCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKID/20260102/us-east-1/bedrock/aws4_request") || r.Header.Get("X-Amz-Security-Token") != "session" || r.Header.Get("X-Amz-Date") != "20260102T030405Z" || r.Header.Get("X-Amz-Content-Sha256") == "" {
			t.Fatalf("headers=%v", r.Header)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	credential := `{"access_key_id":"AKID","secret_access_key":"secret","session_token":"session"}`
	client := NewBedrockWithAuth(server.URL, credential, " AWS_SIGV4 ", "us-east-1")
	client.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || response.Usage.TotalTokens != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseUsesAmbientEnvironmentCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ENVKEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "environment-secret")
	t.Setenv("AWS_SESSION_TOKEN", "environment-session")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=ENVKEY/") || r.Header.Get("X-Amz-Security-Token") != "environment-session" {
			t.Fatalf("headers=%v", r.Header)
		}
		_, _ = fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer server.Close()
	client := NewBedrockWithAuth(server.URL, "", "aws_sigv4", "us-east-1")
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	if err != nil || response.Usage.TotalTokens != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseFailsClosedWithoutAmbientCredentials(t *testing.T) {
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI"} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewBedrockWithAuth(server.URL, "", "aws_sigv4", "us-east-1")
	_, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}})
	var failure *Error
	if !errors.As(err, &failure) || failure.StatusCode != http.StatusServiceUnavailable || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestBedrockRejectsUnrepresentableParametersBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	client := NewBedrock(server.URL, "key")
	seed := int64(1)
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}, Seed: &seed}
	_, err := client.ChatCompletions(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "seed") || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestBedrockRejectsInconsistentUsage(t *testing.T) {
	response := bedrockResponse{StopReason: "end_turn"}
	response.Output.Message.Content = []bedrockContentBlock{{Text: "hello"}}
	response.Usage = &struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	}{InputTokens: 2, OutputTokens: 3, TotalTokens: 4}
	if _, err := bedrockToChat(response, "model"); err == nil {
		t.Fatal("inconsistent usage accepted")
	}
}

func TestBedrockRejectsUnsupportedOutputImage(t *testing.T) {
	response := bedrockResponse{StopReason: "end_turn"}
	image := bedrockImage{Format: "png"}
	image.Source.Bytes = "data"
	response.Output.Message.Content = []bedrockContentBlock{{Image: &image}}
	response.Usage = &struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	}{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}
	if _, err := bedrockToChat(response, "model"); err == nil {
		t.Fatal("unsupported output image was silently discarded")
	}
}
