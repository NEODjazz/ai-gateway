package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

const a2aProtocolVersion = "1.0"

var a2aInputModes = []string{"text/plain", "image/jpeg", "image/png", "image/gif", "image/webp"}

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
		Tenant               string     `json:"tenant"`
		ID                   string     `json:"id"`
		ContextID            string     `json:"contextId,omitempty"`
		Status               string     `json:"status,omitempty"`
		PageSize             int        `json:"pageSize,omitempty"`
		PageToken            string     `json:"pageToken,omitempty"`
		HistoryLength        *int       `json:"historyLength,omitempty"`
		IncludeArtifacts     bool       `json:"includeArtifacts,omitempty"`
		StatusTimestampAfter string     `json:"statusTimestampAfter,omitempty"`
		Message              a2aMessage `json:"message"`
		Configuration        struct {
			AcceptedOutputModes    []string        `json:"acceptedOutputModes,omitempty"`
			ReturnImmediately      *bool           `json:"returnImmediately,omitempty"`
			PushNotificationConfig json.RawMessage `json:"pushNotificationConfig,omitempty"`
		} `json:"configuration,omitempty"`
	} `json:"params"`
}

type A2ATaskRuntimeConfig struct {
	OwnerQuota int
	TTL        time.Duration
}

type a2aTaskStatus struct {
	State     string `json:"state"`
	Timestamp string `json:"timestamp,omitempty"`
}

type a2aArtifact struct {
	ArtifactID string    `json:"artifactId"`
	Parts      []a2aPart `json:"parts"`
}

type a2aTask struct {
	ID        string        `json:"id"`
	ContextID string        `json:"contextId"`
	Status    a2aTaskStatus `json:"status"`
	Artifacts []a2aArtifact `json:"artifacts,omitempty"`
	History   []a2aMessage  `json:"history,omitempty"`
}

type a2aStoredTask struct {
	Task                 a2aTask `json:"task"`
	BackgroundResponseID string  `json:"backgroundResponseId,omitempty"`
	Streaming            bool    `json:"streaming,omitempty"`
}

func (h Handler) WithA2ATaskStore(store a2astate.Store, config A2ATaskRuntimeConfig) Handler {
	h.a2aTasks = store
	h.a2aTaskConfig = config
	return h
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
	card := h.a2aAgentCard(r, profile)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, card)
}

func (h Handler) a2aAgentCard(r *http.Request, profile AgentProfile) map[string]any {
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
	return map[string]any{
		"name": profile.Name, "description": description, "version": "1.0.0",
		"supportedInterfaces": []any{map[string]any{"url": endpoint, "protocolBinding": "JSONRPC", "tenant": profile.ID, "protocolVersion": a2aProtocolVersion}},
		"capabilities": map[string]any{
			"streaming":         h.a2aTasks != nil && h.a2aTaskConfig.OwnerQuota > 0 && h.a2aTaskConfig.TTL > 0,
			"pushNotifications": false, "extendedAgentCard": true,
		},
		"securitySchemes": map[string]any{"bearer": map[string]any{"httpAuthSecurityScheme": map[string]any{
			"description": "Gateway virtual key", "scheme": "Bearer",
		}}},
		"securityRequirements": []any{map[string]any{"schemes": map[string]any{"bearer": map[string]any{"list": []string{}}}}},
		"defaultInputModes":    a2aInputModes,
		"defaultOutputModes":   []string{"text/plain"},
		"skills": []any{map[string]any{
			"id": profile.ID, "name": profile.Name, "description": description,
			"inputModes": a2aInputModes, "outputModes": []string{"text/plain"}, "tags": tags,
		}},
	}
}

func (h Handler) A2AJSONRPC(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		h.writeA2AError(w, nil, http.StatusBadRequest, -32700, "Invalid JSON payload")
		return
	}
	var request a2aRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
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
	profile, ok := h.a2aProfile(r.PathValue("agent"))
	if !ok {
		h.writeA2AError(w, request.ID, http.StatusNotFound, -32601, "Method not found")
		return
	}
	if request.Method != "SendMessage" && request.Method != "SendStreamingMessage" && request.Method != "GetTask" && request.Method != "ListTasks" && request.Method != "CancelTask" && request.Method != "GetExtendedAgentCard" {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32601, "Method not found")
		return
	}
	if request.Params.Tenant != profile.ID {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	switch request.Method {
	case "SendMessage":
		h.sendA2AMessage(w, r, request, profile, false)
	case "SendStreamingMessage":
		h.sendA2AMessage(w, r, request, profile, true)
	case "GetTask":
		h.getA2ATask(w, r, request, profile)
	case "ListTasks":
		h.listA2ATasks(w, r, request, profile)
	case "CancelTask":
		h.cancelA2ATask(w, r, request, profile)
	case "GetExtendedAgentCard":
		h.getA2AExtendedAgentCard(w, r, request, profile)
	default:
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32601, "Method not found")
	}
}

