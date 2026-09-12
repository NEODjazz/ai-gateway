package provider

import (
	"ai-gateway-gateway/internal/openai"
	"context"
	"encoding/json"
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
	result, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "", nil, func(payload string) error { payloads = append(payloads, payload); return nil })
	if err != nil || result.Choices[0].StopSequence == nil || *result.Choices[0].StopSequence != sequence || !strings.Contains(strings.Join(payloads, ""), `"stop_sequence":" END "`) {
		t.Fatalf("stream metadata lost: %+v %v %v", result, err, payloads)
	}
}

func TestAnthropicStreamPreservesContextManagement(t *testing.T) {
	stream := "event: message_start\ndata: {\"message\":{\"id\":\"id\",\"model\":\"m\"}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1},\"context_management\":{\"applied_edits\":[{\"type\":\"clear_thinking_20251015\",\"cleared_thinking_turns\":2,\"cleared_input_tokens\":1000}]}}\n\nevent: message_stop\ndata: {}\n\n"
	request := json.RawMessage(`{"edits":[{"type":"clear_thinking_20251015"}]}`)
	result, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "", request, func(string) error { return nil })
	if err != nil || !json.Valid(result.NativeContextManagement) || !strings.Contains(string(result.NativeContextManagement), "cleared_thinking_turns") {
		t.Fatalf("context=%s err=%v", result.NativeContextManagement, err)
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
	if _, err := streamAnthropicChat(strings.NewReader("event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"stop_sequence\"}}\n\n"), "m", false, nil, nil, false, "", nil, func(string) error { return nil }); err == nil {
		t.Fatal("missing SSE delimiter accepted")
	}
}

func TestAnthropicStreamRequiresRequestedInferenceGeo(t *testing.T) {
	stream := "event: message_start\ndata: {\"message\":{\"id\":\"id\",\"model\":\"m\",\"usage\":{\"inference_geo\":\"us\"}}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n"
	result, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "us", nil, func(string) error { return nil })
	if err != nil || result.Usage.InferenceGeo != "us" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := streamAnthropicChat(strings.NewReader(stream), "model", false, nil, nil, false, "global", nil, func(string) error { return nil }); err == nil {
		t.Fatal("streamed inference geography mismatch accepted")
	}
}
