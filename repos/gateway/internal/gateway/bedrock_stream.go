package gateway

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

const maxBedrockStreamBytes = 64 << 20

type bedrockStreamTool struct {
	id, name strings.Builder
	input    strings.Builder
}

type bedrockStreamWriter struct {
	destination http.ResponseWriter
	headers     http.Header
	status      int
	buffer      bytes.Buffer
	started     bool
	terminal    bool
	message     bool
	textBlock   bool
	bytes       int
	tools       [128]bedrockStreamTool
	toolSeen    [128]bool
	result      *openai.ChatCompletionResponse
	startedAt   time.Time
	err         error
}

func newBedrockStreamWriter(destination http.ResponseWriter) *bedrockStreamWriter {
	return &bedrockStreamWriter{destination: destination, headers: make(http.Header), status: http.StatusOK, startedAt: time.Now()}
}

func (w *bedrockStreamWriter) Header() http.Header { return w.headers }

func (w *bedrockStreamWriter) WriteHeader(status int) { w.status = status }

func (w *bedrockStreamWriter) Flush() {
	if flusher, ok := w.destination.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *bedrockStreamWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if len(data) > maxBedrockStreamBytes-w.buffer.Len() {
		w.err = errors.New("stream frame exceeds limit")
		return 0, w.err
	}
	w.buffer.Write(data)
	if w.headers.Get("Content-Type") != "text/event-stream" {
		return len(data), nil
	}
	for {
		end := bytes.Index(w.buffer.Bytes(), []byte("\n\n"))
		if end < 0 {
			break
		}
		frame := string(w.buffer.Next(end + 2))
		payload := strings.TrimSpace(strings.TrimPrefix(frame, "data:"))
		if err := w.chunk(payload); err != nil {
			w.err = err
			return 0, err
		}
	}
	return len(data), nil
}

func (w *bedrockStreamWriter) chatStreamResult(response openai.ChatCompletionResponse) {
	copy := response
	w.result = &copy
}

func (w *bedrockStreamWriter) chatResult(response openai.ChatCompletionResponse, _ bool) {
	w.chatStreamResult(response)
	if err := w.emitCompleteResponse(response); err != nil {
		w.err = err
	}
}