func (h Handler) getA2AExtendedAgentCard(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile) {
	capture := newA2AResponseCapture()
	reqCtx, ok := h.authorizeOwnedStorageOperation(capture, r, "a2a")
	if !ok {
		copyA2AResponse(w, capture, request.ID)
		return
	}
	copyA2AHeaders(w, capture.header)
	if !h.authorizeA2ATaskModel(w, request.ID, reqCtx, profile.Model) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: h.a2aAgentCard(r, profile)})
}

func (h Handler) sendA2AMessage(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile, stream bool) {
	if request.Params.Message.MessageID == "" || len(request.Params.Message.MessageID) > 128 || request.Params.Message.Role != "ROLE_USER" || len(request.Params.Message.Parts) == 0 || len(request.Params.Message.Parts) > 1024 ||
		(request.Params.Message.ContextID != "" && !validFileToken(request.Params.Message.ContextID, 128)) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	continuation := request.Params.Message.TaskID != ""
	if continuation && !validFileToken(request.Params.Message.TaskID, 128) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	returnImmediately := request.Params.Configuration.ReturnImmediately != nil && *request.Params.Configuration.ReturnImmediately
	if stream && returnImmediately {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "returnImmediately is not valid for streaming")
		return
	}
	if stream && (h.a2aTasks == nil || h.a2aTaskConfig.OwnerQuota < 1 || h.a2aTaskConfig.TTL <= 0) {
		h.writeA2AError(w, request.ID, http.StatusNotImplemented, -32004, "Streaming is not supported")
		return
	}
	if returnImmediately {
		if h.a2aTasks == nil || h.a2aTaskConfig.OwnerQuota < 1 || h.a2aTaskConfig.TTL <= 0 {
			h.writeA2AError(w, request.ID, http.StatusNotImplemented, -32004, "Asynchronous task execution is not supported")
			return
		}
		if _, ok := h.provider.(provider.ResponseResourceProvider); !ok {
			h.writeA2AError(w, request.ID, http.StatusNotImplemented, -32004, "Asynchronous task execution is not supported")
			return
		}
		if _, ok := h.provider.(provider.ResponseCancellationProvider); !ok {
			h.writeA2AError(w, request.ID, http.StatusNotImplemented, -32004, "Asynchronous task execution is not supported")
			return
		}
	}
	if h.a2aTasks != nil && (h.a2aTaskConfig.OwnerQuota < 1 || h.a2aTaskConfig.TTL <= 0) {
		h.writeA2AError(w, request.ID, http.StatusServiceUnavailable, -32603, "Task storage is unavailable")
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
	var stored a2astate.Task
	var existing a2aTask
	if continuation {
		if h.a2aTasks == nil || h.a2aTaskConfig.OwnerQuota < 1 || h.a2aTaskConfig.TTL <= 0 {
			h.writeA2AError(w, request.ID, http.StatusNotImplemented, -32004, "Task continuation is not supported")
			return
		}
		identity, ok := h.authenticateA2AContinuation(w, r, request.ID)
		if !ok {
			return
		}
		var err error
		stored, err = h.a2aTasks.GetA2ATask(r.Context(), fileOwnerKey(identity), profile.ID, request.Params.Message.TaskID)
		if err != nil {
			h.writeA2ATaskStoreError(w, request.ID, err)
			return
		}
		if !h.authorizeA2ATaskModel(w, request.ID, identity, stored.Model) {
			return
		}
		existing, err = decodeA2ATask(stored.Payload)
		if err != nil || existing.ID != stored.ID || existing.ContextID != stored.ContextID || existing.Status.State != stored.State {
			if err == nil {
				err = a2astate.ErrInvalid
			}
			h.writeA2ATaskStoreError(w, request.ID, err)
			return
		}
		if existing.Status.State != "TASK_STATE_COMPLETED" {
			h.writeA2AError(w, request.ID, http.StatusConflict, -32602, "Task cannot be continued in its current state")
			return
		}
		if request.Params.Message.ContextID != "" && request.Params.Message.ContextID != existing.ContextID || a2aHistoryContainsMessage(existing.History, request.Params.Message.MessageID) {
			h.writeA2AError(w, request.ID, http.StatusConflict, -32602, "Task continuation conflicts with stored history")
			return
		}
		request.Params.Message.ContextID = existing.ContextID
	}
	content := make([]any, 0, len(request.Params.Message.Parts))
	for _, part := range request.Params.Message.Parts {
		inputPart, ok := a2aInputPart(part)
		if !ok {
			h.writeA2AError(w, request.ID, http.StatusBadRequest, -32005, "Content type is not supported")
			return
		}
		content = append(content, inputPart)
	}

	capture := newA2AResponseCapture()
	model := profile.Model
	input := []any{map[string]any{"role": "user", "content": content}}
	if continuation {
		model = stored.Model
		input = a2aResponseInput(existing.History, content)
	}
	responseRequest := openai.ResponseRequest{Model: model, Input: input}
	if stream {
		responseRequest.Stream = true
		transformer := newA2AStreamTransformer(h, r.Context(), request, profile, stored, existing, continuation)
		streamWriter := newA2AStreamingResponseWriter(w)
		h.serveResponsesAs(streamWriter, r, responseRequest, "a2a", nil, transformer.Transform, transformer.Finalize, true)
		streamWriter.Finish(request.ID)
		return
	}
	if returnImmediately {
		store := true
		responseRequest.Store = &store
		responseRequest.Background = true
	}
	var storageErr error
	h.serveResponsesAs(capture, r, responseRequest, "a2a", func(response openai.ResponseResponse, reqCtx modules.RequestContext) any {
		if returnImmediately {
			task := newPendingA2ATask(request, existing, continuation, response.Status)
			backgroundResponseID := response.ID
			if response.Status != "queued" && response.Status != "in_progress" {
				task = materializeA2ABackgroundTask(task, response)
				backgroundResponseID = ""
			}
			payload, err := encodeA2AStoredTask(task, backgroundResponseID)
			if err == nil && continuation {
				_, err = h.a2aTasks.UpdateA2ATask(r.Context(), a2astate.Task{
					ID: task.ID, OwnerKey: fileOwnerKey(reqCtx), AgentID: profile.ID, Model: stored.Model, ContextID: task.ContextID,
					State: task.Status.State, Payload: payload,
				}, stored.UpdatedAt, h.a2aTaskConfig.TTL)
			} else if err == nil {
				_, err = h.a2aTasks.CreateA2ATask(r.Context(), a2astate.Task{
					ID: task.ID, OwnerKey: fileOwnerKey(reqCtx), AgentID: profile.ID, Model: profile.Model, ContextID: task.ContextID,
					State: task.Status.State, Payload: payload,
				}, h.a2aTaskConfig.OwnerQuota, h.a2aTaskConfig.TTL)
			}
			storageErr = err
			if err != nil {
				if canceler, ok := h.provider.(provider.ResponseCancellationProvider); ok {
					_, _ = canceler.CancelResponse(r.Context(), reqCtx, response.ID)
				}
			}
			return a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"task": task}}
		}
		messageID := response.ID
		if messageID == "" || len(messageID) > 128 || continuation && a2aHistoryContainsMessage(existing.History, messageID) {
			messageID = newA2AID("msg")
		}
		contextID := request.Params.Message.ContextID
		if contextID == "" {
			contextID = newA2AID("ctx")
		}
		agentMessage := a2aMessage{MessageID: messageID, ContextID: contextID, Role: "ROLE_AGENT", Parts: []a2aPart{{Text: responseOutputText(response)}}}
		if h.a2aTasks == nil {
			return a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"message": agentMessage}}
		}
		taskID := request.Params.Message.TaskID
		if taskID == "" {
			taskID = newA2AID("task")
		}
		request.Params.Message.ContextID = contextID
		request.Params.Message.TaskID = taskID
		agentMessage.TaskID = taskID
		now := time.Now().UTC()
		task := a2aTask{ID: taskID, ContextID: contextID, Status: a2aTaskStatus{State: "TASK_STATE_COMPLETED", Timestamp: now.Format(time.RFC3339Nano)}}
		if continuation {
			task.Artifacts = append(append([]a2aArtifact(nil), existing.Artifacts...), a2aArtifact{ArtifactID: newA2AID("artifact"), Parts: agentMessage.Parts})
			task.History = append(append(append([]a2aMessage(nil), existing.History...), request.Params.Message), agentMessage)
		} else {
			task.Artifacts = []a2aArtifact{{ArtifactID: newA2AID("artifact"), Parts: agentMessage.Parts}}
			task.History = []a2aMessage{request.Params.Message, agentMessage}
		}
		payload, err := json.Marshal(task)
		if err == nil && len(payload) > a2astate.MaxPayloadBytes {
			err = a2astate.ErrInvalid
		}
		if err == nil && continuation {
			_, err = h.a2aTasks.UpdateA2ATask(r.Context(), a2astate.Task{
				ID: taskID, OwnerKey: fileOwnerKey(reqCtx), AgentID: profile.ID, Model: stored.Model, ContextID: contextID,
				State: task.Status.State, Payload: payload,
			}, stored.UpdatedAt, h.a2aTaskConfig.TTL)
		} else if err == nil {
			_, err = h.a2aTasks.CreateA2ATask(r.Context(), a2astate.Task{
				ID: taskID, OwnerKey: fileOwnerKey(reqCtx), AgentID: profile.ID, Model: profile.Model, ContextID: contextID,
				State: task.Status.State, Payload: payload,
			}, h.a2aTaskConfig.OwnerQuota, h.a2aTaskConfig.TTL)
		}
		storageErr = err
		return a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"task": task}}
	}, nil, nil, false)
	if storageErr != nil {
		copyA2AHeaders(w, capture.header)
		status := http.StatusServiceUnavailable
		message := "Task storage is unavailable"
		if errors.Is(storageErr, a2astate.ErrQuotaExceeded) {
			status = http.StatusTooManyRequests
			message = "Task quota exceeded"
		} else if errors.Is(storageErr, a2astate.ErrInvalid) {
			status = http.StatusBadGateway
			message = "Task result is too large"
		} else if errors.Is(storageErr, a2astate.ErrConflict) {
			status = http.StatusConflict
			message = "Task changed concurrently"
		} else if errors.Is(storageErr, a2astate.ErrNotFound) {
			status = http.StatusNotFound
			message = "Task not found"
		}
		h.writeA2AError(w, request.ID, status, -32603, message)
		return
	}
	copyA2AResponse(w, capture, request.ID)
}

