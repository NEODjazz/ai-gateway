package provider

import (
	"ai-gateway-gateway/internal/openai"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicMatchedStopIsPreservedInJSONAndStream(t *testing.T) {
	sequence := " END "
	result := anthropicToChatCompletion(anthropicResponse{StopReason: "stop_sequence", StopSequence: &sequence}, "model")
	if result.Choices[0].StopSequence == nil || *result.Choices[0].StopSequence != sequence {
		t.Fatal("matched sequence lost")
	}
	result = anthropicToChatCompletion(anthropicResponse{StopReason: "end_turn", StopSequence: &sequence}, "model")
	if result.Choices[0].StopSequence != nil {
		t.Fatal("sequence invented for end_turn")
	}
	var payloads []string
	stream := "event: message_start\ndata: {\"message\":{\"id\":\"id\",\"model\":\"m\"}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"stop_sequence\",\"stop_sequence\":\" END \"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n"
	result, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, func(payload string) error { payloads = append(payloads, payload); return nil })
	if err != nil || result.Choices[0].StopSequence == nil || *result.Choices[0].StopSequence != sequence || !strings.Contains(strings.Join(payloads, ""), `"stop_sequence":" END "`) {
		t.Fatalf("stream metadata lost: %+v %v %v", result, err, payloads)
	}
}

func TestAnthropicRejectsMissingMatchedSequence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"m","content":[],"stop_reason":"stop_sequence"}`))
	}))
	defer server.Close()
	if _, err := NewAnthropic(server.URL, "", false).ChatCompletions(context.Background(), openai.ChatCompletionRequest{Model: "m"}); err == nil {
		t.Fatal("missing JSON delimiter accepted")
	}
	if _, err := streamAnthropicChat(strings.NewReader("event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"stop_sequence\"}}\n\n"), "m", false, nil, nil, false, func(string) error { return nil }); err == nil {
		t.Fatal("missing SSE delimiter accepted")
	}
}