func (w *bedrockStreamWriter) chunk(payload string) error {
	if w.terminal {
		return nil
	}
	if payload == "[DONE]" {
		if w.result == nil {
			return errors.New("stream ended without a final response")
		}
		return w.complete(*w.result)
	}
	var chunk struct {
		Choices []struct {
			Index int            `json:"index"`
			Delta openai.Message `json:"delta"`
		} `json:"choices"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return err
	}
	if len(chunk.Error) > 0 {
		w.terminal = true
		return w.exception("internalServerException", "inference stream failed")
	}
	if len(chunk.Choices) > 1 {
		return errors.New("multiple stream choices are unsupported")
	}
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			return errors.New("invalid stream choice index")
		}
		if text := openai.ContentText(choice.Delta.Content); text != "" {
			if err := w.startText(); err != nil {
				return err
			}
			if err := w.event("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": text}}); err != nil {
				return err
			}
		}
		for _, delta := range choice.Delta.ToolCalls {
			if delta.Index == nil || *delta.Index < 0 || *delta.Index >= len(w.tools) || delta.Type != "" && delta.Type != "function" {
				return errors.New("invalid tool stream delta")
			}
			index := *delta.Index
			extra := len(delta.ID) + len(delta.Function.Name) + len(delta.Function.Arguments)
			if extra > maxBedrockStreamBytes-w.bytes {
				return errors.New("tool stream exceeds limit")
			}
			w.bytes += extra
			w.toolSeen[index] = true
			w.tools[index].id.WriteString(delta.ID)
			w.tools[index].name.WriteString(delta.Function.Name)
			w.tools[index].input.WriteString(delta.Function.Arguments)
		}
	}
	return nil
}

func (w *bedrockStreamWriter) startMessage() error {
	if w.message {
		return nil
	}
	w.message = true
	return w.event("messageStart", map[string]any{"role": "assistant"})
}

func (w *bedrockStreamWriter) startText() error {
	if err := w.startMessage(); err != nil {
		return err
	}
	if w.textBlock {
		return nil
	}
	w.textBlock = true
	return w.event("contentBlockStart", map[string]any{"contentBlockIndex": 0, "start": map[string]any{}})
}

func (w *bedrockStreamWriter) complete(response openai.ChatCompletionResponse) error {
	native, err := openai.BedrockFromChat(response)
	if err != nil {
		return err
	}
	if err := w.startMessage(); err != nil {
		return err
	}
	if !w.textBlock {
		if text := openai.ContentText(response.Choices[0].Message.Content); text != "" {
			if err := w.startText(); err != nil {
				return err
			}
			if err := w.event("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": text}}); err != nil {
				return err
			}
		}
	}
	for index, call := range response.Choices[0].Message.ToolCalls {
		if index >= len(w.tools) {
			return errors.New("too many tool calls")
		}
		if w.toolSeen[index] {
			continue
		}
		w.toolSeen[index] = true
		w.tools[index].id.WriteString(call.ID)
		w.tools[index].name.WriteString(call.Function.Name)
		w.tools[index].input.WriteString(call.Function.Arguments)
	}
	if w.textBlock {
		for _, block := range native.Output.Message.Content {
			if block.CitationsContent == nil {
				continue
			}
			for _, citation := range block.CitationsContent.Citations {
				if err := w.event("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"citation": citation}}); err != nil {
					return err
				}
			}
		}
		if err := w.event("contentBlockStop", map[string]any{"contentBlockIndex": 0}); err != nil {
			return err
		}
	}
	blockIndex := 0
	if w.textBlock {
		blockIndex = 1
	}
	for index := range w.tools {
		if !w.toolSeen[index] {
			continue
		}
		tool := &w.tools[index]
		if tool.id.Len() == 0 || !openai.ValidBedrockToolName(tool.name.String()) || !json.Valid([]byte(tool.input.String())) {
			return errors.New("invalid completed tool stream")
		}
		if err := w.event("contentBlockStart", map[string]any{"contentBlockIndex": blockIndex, "start": map[string]any{"toolUse": map[string]any{"toolUseId": tool.id.String(), "name": tool.name.String()}}}); err != nil {
			return err
		}
		if err := w.event("contentBlockDelta", map[string]any{"contentBlockIndex": blockIndex, "delta": map[string]any{"toolUse": map[string]any{"input": tool.input.String()}}}); err != nil {
			return err
		}
		if err := w.event("contentBlockStop", map[string]any{"contentBlockIndex": blockIndex}); err != nil {
			return err
		}
		blockIndex++
	}
	stop := map[string]any{"stopReason": native.StopReason}
	if len(native.AdditionalModelResponseFields) > 0 {
		stop["additionalModelResponseFields"] = native.AdditionalModelResponseFields
	}
	if err := w.event("messageStop", stop); err != nil {
		return err
	}
	metadata := map[string]any{
		"usage":   native.Usage,
		"metrics": map[string]int64{"latencyMs": max(0, time.Since(w.startedAt).Milliseconds())},
	}
	if len(native.Trace) > 0 {
		metadata["trace"] = native.Trace
	}
	if response.ServiceTier != "" {
		metadata["serviceTier"] = map[string]string{"type": response.ServiceTier}
	}
	if err := w.event("metadata", metadata); err != nil {
		return err
	}
	w.terminal = true
	return nil
}

func (w *bedrockStreamWriter) emitCompleteResponse(response openai.ChatCompletionResponse) error {
	if len(response.Choices) != 1 {
		return errors.New("invalid provider response")
	}
	message := response.Choices[0].Message
	if text := openai.ContentText(message.Content); text != "" {
		if err := w.startText(); err != nil {
			return err
		}
		if err := w.event("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": text}}); err != nil {
			return err
		}
	}
	for index, call := range message.ToolCalls {
		if index >= len(w.tools) {
			return errors.New("too many tool calls")
		}
		w.toolSeen[index] = true
		w.tools[index].id.WriteString(call.ID)
		w.tools[index].name.WriteString(call.Function.Name)
		w.tools[index].input.WriteString(call.Function.Arguments)
	}
	return w.complete(response)
}

func (w *bedrockStreamWriter) event(eventType string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return w.frame(map[string]string{":message-type": "event", ":event-type": eventType, ":content-type": "application/json"}, payload)
}

func (w *bedrockStreamWriter) exception(exceptionType, message string) error {
	payload, _ := json.Marshal(map[string]string{"message": message})
	return w.frame(map[string]string{":message-type": "exception", ":exception-type": exceptionType, ":content-type": "application/json"}, payload)
}

func (w *bedrockStreamWriter) frame(headers map[string]string, payload []byte) error {
	if !w.started {
		for key, values := range w.headers {
			if key == "Content-Type" {
				continue
			}
			w.destination.Header()[key] = append([]string(nil), values...)
		}
		w.destination.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.destination.Header().Set("x-amzn-bedrock-content-type", "application/json")
		w.destination.WriteHeader(http.StatusOK)
		w.started = true
	}
	encoded, err := encodeAWSMessage(headers, payload)
	if err != nil {
		return err
	}
	if len(encoded) > maxBedrockStreamBytes-w.bytes {
		return errors.New("Bedrock event stream exceeds limit")
	}
	w.bytes += len(encoded)
	if _, err := w.destination.Write(encoded); err != nil {
		return err
	}
	w.Flush()
	return nil
}

func encodeAWSMessage(headers map[string]string, payload []byte) ([]byte, error) {
	var headerBytes bytes.Buffer
	for _, name := range []string{":message-type", ":event-type", ":exception-type", ":content-type"} {
		value, ok := headers[name]
		if !ok {
			continue
		}
		if len(name) > 255 || len(value) > 65535 {
			return nil, errors.New("event stream header exceeds limit")
		}
		headerBytes.WriteByte(byte(len(name)))
		headerBytes.WriteString(name)
		headerBytes.WriteByte(7)
		_ = binary.Write(&headerBytes, binary.BigEndian, uint16(len(value)))
		headerBytes.WriteString(value)
	}
	total := 16 + headerBytes.Len() + len(payload)
	if total > maxBedrockStreamBytes {
		return nil, errors.New("event stream message exceeds limit")
	}
	message := make([]byte, total)
	binary.BigEndian.PutUint32(message[0:4], uint32(total))
	binary.BigEndian.PutUint32(message[4:8], uint32(headerBytes.Len()))
	binary.BigEndian.PutUint32(message[8:12], crc32.ChecksumIEEE(message[:8]))
	copy(message[12:], headerBytes.Bytes())
	copy(message[12+headerBytes.Len():], payload)
	binary.BigEndian.PutUint32(message[total-4:], crc32.ChecksumIEEE(message[:total-4]))
	return message, nil
}

func (w *bedrockStreamWriter) finish() {
	if w.started {
		if !w.terminal {
			_ = w.exception("internalServerException", "inference stream did not complete")
		}
		return
	}
	for key, values := range w.headers {
		w.destination.Header()[key] = append([]string(nil), values...)
	}
	if w.err != nil {
		w.status = http.StatusBadGateway
	}
	w.destination.WriteHeader(w.status)
	_, _ = w.destination.Write(w.buffer.Bytes())
}