func newPendingA2ATask(request a2aRequest, existing a2aTask, continuation bool, responseStatus string) a2aTask {
	contextID := request.Params.Message.ContextID
	if contextID == "" {
		contextID = newA2AID("ctx")
	}
	taskID := request.Params.Message.TaskID
	if taskID == "" {
		taskID = newA2AID("task")
	}
	request.Params.Message.ContextID = contextID
	request.Params.Message.TaskID = taskID
	state := "TASK_STATE_SUBMITTED"
	if responseStatus == "in_progress" {
		state = "TASK_STATE_WORKING"
	}
	task := a2aTask{ID: taskID, ContextID: contextID, Status: a2aTaskStatus{State: state, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}}
	if continuation {
		task.Artifacts = append([]a2aArtifact(nil), existing.Artifacts...)
		task.History = append(append([]a2aMessage(nil), existing.History...), request.Params.Message)
	} else {
		task.History = []a2aMessage{request.Params.Message}
	}
	return task
}

func (h Handler) authenticateA2AContinuation(w http.ResponseWriter, r *http.Request, rpcID json.RawMessage) (modules.RequestContext, bool) {
	req := modules.RequestContext{APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: newExecutionID(), SessionID: sessionID(r), Metadata: map[string]string{"gateway.api_type": "a2a"}}
	if err := h.pipeline.RunAuthentication(r.Context(), &req); err != nil {
		status, message := http.StatusBadGateway, "Authentication failed"
		if errors.Is(err, modules.ErrUnauthorized) {
			status, message = http.StatusUnauthorized, "Invalid API key"
		}
		h.writeA2AError(w, rpcID, status, -32603, message)
		return modules.RequestContext{}, false
	}
	req.APIKey = ""
	capture := newA2AResponseCapture()
	if !h.prepareAccessGroups(capture, &req) {
		copyA2AResponse(w, capture, rpcID)
		return modules.RequestContext{}, false
	}
	return req, true
}

