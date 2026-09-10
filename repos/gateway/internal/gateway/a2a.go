package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const a2aProtocolVersion = "1.0"

type a2aPart struct {
	Text      *string         `json:"text,omitempty"`
	Raw       *string         `json:"raw,omitempty"`
	URL       *string         `json:"url,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
	Filename  string          `json:"filename,omitempty"`
}

type a2aMessage struct {
	MessageID string         `json:"messageId"`
	ContextID string         `json:"contextId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Role      string         `json:"role"`
	Parts     []a2aPart      `json:"parts"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type a2aRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		Tenant        string     `json:"tenant"`
		Message       a2aMessage `json:"message"`
		Configuration struct {
			AcceptedOutputModes    []string        `json:"acceptedOutputModes,omitempty"`
			ReturnImmediately      *bool           `json:"returnImmediately,omitempty"`
			PushNotificationConfig json.RawMessage `json:"pushNotificationConfig,omitempty"`
		} `json:"configuration,omitempty"`
	} `json:"params"`
}

type a2aRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type a2aRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *a2aRPCError    `json:"error,omitempty"`
}

func (h Handler) A2AAgentCard(w http.ResponseWriter, r *http.Request) {
	profile, ok := h.a2aProfile(r.PathValue("agent"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	endpoint := scheme + "://" + r.Host + "/a2a/" + profile.ID
	description := profile.Description
	if description == "" {
		description = profile.Name
	}
	tags := profile.Tags
	if len(tags) == 0 {
		tags = []string{"agent"}
	}
	card := map[string]any{
		"name": profile.Name, "description": description, "version": "1.0.0",
		"supportedInterfaces": []any{map[string]any{"url": endpoint, "protocolBinding": "JSONRPC", "tenant": profile.ID, "protocolVersion": a2aProtocolVersion}},
		"capabilities":        map[string]any{"streaming": false, "pushNotifications": false, "extendedAgentCard": false},
		"securitySchemes": map[string]any{"bearer": map[string]any{"httpAuthSecurityScheme": map[string]any{
			"description": "Gateway virtual key", "scheme": "Bearer",
		}}},
		"securityRequirements": []any{map[string]any{"schemes": map[string]any{"bearer": map[string]any{"list": []string{}}}}},
		"defaultInputModes":    []string{"text/plain"},
		"defaultOutputModes":   []string{"text/plain"},
		"skills": []any{map[string]any{
			"id": profile.ID, "name": profile.Name, "description": description,
			"inputModes": []string{"text/plain"}, "outputModes": []string{"text/plain"}, "tags": tags,
		}},
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, card)
}

func (h Handler) A2AJSONRPC(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		h.writeA2AError(w, nil, http.StatusBadRequest, -32700, "Invalid JSON payload")
		return
	}
	var request a2aRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		h.writeA2AError(w, nil, http.StatusBadRequest, -32700, "Invalid JSON payload")
		return
	}
	if request.JSONRPC != "2.0" || !validA2ARequestID(request.ID) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32600, "Request payload validation error")
		return
	}
	if r.Header.Get("A2A-Version") != a2aProtocolVersion {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32009, "Version not supported")
		return
	}
	if request.Method != "SendMessage" {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32601, "Method not found")
		return
	}
	profile, ok := h.a2aProfile(r.PathValue("agent"))
	if !ok {
		h.writeA2AError(w, request.ID, http.StatusNotFound, -32601, "Method not found")
		return
	}
	if request.Params.Tenant != profile.ID || request.Params.Message.MessageID == "" || request.Params.Message.Role != "ROLE_USER" || len(request.Params.Message.Parts) == 0 {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	if request.Params.Message.TaskID != "" {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32004, "This operation is not supported")
		return
	}
	if len(request.Params.Configuration.PushNotificationConfig) != 0 && string(request.Params.Configuration.PushNotificationConfig) != "null" {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32003, "Push notifications are not supported")
		return
	}
	for _, mode := range request.Params.Configuration.AcceptedOutputModes {
		if mode != "text/plain" {
			h.writeA2AError(w, request.ID, http.StatusBadRequest, -32005, "Content type is not supported")
			return
		}
	}
	content := make([]any, 0, len(request.Params.Message.Parts))
	for _, part := range request.Params.Message.Parts {
		if part.Text == nil || part.Raw != nil || part.URL != nil || len(part.Data) != 0 || (part.MediaType != "" && part.MediaType != "text/plain") || part.Filename != "" {
			h.writeA2AError(w, request.ID, http.StatusBadRequest, -32005, "Content type is not supported")
			return
		}
		content = append(content, map[string]any{"type": "input_text", "text": *part.Text})
	}

	capture := newA2AResponseCapture()
	responseRequest := openai.ResponseRequest{Model: profile.Model, Input: []any{map[string]any{"role": "user", "content": content}}}
	h.serveResponsesAs(capture, r, responseRequest, "a2a", func(response openai.ResponseResponse) any {
		messageID := response.ID
		if messageID == "" {
			messageID = newA2AID("msg")
		}
		contextID := request.Params.Message.ContextID
		if contextID == "" {
			contextID = newA2AID("ctx")
		}
		return a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"message": a2aMessage{
			MessageID: messageID, ContextID: contextID, Role: "ROLE_AGENT", Parts: []a2aPart{{Text: responseOutputText(response)}},
		}}}
	})
	copyA2AResponse(w, capture, request.ID)
}

func (h Handler) a2aProfile(id string) (AgentProfile, bool) {
	if h.agents == nil {
		return AgentProfile{}, false
	}
	profile, ok := h.agents.AgentProfile(id)
	return profile, ok && profile.Enabled && profile.ExecutionSupported
}

func (h Handler) writeA2AError(w http.ResponseWriter, id json.RawMessage, status, code int, message string) {
	writeJSON(w, status, a2aRPCResponse{JSONRPC: "2.0", ID: id, Error: &a2aRPCError{Code: code, Message: message}})
}

type a2aResponseCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newA2AResponseCapture() *a2aResponseCapture {
	return &a2aResponseCapture{header: make(http.Header)}
}
func (w *a2aResponseCapture) Header() http.Header { return w.header }
func (w *a2aResponseCapture) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *a2aResponseCapture) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func copyA2AResponse(w http.ResponseWriter, captured *a2aResponseCapture, id json.RawMessage) {
	for key, values := range captured.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if captured.status >= 200 && captured.status < 300 {
		w.WriteHeader(captured.status)
		_, _ = w.Write(captured.body.Bytes())
		return
	}
	var failure struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	message := "Internal error"
	if json.Unmarshal(captured.body.Bytes(), &failure) == nil && strings.TrimSpace(failure.Error.Message) != "" {
		message = failure.Error.Message
	}
	code := -32603
	if captured.status == http.StatusBadRequest {
		code = -32602
	}
	writeJSON(w, captured.status, a2aRPCResponse{JSONRPC: "2.0", ID: id, Error: &a2aRPCError{Code: code, Message: message}})
}

func responseOutputText(response openai.ResponseResponse) *string {
	text := response.OutputText
	if text == "" {
		var parts []string
		for _, item := range response.Output {
			if item.Type != "message" {
				continue
			}
			for _, content := range item.Content {
				if content.Type == "output_text" && content.Text != "" {
					parts = append(parts, content.Text)
				}
			}
		}
		text = strings.Join(parts, "")
	}
	return &text
}

func newA2AID(prefix string) string {
	return prefix + "_" + newExecutionID()
}

func validA2ARequestID(id json.RawMessage) bool {
	value := strings.TrimSpace(string(id))
	if value == "" || value == "null" || len(value) > 1024 || !json.Valid(id) {
		return false
	}
	if value[0] == '"' {
		var text string
		return json.Unmarshal(id, &text) == nil
	}
	return value[0] == '-' || value[0] >= '0' && value[0] <= '9'
}
