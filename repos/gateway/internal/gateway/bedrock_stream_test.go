package gateway

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type decodedAWSMessage struct {
	headers map[string]string
	payload map[string]any
}

func decodeAWSMessages(t *testing.T, data []byte) []decodedAWSMessage {
	t.Helper()
	var messages []decodedAWSMessage
	for len(data) > 0 {
		if len(data) < 16 {
			t.Fatalf("truncated event stream message: %d bytes", len(data))
		}
		total := int(binary.BigEndian.Uint32(data[:4]))
		headerLength := int(binary.BigEndian.Uint32(data[4:8]))
		if total < 16 || total > len(data) || headerLength > total-16 || binary.BigEndian.Uint32(data[8:12]) != crc32.ChecksumIEEE(data[:8]) || binary.BigEndian.Uint32(data[total-4:total]) != crc32.ChecksumIEEE(data[:total-4]) {
			t.Fatal("invalid event stream length or checksum")
		}
		headers := make(map[string]string)
		encodedHeaders := data[12 : 12+headerLength]
		for len(encodedHeaders) > 0 {
			nameLength := int(encodedHeaders[0])
			if len(encodedHeaders) < 1+nameLength+3 || encodedHeaders[1+nameLength] != 7 {
				t.Fatal("invalid event stream string header")
			}
			name := string(encodedHeaders[1 : 1+nameLength])
			valueLength := int(binary.BigEndian.Uint16(encodedHeaders[2+nameLength : 4+nameLength]))
			if len(encodedHeaders) < 4+nameLength+valueLength {
				t.Fatal("truncated event stream header")
			}
			headers[name] = string(encodedHeaders[4+nameLength : 4+nameLength+valueLength])
			encodedHeaders = encodedHeaders[4+nameLength+valueLength:]
		}
		var payload map[string]any
		if err := json.Unmarshal(data[12+headerLength:total-4], &payload); err != nil {
			t.Fatalf("decode event payload: %v", err)
		}
		messages = append(messages, decodedAWSMessage{headers: headers, payload: payload})
		data = data[total:]
	}
	return messages
}