func a2aHistoryContainsMessage(history []a2aMessage, id string) bool {
	for _, message := range history {
		if message.MessageID == id {
			return true
		}
	}
	return false
}

func a2aResponseInput(history []a2aMessage, latest []any) []any {
	input := make([]any, 0, len(history)+1)
	for _, message := range history {
		content := make([]any, 0, len(message.Parts))
		for _, part := range message.Parts {
			inputPart, ok := a2aInputPart(part)
			if !ok {
				continue
			}
			if message.Role == "ROLE_AGENT" {
				inputPart["type"] = "output_text"
			}
			content = append(content, inputPart)
		}
		role := "user"
		if message.Role == "ROLE_AGENT" {
			role = "assistant"
		}
		input = append(input, map[string]any{"role": role, "content": content})
	}
	return append(input, map[string]any{"role": "user", "content": latest})
}

func a2aInputPart(part a2aPart) (map[string]any, bool) {
	if part.Text != nil && part.Raw == nil && part.URL == nil && len(part.Data) == 0 && (part.MediaType == "" || part.MediaType == "text/plain") && part.Filename == "" {
		return map[string]any{"type": "input_text", "text": *part.Text}, true
	}
	if part.Text != nil || part.Raw == nil || part.URL != nil || len(part.Data) != 0 || part.Filename != "" {
		return nil, false
	}
	dataURL := "data:" + part.MediaType + ";base64," + *part.Raw
	if _, err := openai.ParseDataImageURL(dataURL); err != nil {
		return nil, false
	}
	return map[string]any{"type": "input_image", "image_url": dataURL}, true
}

