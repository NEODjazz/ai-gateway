package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnthropicReasoningBlocksRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		blocks, _ := body.Messages[0].Content.([]any)
		if len(blocks) != 3 {
			t.Errorf("reasoning history lost: %+v", blocks)
			return
		}
		first, _ := blocks[0].(map[string]any)
		second, _ := blocks[1].(map[string]any)
		third, _ := blocks[2].(map[string]any)
		if first["type"] != "thinking" || first["signature"] != "signed-in" || second["type"] != "redacted_thinking" || third["text"] != "answer" {
			t.Errorf("reasoning history lost: %+v", blocks)
		}
		_, _ = w.Write([]byte(`{"id":"msg-reason","model":"claude-test","content":[{"type":"thinking","thinking":"private plan","signature":"signed-out"},{"type":"redacted_thinking","data":"opaque-out"},{"type":"text","text":"result"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":6,"output_tokens_details":{"thinking_tokens":3}}}`))
	}))
	defer server.Close()

	client := NewAnthropic(server.URL, "test", false)
	response, err := client.ChatCompletions(t.Context(), openai.ChatCompletionRequest{
		Model: "claude-test",
		Messages: []openai.Message{{Role: "assistant", Content: "answer", Reasoning: []openai.ReasoningBlock{
			{Type: "thinking", Thinking: "private input", Signature: "signed-in"},
			{Type: "redacted_thinking", Data: "opaque-in"},
		}}, {Role: "user", Content: "continue"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := response.Choices[0].Message.Reasoning
	if len(got) != 2 || got[0].Thinking != "private plan" || got[0].Signature != "signed-out" || got[1].Data != "opaque-out" || response.Usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("reasoning response lost: %+v", response)
	}
}

func TestAnthropicReasoningBlocksStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"message\":{\"id\":\"msg-reason\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":4}}}\n\n" +
			"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"private plan\"}}\n\n" +
			"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"signed-out\"}}\n\n" +
			"event: content_block_stop\ndata: {\"index\":0}\n\n" +
			"event: content_block_start\ndata: {\"index\":1,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"opaque-out\"}}\n\n" +
			"event: content_block_stop\ndata: {\"index\":1}\n\n" +
			"event: content_block_delta\ndata: {\"index\":2,\"delta\":{\"type\":\"text_delta\",\"text\":\"result\"}}\n\n" +
			"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":6,\"output_tokens_details\":{\"thinking_tokens\":3}}}\n\n" +
			"event: message_stop\ndata: {}\n\n"))
	}))
	defer server.Close()

	client := NewAnthropic(server.URL, "test", true)
	var chunks []string
	response, err := client.StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "claude-test", Messages: []openai.Message{{Role: "user", Content: "hi"}}}, func(payload string) error {
		chunks = append(chunks, payload)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := response.Choices[0].Message.Reasoning
	joined := strings.Join(chunks, "\n")
	if len(got) != 2 || got[0].Thinking != "private plan" || got[0].Signature != "signed-out" || got[1].Data != "opaque-out" || !strings.Contains(joined, `"reasoning"`) {
		t.Fatalf("stream reasoning lost: response=%+v chunks=%s", response, joined)
	}
}

func TestReasoningBlocksRequireExplicitAdapterSupport(t *testing.T) {
	request := openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", Reasoning: []openai.ReasoningBlock{{Type: "thinking", Thinking: "plan", Signature: "signed"}}}}}
	if err := validateChatAdapter(Demo{}, request); err == nil {
		t.Fatal("reasoning history was silently discarded by an unsupported adapter")
	}
	request.Messages[0].Role = "user"
	if err := validateChatAdapter(NewAnthropic("http://unused.invalid", "", false), request); err == nil {
		t.Fatal("reasoning history was accepted on a user message")
	}
	if err := validateChatCompletionEnvelope(openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{Reasoning: []openai.ReasoningBlock{{Type: "thinking", Thinking: "unsigned"}}}}}}); err == nil {
		t.Fatal("invalid provider reasoning response was accepted")
	}
}
