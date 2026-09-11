package provider

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func bedrockTestEvent(t *testing.T, eventType string, payload any) []byte {
	t.Helper()
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var headers bytes.Buffer
	for _, pair := range [][2]string{{":message-type", "event"}, {":event-type", eventType}, {":content-type", "application/json"}} {
		headers.WriteByte(byte(len(pair[0])))
		headers.WriteString(pair[0])
		headers.WriteByte(7)
		_ = binary.Write(&headers, binary.BigEndian, uint16(len(pair[1])))
		headers.WriteString(pair[1])
	}
	total := 16 + headers.Len() + len(encodedPayload)
	message := make([]byte, total)
	binary.BigEndian.PutUint32(message[:4], uint32(total))
	binary.BigEndian.PutUint32(message[4:8], uint32(headers.Len()))
	binary.BigEndian.PutUint32(message[8:12], crc32.ChecksumIEEE(message[:8]))
	copy(message[12:], headers.Bytes())
	copy(message[12+headers.Len():], encodedPayload)
	binary.BigEndian.PutUint32(message[total-4:], crc32.ChecksumIEEE(message[:total-4]))
	return message
}

func TestBedrockConverseStreamConsumesNativeEventStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/model/model:1/converse-stream" || r.Header.Get("Authorization") != "Bearer key" {
			t.Fatalf("path=%q authorization=%q", r.URL.EscapedPath(), r.Header.Get("Authorization"))
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["messages"] == nil {
			t.Fatalf("request=%#v", body)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		events := [][]byte{
			bedrockTestEvent(t, "messageStart", map[string]any{"role": "assistant"}),
			bedrockTestEvent(t, "contentBlockStart", map[string]any{"contentBlockIndex": 0, "start": map[string]any{}}),
			bedrockTestEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "hello "}}),
			bedrockTestEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "stream"}}),
			bedrockTestEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"citation": map[string]any{"title": "source", "location": map[string]any{"web": map[string]any{"url": "https://example.test/source"}}}}}),
			bedrockTestEvent(t, "contentBlockStop", map[string]any{"contentBlockIndex": 0}),
			bedrockTestEvent(t, "messageStop", map[string]any{"stopReason": "end_turn"}),
			bedrockTestEvent(t, "metadata", map[string]any{"usage": map[string]any{"inputTokens": 4, "outputTokens": 2, "totalTokens": 6}, "metrics": map[string]any{"latencyMs": 12}}),
		}
		for _, event := range events {
			_, _ = w.Write(event)
		}
	}))
	defer server.Close()

	request := openai.ChatCompletionRequest{Model: "model:1", Stream: true, StreamOptions: &openai.ChatStreamOptions{IncludeUsage: true}, Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	var payloads []string
	response, err := NewBedrock(server.URL, "key").StreamChatCompletions(t.Context(), request, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil || openai.ContentText(response.Choices[0].Message.Content) != "hello stream" || response.Choices[0].FinishReason != "stop" || response.Usage.TotalTokens != 6 || len(response.Choices[0].Message.Annotations) != 1 || response.Choices[0].Message.Annotations[0].URLCitation.URL != "https://example.test/source" || len(payloads) != 4 {
		t.Fatalf("response=%+v payloads=%v err=%v", response, payloads, err)
	}
	if !strings.Contains(payloads[1], `"content":"hello "`) || !strings.Contains(payloads[2], `"content":"stream"`) || !strings.Contains(payloads[3], `"finish_reason":"stop"`) || !strings.Contains(payloads[3], `"total_tokens":6`) {
		t.Fatalf("payloads=%v", payloads)
	}
}

func TestBedrockInvokeStreamConsumesAnthropicChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/model/model:1/invoke-with-response-stream" || r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("X-Amzn-Bedrock-Accept") != "application/json" {
			t.Fatalf("path=%q authorization=%q accept=%q", r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("X-Amzn-Bedrock-Accept"))
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["anthropic_version"] != "bedrock-2023-05-31" || body["model"] != nil {
			t.Fatalf("request=%#v", body)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"upstream","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"stream"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		}
		for _, event := range events {
			_, _ = w.Write(bedrockTestEvent(t, "chunk", map[string]any{"bytes": []byte(event)}))
		}
	}))
	defer server.Close()
	maxTokens := 32
	request := openai.ChatCompletionRequest{Model: "model:1", BedrockInvoke: true, Stream: true, StreamOptions: &openai.ChatStreamOptions{IncludeUsage: true}, MaxTokens: &maxTokens, Messages: []openai.Message{{Role: "user", Content: "hello"}}}
	var payloads []string
	response, err := NewBedrock(server.URL, "key").StreamChatCompletions(t.Context(), request, func(payload string) error {
		payloads = append(payloads, payload)
		return nil
	})
	if err != nil || response.ID != "msg_1" || openai.ContentText(response.Choices[0].Message.Content) != "hello stream" || response.Choices[0].FinishReason != "stop" || response.Usage.TotalTokens != 6 {
		t.Fatalf("response=%+v payloads=%v err=%v", response, payloads, err)
	}
	if len(payloads) != 3 || !strings.Contains(payloads[0], `"content":"hello "`) || !strings.Contains(payloads[1], `"content":"stream"`) || !strings.Contains(payloads[2], `"finish_reason":"stop"`) {
		t.Fatalf("payloads=%v", payloads)
	}
}