func (h Handler) getA2ATask(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile) {
	if !validFileToken(request.Params.ID, 128) || !validA2AHistoryLength(request.Params.HistoryLength) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	reqCtx, ok := h.authorizeA2ATaskOperation(w, r, request.ID)
	if !ok {
		return
	}
	task, err := h.a2aTasks.GetA2ATask(r.Context(), fileOwnerKey(reqCtx), profile.ID, request.Params.ID)
	if err != nil {
		h.writeA2ATaskStoreError(w, request.ID, err)
		return
	}
	if !h.authorizeA2ATaskModel(w, request.ID, reqCtx, task.Model) {
		return
	}
	decoded, backgroundResponseID, err := decodeA2AStoredTask(task.Payload)
	if err != nil || decoded.ID != task.ID || decoded.ContextID != task.ContextID || decoded.Status.State != task.State {
		if err == nil {
			err = a2astate.ErrInvalid
		}
		h.writeA2ATaskStoreError(w, request.ID, err)
		return
	}
	if backgroundResponseID != "" && a2aTaskPending(decoded.Status.State) {
		resourceProvider := h.provider.(provider.ResponseResourceProvider)
		responseModel, resolveErr := resourceProvider.ResolveResponseResource(r.Context(), reqCtx, backgroundResponseID)
		if resolveErr != nil || responseModel != task.Model {
			h.writeA2AError(w, request.ID, http.StatusBadGateway, -32603, "Task status is temporarily unavailable")
			return
		}
		response, retrieveErr := resourceProvider.RetrieveResponse(r.Context(), reqCtx, backgroundResponseID)
		if retrieveErr != nil {
			h.writeA2AError(w, request.ID, http.StatusBadGateway, -32603, "Task status is temporarily unavailable")
			return
		}
		updated := materializeA2ABackgroundTask(decoded, response)
		if updated.Status.State != decoded.Status.State || a2aTaskTerminal(updated.Status.State) {
			persistedResponseID := backgroundResponseID
			if a2aTaskTerminal(updated.Status.State) {
				persistedResponseID = ""
			}
			payload, encodeErr := encodeA2AStoredTask(updated, persistedResponseID)
			if encodeErr != nil {
				h.writeA2ATaskStoreError(w, request.ID, encodeErr)
				return
			}
			_, updateErr := h.a2aTasks.UpdateA2ATask(r.Context(), a2astate.Task{
				ID: task.ID, OwnerKey: task.OwnerKey, AgentID: task.AgentID, Model: task.Model, ContextID: task.ContextID,
				State: updated.Status.State, Payload: payload,
			}, task.UpdatedAt, h.a2aTaskConfig.TTL)
			if updateErr != nil {
				h.writeA2ATaskStoreError(w, request.ID, updateErr)
				return
			}
			decoded = updated
		}
	}
	trimA2AHistory(&decoded, request.Params.HistoryLength)
	writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: decoded})
}

func (h Handler) listA2ATasks(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile) {
	pageSize := request.Params.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	if pageSize < 1 || pageSize > 100 || !validA2AHistoryLength(request.Params.HistoryLength) ||
		(request.Params.PageToken != "" && !validFileToken(request.Params.PageToken, 128)) ||
		(request.Params.ContextID != "" && !validFileToken(request.Params.ContextID, 128)) ||
		(request.Params.Status != "" && !validA2ATaskState(request.Params.Status)) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	var updatedAfter *time.Time
	if request.Params.StatusTimestampAfter != "" {
		parsed, err := time.Parse(time.RFC3339, request.Params.StatusTimestampAfter)
		if err != nil {
			h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
			return
		}
		updatedAfter = &parsed
	}
	reqCtx, ok := h.authorizeA2ATaskOperation(w, r, request.ID)
	if !ok {
		return
	}
	if !h.authorizeA2ATaskModel(w, request.ID, reqCtx, profile.Model) {
		return
	}
	tasks, next, total, err := h.a2aTasks.ListA2ATasks(r.Context(), fileOwnerKey(reqCtx), profile.ID, a2astate.ListOptions{
		Model: profile.Model, Limit: pageSize, After: request.Params.PageToken, ContextID: request.Params.ContextID,
		State: request.Params.Status, UpdatedAfter: updatedAfter,
	})
	if err != nil {
		h.writeA2ATaskStoreError(w, request.ID, err)
		return
	}
	result := make([]a2aTask, 0, len(tasks))
	for _, stored := range tasks {
		decoded, decodeErr := decodeA2ATask(stored.Payload)
		if decodeErr != nil || decoded.ID != stored.ID || decoded.ContextID != stored.ContextID || decoded.Status.State != stored.State {
			if decodeErr == nil {
				decodeErr = a2astate.ErrInvalid
			}
			h.writeA2ATaskStoreError(w, request.ID, decodeErr)
			return
		}
		trimA2AHistory(&decoded, request.Params.HistoryLength)
		if !request.Params.IncludeArtifacts {
			decoded.Artifacts = nil
		}
		result = append(result, decoded)
	}
	writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{
		"tasks": result, "nextPageToken": next, "pageSize": pageSize, "totalSize": total,
	}})
}

