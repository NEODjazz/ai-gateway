package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const maxBedrockInvokeEventBytes = 16 << 20

type bedrockInvokeStreamWriter struct {
	messages *messagesWriter
	bridge   *bedrockInvokeEventBridge
}

func newBedrockInvokeStreamWriter(destination http.ResponseWriter) *bedrockInvokeStreamWriter {
	bridge := &bedrockInvokeEventBridge{
		frames: &bedrockStreamWriter{destination: destination, headers: make(http.Header), status: http.StatusOK},
		status: http.StatusOK,
	}
	return &bedrockInvokeStreamWriter{
		messages: &messagesWriter{destination: bridge, headers: make(http.Header), status: http.StatusOK, tools: map[int]int{}},
		bridge:   bridge,
	}
}

func (w *bedrockInvokeStreamWriter) Header() http.Header            { return w.messages.Header() }
func (w *bedrockInvokeStreamWriter) WriteHeader(status int)         { w.messages.WriteHeader(status) }
func (w *bedrockInvokeStreamWriter) Write(data []byte) (int, error) { return w.messages.Write(data) }
func (w *bedrockInvokeStreamWriter) Flush()                         { w.messages.Flush() }
func (w *bedrockInvokeStreamWriter) chatStreamResult(response openai.ChatCompletionResponse) {
	w.messages.chatStreamResult(response)
}
func (w *bedrockInvokeStreamWriter) chatResult(response openai.ChatCompletionResponse, stream bool) {
	w.messages.chatResult(response, stream)
}
func (w *bedrockInvokeStreamWriter) finish() {
	w.messages.finish()
	w.bridge.finish()
}

type bedrockInvokeEventBridge struct {
	frames *bedrockStreamWriter
	status int
	buffer bytes.Buffer
}

func (w *bedrockInvokeEventBridge) Header() http.Header { return w.frames.headers }
func (w *bedrockInvokeEventBridge) WriteHeader(status int) {
	w.status = status
}
func (w *bedrockInvokeEventBridge) Flush() { w.frames.Flush() }

func (w *bedrockInvokeEventBridge) Write(data []byte) (int, error) {
	if len(data) > maxBedrockStreamBytes-w.buffer.Len() {
		return 0, errors.New("Bedrock InvokeModel stream exceeds limit")
	}
	w.buffer.Write(data)
	if w.Header().Get("Content-Type") != "text/event-stream" {
		return len(data), nil
	}
	for {
		end := bytes.Index(w.buffer.Bytes(), []byte("\n\n"))
		if end < 0 {
			break
		}
		event := string(w.buffer.Next(end + 2))
		payload := ""
		for _, line := range strings.Split(event, "\n") {
			if strings.HasPrefix(line, "data:") {
				payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if payload == "" || payload == "[DONE]" || len(payload) > maxBedrockInvokeEventBytes || !json.Valid([]byte(payload)) {
			return 0, errors.New("invalid Bedrock InvokeModel stream payload")
		}
		wrapped, err := json.Marshal(struct {
			Bytes []byte `json:"bytes"`
		}{Bytes: []byte(payload)})
		if err != nil {
			return 0, err
		}
		if err := w.frames.frame(map[string]string{":message-type": "event", ":event-type": "chunk", ":content-type": "application/json"}, wrapped); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

func (w *bedrockInvokeEventBridge) finish() {
	if w.frames.started {
		return
	}
	for key, values := range w.Header() {
		w.frames.destination.Header()[key] = append([]string(nil), values...)
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.frames.destination.WriteHeader(status)
	if w.buffer.Len() > 0 {
		_, _ = w.frames.destination.Write(w.buffer.Bytes())
	}
}