func TestBedrockInvokeStreamRejectsMissingUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(bedrockTestEvent(t, "chunk", map[string]any{"bytes": []byte(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"model","content":[]}}`)}))
	}))
	defer server.Close()
	maxTokens := 1
	_, err := NewBedrock(server.URL, "key").StreamChatCompletions(t.Context(), openai.ChatCompletionRequest{Model: "model", BedrockInvoke: true, Stream: true, MaxTokens: &maxTokens, Messages: []openai.Message{{Role: "user", Content: "hello"}}}, func(string) error { return nil })
	if err == nil {
		t.Fatal("InvokeModel stream without usage accepted")
	}
}

func TestBedrockInvokeStreamCancelsUpstreamWhenWriterFails(t *testing.T) {
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		} {
			_, _ = w.Write(bedrockTestEvent(t, "chunk", map[string]any{"bytes": []byte(event)}))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	maxTokens := 1
	_, err := NewBedrock(server.URL, "key").StreamChatCompletions(ctx, openai.ChatCompletionRequest{Model: "model", BedrockInvoke: true, Stream: true, MaxTokens: &maxTokens, Messages: []openai.Message{{Role: "user", Content: "hello"}}}, func(string) error {
		return errors.New("client writer closed")
	})
	if err == nil || !strings.Contains(err.Error(), "client writer closed") {
		t.Fatalf("error=%v", err)
	}
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("upstream request was not canceled after writer failure")
	}
}

func TestBedrockInvokeStreamRejectsOrphanContentDelta(t *testing.T) {
	state := bedrockInvokeStreamEnvelopeState{}
	var output bytes.Buffer
	handle := state.handle(&output)
	headers := map[string]string{":message-type": "event", ":event-type": "chunk", ":content-type": "application/json"}
	wrap := func(event string) []byte {
		encoded, err := json.Marshal(map[string]any{"bytes": []byte(event)})
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	if err := handle(headers, wrap(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1,"output_tokens":0}}}`)); err != nil {
		t.Fatal(err)
	}
	err := handle(headers, wrap(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"orphan"}}`))
	if err == nil {
		t.Fatal("orphan content delta accepted")
	}
}

func TestBedrockConverseStreamConsumesToolUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		for _, event := range [][]byte{
			bedrockTestEvent(t, "messageStart", map[string]any{"role": "assistant"}),
			bedrockTestEvent(t, "contentBlockStart", map[string]any{"contentBlockIndex": 0, "start": map[string]any{"toolUse": map[string]any{"toolUseId": "call_1", "name": "weather"}}}),
			bedrockTestEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"toolUse": map[string]any{"input": `{"city":`}}}),
			bedrockTestEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"toolUse": map[string]any{"input": `"Paris"}`}}}),
			bedrockTestEvent(t, "contentBlockStop", map[string]any{"contentBlockIndex": 0}),
			bedrockTestEvent(t, "messageStop", map[string]any{"stopReason": "tool_use"}),
			bedrockTestEvent(t, "metadata", map[string]any{"usage": map[string]any{"inputTokens": 5, "outputTokens": 3, "totalTokens": 8}, "metrics": map[string]any{"latencyMs": 2}}),
		} {
			_, _ = w.Write(event)
		}
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Stream: true, Messages: []openai.Message{{Role: "user", Content: "weather"}}, Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}}}}}
	var payloads []string
	response, err := NewBedrock(server.URL, "key").StreamChatCompletions(t.Context(), request, func(payload string) error { payloads = append(payloads, payload); return nil })
	if err != nil || response.Choices[0].FinishReason != "tool_calls" || len(response.Choices[0].Message.ToolCalls) != 1 || response.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` || len(payloads) != 5 {
		t.Fatalf("response=%+v payloads=%v err=%v", response, payloads, err)
	}
}

func TestBedrockConverseStreamPreservesReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		for _, event := range [][]byte{
			bedrockTestEvent(t, "messageStart", map[string]any{"role": "assistant"}),
			bedrockTestEvent(t, "contentBlockStart", map[string]any{"contentBlockIndex": 0, "start": map[string]any{}}),
			bedrockTestEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"reasoningContent": map[string]any{"text": "private plan"}}}),
			bedrockTestEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"reasoningContent": map[string]any{"signature": "signed"}}}),
			bedrockTestEvent(t, "contentBlockStop", map[string]any{"contentBlockIndex": 0}),
			bedrockTestEvent(t, "contentBlockStart", map[string]any{"contentBlockIndex": 1, "start": map[string]any{}}),
			bedrockTestEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 1, "delta": map[string]any{"text": "answer"}}),
			bedrockTestEvent(t, "contentBlockStop", map[string]any{"contentBlockIndex": 1}),
			bedrockTestEvent(t, "contentBlockStart", map[string]any{"contentBlockIndex": 2, "start": map[string]any{}}),
			bedrockTestEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 2, "delta": map[string]any{"reasoningContent": map[string]any{"redactedContent": "b3BhcXVl"}}}),
			bedrockTestEvent(t, "contentBlockStop", map[string]any{"contentBlockIndex": 2}),
			bedrockTestEvent(t, "messageStop", map[string]any{"stopReason": "end_turn"}),
			bedrockTestEvent(t, "metadata", map[string]any{"usage": map[string]any{"inputTokens": 2, "outputTokens": 3, "totalTokens": 5}}),
		} {
			_, _ = w.Write(event)
		}
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Stream: true, Messages: []openai.Message{{Role: "user", Content: "question"}}}
	var payloads []string
	response, err := NewBedrock(server.URL, "key").StreamChatCompletions(t.Context(), request, func(payload string) error { payloads = append(payloads, payload); return nil })
	if err != nil || len(response.Choices[0].Message.Reasoning) != 2 || response.Choices[0].Message.Reasoning[0].Signature != "signed" || response.Choices[0].Message.Reasoning[1].Data != "b3BhcXVl" || openai.ContentText(response.Choices[0].Message.Content) != "answer" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	joined := strings.Join(payloads, "\n")
	if !strings.Contains(joined, `"thinking":"private plan"`) || !strings.Contains(joined, `"signature":"signed"`) || !strings.Contains(joined, `"data":"b3BhcXVl"`) {
		t.Fatalf("reasoning stream chunks=%s", joined)
	}
}

func TestBedrockEventStreamRejectsCorruptChecksumAndTruncation(t *testing.T) {
	valid := bedrockTestEvent(t, "messageStart", map[string]any{"role": "assistant"})
	corrupt := append([]byte(nil), valid...)
	corrupt[len(corrupt)-1] ^= 0xff
	for name, stream := range map[string][]byte{"corrupt": corrupt, "truncated": valid[:len(valid)-1]} {
		t.Run(name, func(t *testing.T) {
			if err := readBedrockEventStream(bytes.NewReader(stream), func(map[string]string, []byte) error { return nil }); err == nil {
				t.Fatal("invalid event stream accepted")
			}
		})
	}
}

func TestBedrockConverseStreamRejectsMixedReasoningDelta(t *testing.T) {
	state := bedrockStreamState{model: "model", blocks: map[int]*bedrockStreamBlock{0: {started: true}}}
	err := state.contentDelta([]byte(`{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"plan","signature":"signed"}}}`), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "one union member") {
		t.Fatalf("mixed reasoning union accepted: %v", err)
	}
}

func TestBedrockConverseStreamAcceptsUnsignedReasoningText(t *testing.T) {
	state := bedrockStreamState{model: "model", blocks: map[int]*bedrockStreamBlock{0: {started: true}}}
	if err := state.contentDelta([]byte(`{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"plan"}}}`), func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := state.contentStop([]byte(`{"contentBlockIndex":0}`)); err != nil {
		t.Fatal(err)
	}
	if got := state.response.Output.Message.Content[0].ReasoningContent.ReasoningText; got == nil || got.Text != "plan" || got.Signature != "" {
		t.Fatalf("reasoning=%+v", got)
	}
}

func TestBedrockConverseStreamMapsProviderExceptions(t *testing.T) {
	var headers bytes.Buffer
	for _, pair := range [][2]string{{":message-type", "exception"}, {":exception-type", "throttlingException"}, {":content-type", "application/json"}} {
		headers.WriteByte(byte(len(pair[0])))
		headers.WriteString(pair[0])
		headers.WriteByte(7)
		_ = binary.Write(&headers, binary.BigEndian, uint16(len(pair[1])))
		headers.WriteString(pair[1])
	}
	payload := []byte(`{"message":"slow down"}`)
	total := 16 + headers.Len() + len(payload)
	frame := make([]byte, total)
	binary.BigEndian.PutUint32(frame[:4], uint32(total))
	binary.BigEndian.PutUint32(frame[4:8], uint32(headers.Len()))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], headers.Bytes())
	copy(frame[12+headers.Len():], payload)
	binary.BigEndian.PutUint32(frame[total-4:], crc32.ChecksumIEEE(frame[:total-4]))
	state := bedrockStreamState{blocks: make(map[int]*bedrockStreamBlock)}
	err := readBedrockEventStream(bytes.NewReader(frame), state.handle(func(string) error { return nil }))
	var providerError *Error
	if !errors.As(err, &providerError) || providerError.Class != FailureRateLimit || providerError.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error=%v", err)
	}
}