func (h Handler) cancelA2ATask(w http.ResponseWriter, r *http.Request, request a2aRequest, profile AgentProfile) {
	if !validFileToken(request.Params.ID, 128) {
		h.writeA2AError(w, request.ID, http.StatusBadRequest, -32602, "Invalid parameters")
		return
	}
	reqCtx, ok := h.authorizeA2ATaskOperation(w, r, request.ID)
	if !ok {
		return
	}
	task, err := h.a2aTasks.GetA2ATask(r.Context(), fileOwnerKey(reqCtx), profile.ID, request.Params.ID)
	if err != nil {
		h.writeA2ATaskStoreError(w, request.ID, err)
		return
	}
	if !h.authorizeA2ATaskModel(w, request.ID, reqCtx, task.Model) {
		return
	}
	decoded, backgroundResponseID, err := decodeA2AStoredTask(task.Payload)
	if err != nil || decoded.ID != task.ID || decoded.ContextID != task.ContextID || decoded.Status.State != task.State {
		if err == nil {
			err = a2astate.ErrInvalid
		}
		h.writeA2ATaskStoreError(w, request.ID, err)
		return
	}
	if backgroundResponseID != "" && a2aTaskPending(decoded.Status.State) {
		canceler, ok := h.provider.(provider.ResponseCancellationProvider)
		if !ok {
			h.writeA2AError(w, request.ID, http.StatusNotImplemented, -32004, "Task cancellation is not supported")
			return
		}
		responseModel, resolveErr := canceler.ResolveResponseResource(r.Context(), reqCtx, backgroundResponseID)
		if resolveErr != nil || responseModel != task.Model {
			h.writeA2AError(w, request.ID, http.StatusBadGateway, -32603, "Task cancellation failed")
			return
		}
		response, cancelErr := canceler.CancelResponse(r.Context(), reqCtx, backgroundResponseID)
		if cancelErr != nil {
			h.writeA2AError(w, request.ID, http.StatusBadGateway, -32603, "Task cancellation failed")
			return
		}
		updated := materializeA2ABackgroundTask(decoded, response)
		if a2aTaskPending(updated.Status.State) {
			updated.Status = a2aTaskStatus{State: "TASK_STATE_CANCELED", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
		}
		payload, encodeErr := encodeA2AStoredTask(updated, "")
		if encodeErr != nil {
			h.writeA2ATaskStoreError(w, request.ID, encodeErr)
			return
		}
		if _, updateErr := h.a2aTasks.UpdateA2ATask(r.Context(), a2astate.Task{
			ID: task.ID, OwnerKey: task.OwnerKey, AgentID: task.AgentID, Model: task.Model, ContextID: task.ContextID,
			State: updated.Status.State, Payload: payload,
		}, task.UpdatedAt, h.a2aTaskConfig.TTL); updateErr != nil {
			h.writeA2ATaskStoreError(w, request.ID, updateErr)
			return
		}
		writeJSON(w, http.StatusOK, a2aRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: updated})
		return
	}
	h.writeA2AError(w, request.ID, http.StatusBadRequest, -32002, "Task cannot be canceled")
}

func (h Handler) authorizeA2ATaskOperation(w http.ResponseWriter, r *http.Request, rpcID json.RawMessage) (modules.RequestContext, bool) {
	if h.a2aTasks == nil || h.a2aTaskConfig.OwnerQuota < 1 || h.a2aTaskConfig.TTL <= 0 {
		h.writeA2AError(w, rpcID, http.StatusNotImplemented, -32004, "Task lifecycle is not supported")
		return modules.RequestContext{}, false
	}
	capture := newA2AResponseCapture()
	reqCtx, ok := h.authorizeOwnedStorageOperation(capture, r, "a2a")
	if !ok {
		copyA2AResponse(w, capture, rpcID)
		return modules.RequestContext{}, false
	}
	copyA2AHeaders(w, capture.header)
	return reqCtx, true
}