func TestEncodeAWSMessageProducesValidChecksumsAndHeaders(t *testing.T) {
	encoded, err := encodeAWSMessage(map[string]string{":message-type": "event", ":event-type": "messageStart", ":content-type": "application/json"}, []byte(`{"role":"assistant"}`))
	if err != nil {
		t.Fatal(err)
	}
	messages := decodeAWSMessages(t, encoded)
	if len(messages) != 1 || messages[0].headers[":event-type"] != "messageStart" || messages[0].payload["role"] != "assistant" {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestBedrockConverseStreamUsesAuthorizationStreamingAndBilling(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil || r.URL.Path != "/model/upstream-model/converse-stream" || r.Header.Get("Authorization") != "Bearer provider-key" || request["messages"] == nil {
			t.Fatalf("path=%s headers=%v upstream request=%+v", r.URL.Path, r.Header, request)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		writeEvent := func(eventType string, payload any) {
			encoded, err := encodeAWSMessage(map[string]string{":message-type": "event", ":event-type": eventType, ":content-type": "application/json"}, mustJSON(t, payload))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(encoded)
		}
		writeEvent("messageStart", map[string]any{"role": "assistant"})
		writeEvent("contentBlockStart", map[string]any{"contentBlockIndex": 0, "start": map[string]any{}})
		writeEvent("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "hello "}})
		writeEvent("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "stream"}})
		writeEvent("contentBlockStop", map[string]any{"contentBlockIndex": 0})
		writeEvent("messageStop", map[string]any{"stopReason": "end_turn"})
		writeEvent("metadata", map[string]any{"usage": map[string]any{"inputTokens": 4, "cacheReadInputTokens": 3, "cacheWriteInputTokens": 2, "outputTokens": 2, "totalTokens": 11}, "metrics": map[string]any{"latencyMs": 1}})
	}))
	defer upstream.Close()

	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{
		Endpoints: []config.ProviderEndpointConfig{{Name: "streaming", Type: "bedrock", BaseURL: upstream.URL, APIKey: "provider-key", Models: []string{"public-model"}, ModelAliases: map[string]string{"public-model": "upstream-model"}, Capabilities: []string{"chat", "stream"}, Stream: true}},
		Modules:   modules.NewPipeline([]modules.Module{billing}),
	})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public-model"}, tpm: 100}}}), router))
	request := httptest.NewRequest(http.MethodPost, "/model/public-model/converse-stream?provider=streaming", strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"hello"}]}],"inferenceConfig":{"maxTokens":8}}`))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/vnd.amazon.eventstream" || response.Header().Get("X-Execution-ID") == "" {
		t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.Bytes())
	}
	messages := decodeAWSMessages(t, response.Body.Bytes())
	var eventTypes []string
	var streamedText string
	for _, message := range messages {
		eventTypes = append(eventTypes, message.headers[":event-type"])
		if message.headers[":event-type"] == "contentBlockDelta" {
			delta, _ := message.payload["delta"].(map[string]any)
			if text, ok := delta["text"].(string); ok {
				streamedText += text
			}
		}
	}
	if strings.Join(eventTypes, ",") != "messageStart,contentBlockStart,contentBlockDelta,contentBlockDelta,contentBlockStop,messageStop,metadata" || streamedText != "hello stream" {
		t.Fatalf("events=%v text=%q", eventTypes, streamedText)
	}
	metadata := messages[len(messages)-1].payload
	usage, _ := metadata["usage"].(map[string]any)
	details := billing.usage.PromptTokensDetails
	if usage["inputTokens"] != float64(4) || usage["cacheReadInputTokens"] != float64(3) || usage["cacheWriteInputTokens"] != float64(2) || usage["totalTokens"] != float64(11) || upstreamCalls != 1 || billing.calls != 1 || billing.usage.PromptTokens != 9 || billing.usage.TotalTokens != 11 || details == nil || details.CachedTokens != 3 || details.CacheWriteTokens != 2 {
		t.Fatalf("metadata=%+v upstream=%d billing=%+v", metadata, upstreamCalls, billing)
	}
}

func TestBedrockInvokeStreamUsesAuthorizationStreamingAndBilling(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil || r.URL.Path != "/model/upstream-model/invoke-with-response-stream" || r.Header.Get("Authorization") != "Bearer provider-key" || request["anthropic_version"] != "bedrock-2023-05-31" {
			t.Fatalf("path=%s headers=%v upstream request=%+v", r.URL.Path, r.Header, request)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		writeChunk := func(payload string) {
			encoded, err := encodeAWSMessage(map[string]string{":message-type": "event", ":event-type": "chunk", ":content-type": "application/json"}, mustJSON(t, map[string]any{"bytes": []byte(payload)}))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(encoded)
		}
		writeChunk(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"upstream-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":0}}}`)
		writeChunk(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		writeChunk(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello stream"}}`)
		writeChunk(`{"type":"content_block_stop","index":0}`)
		writeChunk(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`)
		writeChunk(`{"type":"message_stop"}`)
	}))
	defer upstream.Close()

	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{
		Endpoints: []config.ProviderEndpointConfig{{Name: "streaming", Type: "bedrock", BaseURL: upstream.URL, APIKey: "provider-key", Models: []string{"public-model"}, ModelAliases: map[string]string{"public-model": "upstream-model"}, Capabilities: []string{"chat", "stream", "bedrock_invoke"}, Stream: true}},
		Modules:   modules.NewPipeline([]modules.Module{billing}),
	})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public-model"}, tpm: 100}}}), router))
	request := httptest.NewRequest(http.MethodPost, "/model/public-model/invoke-with-response-stream?provider=streaming", strings.NewReader(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/vnd.amazon.eventstream" || response.Header().Get("X-Execution-ID") == "" {
		t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.Bytes())
	}
	messages := decodeAWSMessages(t, response.Body.Bytes())
	var eventTypes []string
	var streamedText string
	for _, message := range messages {
		if message.headers[":event-type"] != "chunk" {
			t.Fatalf("unexpected headers=%v", message.headers)
		}
		encoded, _ := message.payload["bytes"].(string)
		payload, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
		}
		if json.Unmarshal(payload, &event) != nil {
			t.Fatalf("payload=%q", payload)
		}
		eventTypes = append(eventTypes, event.Type)
		streamedText += event.Delta.Text
	}
	if strings.Join(eventTypes, ",") != "message_start,content_block_start,content_block_delta,content_block_stop,message_delta,message_stop" || streamedText != "hello stream" {
		t.Fatalf("events=%v text=%q", eventTypes, streamedText)
	}
	if upstreamCalls != 1 || billing.calls != 1 || billing.usage.TotalTokens != 6 {
		t.Fatalf("upstream=%d billing=%d usage=%+v", upstreamCalls, billing.calls, billing.usage)
	}
}

func TestBedrockInvokeStreamPreservesPreStreamJSONErrors(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/model/model/invoke-with-response-stream", strings.NewReader(`{"anthropic_version":"wrong","max_tokens":8,"messages":[{"role":"user","content":"hello"}]}`))
	Routes(Handler{}).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") != "application/json" || !strings.Contains(response.Body.String(), `"type":"error"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestBedrockConverseStreamReturnsJSONBeforeFirstEvent(t *testing.T) {
	response := httptest.NewRecorder()
	Routes(Handler{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/model/converse-stream", strings.NewReader(`{"messages":[]}`)))
	if response.Code != http.StatusBadRequest || response.Header().Get("Content-Type") == "application/vnd.amazon.eventstream" || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestBedrockConverseStreamSynthesizesNonStreamingResponse(t *testing.T) {
	upstream := &chatProvider{}
	response := httptest.NewRecorder()
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/model/model/converse-stream", strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"hello"}]}]}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.Bytes())
	}
	messages := decodeAWSMessages(t, response.Body.Bytes())
	if len(messages) != 6 || messages[2].headers[":event-type"] != "contentBlockDelta" || messages[2].payload["delta"].(map[string]any)["text"] != "hello stream" || messages[5].headers[":event-type"] != "metadata" {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestBedrockStreamWriterBuffersSplitToolUse(t *testing.T) {
	destination := httptest.NewRecorder()
	w := newBedrockStreamWriter(destination)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, frame := range []string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_","type":"function","function":{"name":"wea","arguments":"{\"city\":"}}]}}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"1","function":{"name":"ther","arguments":"\"Paris\"}"}}]}}]}` + "\n\n",
	} {
		if _, err := w.Write([]byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	response := openai.ChatCompletionResponse{
		Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call_1", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}}}}, FinishReason: "tool_calls"}},
		Usage:   openai.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	}
	w.chatStreamResult(response)
	if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
		t.Fatal(err)
	}
	w.finish()
	messages := decodeAWSMessages(t, destination.Body.Bytes())
	if len(messages) != 6 || messages[1].headers[":event-type"] != "contentBlockStart" || messages[1].payload["start"].(map[string]any)["toolUse"].(map[string]any)["name"] != "weather" || messages[2].payload["delta"].(map[string]any)["toolUse"].(map[string]any)["input"] != `{"city":"Paris"}` {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestBedrockStreamWriterEmitsReasoningContent(t *testing.T) {
	destination := httptest.NewRecorder()
	w := newBedrockStreamWriter(destination)
	w.Header().Set("Content-Type", "text/event-stream")
	response := openai.ChatCompletionResponse{
		Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "answer", Reasoning: []openai.ReasoningBlock{
			{Type: "thinking", Thinking: "private plan", Signature: "signed"},
			{Type: "redacted_thinking", Data: "b3BhcXVl"},
		}}, FinishReason: "stop"}},
		Usage: openai.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	}
	w.chatResult(response, false)
	w.finish()
	if w.err != nil {
		t.Fatal(w.err)
	}
	messages := decodeAWSMessages(t, destination.Body.Bytes())
	var reasoning []map[string]any
	for _, message := range messages {
		if message.headers[":event-type"] != "contentBlockDelta" {
			continue
		}
		delta, _ := message.payload["delta"].(map[string]any)
		if value, ok := delta["reasoningContent"].(map[string]any); ok {
			reasoning = append(reasoning, value)
		}
	}
	if len(reasoning) != 3 || reasoning[0]["text"] != "private plan" || reasoning[1]["signature"] != "signed" || reasoning[2]["redactedContent"] != "b3BhcXVl" {
		t.Fatalf("reasoning=%+v messages=%+v", reasoning, messages)
	}
}