func (h Handler) authorizeA2ATaskModel(w http.ResponseWriter, rpcID json.RawMessage, reqCtx modules.RequestContext, model string) bool {
	if !modelAllowed(model, reqCtx.AllowedModels) || reqCtx.AccessGroupsEvaluated && !modelAllowed(model, reqCtx.AccessGroupModels) {
		h.writeA2AError(w, rpcID, http.StatusForbidden, -32603, "Task access is not allowed")
		return false
	}
	if h.access != nil {
		if allowed, _ := h.access.TagModelAllowed(reqCtx.Tags, model); !allowed {
			h.writeA2AError(w, rpcID, http.StatusForbidden, -32603, "Task access is not allowed")
			return false
		}
	}
	return true
}

func (h Handler) writeA2ATaskStoreError(w http.ResponseWriter, id json.RawMessage, err error) {
	switch {
	case errors.Is(err, a2astate.ErrNotFound):
		h.writeA2AError(w, id, http.StatusNotFound, -32001, "Task not found")
	case errors.Is(err, a2astate.ErrInvalid):
		h.writeA2AError(w, id, http.StatusInternalServerError, -32603, "Stored task is invalid")
	default:
		h.writeA2AError(w, id, http.StatusServiceUnavailable, -32603, "Task storage is unavailable")
	}
}

func decodeA2ATask(payload []byte) (a2aTask, error) {
	task, _, err := decodeA2AStoredTask(payload)
	return task, err
}

func decodeA2AStoredTask(payload []byte) (a2aTask, string, error) {
	if len(payload) == 0 || len(payload) > a2astate.MaxPayloadBytes {
		return a2aTask{}, "", a2astate.ErrInvalid
	}
	var task a2aTask
	backgroundResponseID := ""
	var stored a2aStoredTask
	if json.Unmarshal(payload, &stored) == nil && stored.Task.ID != "" {
		task = stored.Task
		backgroundResponseID = stored.BackgroundResponseID
	} else if json.Unmarshal(payload, &task) != nil {
		return a2aTask{}, "", a2astate.ErrInvalid
	}
	if !validFileToken(task.ID, 128) || !validFileToken(task.ContextID, 128) || !validA2ATaskState(task.Status.State) || !validStoredA2ATask(task) {
		return a2aTask{}, "", a2astate.ErrInvalid
	}
	pending := a2aTaskPending(task.Status.State)
	invalidBackground := backgroundResponseID != "" && (!pending || stored.Streaming || !validLifecycleToken(backgroundResponseID))
	missingExecution := backgroundResponseID == "" && pending && !stored.Streaming
	invalidStreaming := stored.Streaming && !pending
	if invalidBackground || missingExecution || invalidStreaming {
		return a2aTask{}, "", a2astate.ErrInvalid
	}
	return task, backgroundResponseID, nil
}

func encodeA2AStreamingTask(task a2aTask) ([]byte, error) {
	if !validStoredA2ATask(task) || !a2aTaskPending(task.Status.State) {
		return nil, a2astate.ErrInvalid
	}
	payload, err := json.Marshal(a2aStoredTask{Task: task, Streaming: true})
	if err != nil || len(payload) > a2astate.MaxPayloadBytes {
		return nil, a2astate.ErrInvalid
	}
	return payload, nil
}

func encodeA2AStoredTask(task a2aTask, backgroundResponseID string) ([]byte, error) {
	if !validStoredA2ATask(task) || backgroundResponseID != "" && !validLifecycleToken(backgroundResponseID) {
		return nil, a2astate.ErrInvalid
	}
	payload, err := json.Marshal(a2aStoredTask{Task: task, BackgroundResponseID: backgroundResponseID})
	if err != nil || len(payload) > a2astate.MaxPayloadBytes {
		return nil, a2astate.ErrInvalid
	}
	return payload, nil
}

func validStoredA2ATask(task a2aTask) bool {
	if task.Status.Timestamp != "" {
		if _, err := time.Parse(time.RFC3339Nano, task.Status.Timestamp); err != nil {
			return false
		}
	}
	switch {
	case a2aTaskPending(task.Status.State):
		if len(task.History) < 1 || len(task.Artifacts) > 0 {
			return false
		}
	case task.Status.State == "TASK_STATE_COMPLETED":
		if len(task.History) < 2 || len(task.Artifacts) < 1 {
			return false
		}
	case len(task.History) < 1:
		return false
	}
	for _, message := range task.History {
		if message.MessageID == "" || len(message.MessageID) > 128 || message.ContextID != task.ContextID || message.TaskID != task.ID || (message.Role != "ROLE_USER" && message.Role != "ROLE_AGENT") || len(message.Parts) == 0 || len(message.Parts) > 1024 {
			return false
		}
		for _, part := range message.Parts {
			_, valid := a2aInputPart(part)
			if !valid || message.Role == "ROLE_AGENT" && part.Text == nil {
				return false
			}
		}
	}
	for _, artifact := range task.Artifacts {
		if !validFileToken(artifact.ArtifactID, 128) || len(artifact.Parts) == 0 || len(artifact.Parts) > 1024 {
			return false
		}
		for _, part := range artifact.Parts {
			if part.Text == nil || part.Raw != nil || part.URL != nil || len(part.Data) != 0 || (part.MediaType != "" && part.MediaType != "text/plain") || part.Filename != "" {
				return false
			}
		}
	}
	return true
}

func a2aTaskPending(state string) bool {
	return state == "TASK_STATE_SUBMITTED" || state == "TASK_STATE_WORKING"
}

func a2aTaskTerminal(state string) bool {
	return state == "TASK_STATE_COMPLETED" || state == "TASK_STATE_FAILED" || state == "TASK_STATE_CANCELED"
}

func materializeA2ABackgroundTask(task a2aTask, response openai.ResponseResponse) a2aTask {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch response.Status {
	case "queued":
		task.Status = a2aTaskStatus{State: "TASK_STATE_SUBMITTED", Timestamp: now}
	case "in_progress":
		task.Status = a2aTaskStatus{State: "TASK_STATE_WORKING", Timestamp: now}
	case "completed", "":
		messageID := response.ID
		if messageID == "" || len(messageID) > 128 || a2aHistoryContainsMessage(task.History, messageID) {
			messageID = newA2AID("msg")
		}
		message := a2aMessage{MessageID: messageID, ContextID: task.ContextID, TaskID: task.ID, Role: "ROLE_AGENT", Parts: []a2aPart{{Text: responseOutputText(response)}}}
		task.History = append(task.History, message)
		task.Artifacts = append(task.Artifacts, a2aArtifact{ArtifactID: newA2AID("artifact"), Parts: message.Parts})
		task.Status = a2aTaskStatus{State: "TASK_STATE_COMPLETED", Timestamp: now}
	case "cancelled", "canceled":
		task.Status = a2aTaskStatus{State: "TASK_STATE_CANCELED", Timestamp: now}
	default:
		task.Status = a2aTaskStatus{State: "TASK_STATE_FAILED", Timestamp: now}
	}
	return task
}

func validA2AHistoryLength(length *int) bool {
	return length == nil || *length >= 0 && *length <= 100
}

func validA2ATaskState(state string) bool {
	switch state {
	case "TASK_STATE_UNSPECIFIED", "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING", "TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_REJECTED", "TASK_STATE_AUTH_REQUIRED":
		return true
	default:
		return false
	}
}

func trimA2AHistory(task *a2aTask, length *int) {
	if length == nil || *length >= len(task.History) {
		return
	}
	if *length == 0 {
		task.History = nil
		return
	}
	task.History = append([]a2aMessage(nil), task.History[len(task.History)-*length:]...)
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

type a2aStreamingResponseWriter struct {
	target      http.ResponseWriter
	capture     *a2aResponseCapture
	passthrough bool
}

func newA2AStreamingResponseWriter(target http.ResponseWriter) *a2aStreamingResponseWriter {
	return &a2aStreamingResponseWriter{target: target, capture: newA2AResponseCapture()}
}

func (w *a2aStreamingResponseWriter) Header() http.Header { return w.capture.Header() }

func (w *a2aStreamingResponseWriter) WriteHeader(status int) {
	if w.capture.status != 0 {
		return
	}
	w.capture.status = status
	if status >= 200 && status < 300 {
		copyA2AHeaders(w.target, w.capture.header)
		w.target.WriteHeader(status)
		w.passthrough = true
	}
}

func (w *a2aStreamingResponseWriter) Write(payload []byte) (int, error) {
	if w.capture.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.passthrough {
		return w.target.Write(payload)
	}
	return w.capture.body.Write(payload)
}

func (w *a2aStreamingResponseWriter) Flush() {
	if w.passthrough {
		if flusher, ok := w.target.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (w *a2aStreamingResponseWriter) Finish(id json.RawMessage) {
	if w.passthrough {
		return
	}
	if w.capture.status == 0 {
		w.capture.status = http.StatusInternalServerError
	}
	copyA2AResponse(w.target, w.capture, id)
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
	copyA2AHeaders(w, captured.header)
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

func copyA2AHeaders(w http.ResponseWriter, header http.Header) {
	for key, values := range header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
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
				} else if content.Type == "refusal" && content.Refusal != "" {
					parts = append(parts, content.Refusal)
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
