package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"ai-gateway-gateway/internal/assistantstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

const maxAssistantRunMessages = 1000

var errAssistantRunProviderPayload = errors.New("invalid assistant run provider payload")

type assistantRunCreateRequest struct {
	AssistantID            string                  `json:"assistant_id"`
	Model                  string                  `json:"model,omitempty"`
	Instructions           optionalAssistantString `json:"instructions,omitempty"`
	AdditionalInstructions string                  `json:"additional_instructions,omitempty"`
	Metadata               map[string]string       `json:"metadata,omitempty"`
	MaxCompletionTokens    *int                    `json:"max_completion_tokens,omitempty"`
	Temperature            *float64                `json:"temperature,omitempty"`
	TopP                   *float64                `json:"top_p,omitempty"`
}

type assistantRunSnapshot struct {
	AssistantID       string                            `json:"assistant_id"`
	Model             string                            `json:"model"`
	Instructions      string                            `json:"instructions"`
	Metadata          map[string]string                 `json:"metadata"`
	ResponseID        string                            `json:"response_id"`
	LastError         *openai.ResponseError             `json:"last_error"`
	IncompleteDetails *openai.ResponseIncompleteDetails `json:"incomplete_details"`
	RequiredAction    *assistantRunRequiredAction       `json:"required_action"`
}

type assistantRunRequiredAction struct {
	Type              string                        `json:"type"`
	SubmitToolOutputs assistantRunSubmitToolOutputs `json:"submit_tool_outputs"`
}

type assistantRunSubmitToolOutputs struct {
	ToolCalls []assistantRunToolCall `json:"tool_calls"`
}

type assistantRunToolCall struct {
	ID       string                   `json:"id"`
	Type     string                   `json:"type"`
	Function assistantRunToolFunction `json:"function"`
}

type assistantRunToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type assistantRunToolOutputRequest struct {
	ToolOutputs []assistantRunToolOutput `json:"tool_outputs"`
}

type assistantRunToolOutput struct {
	ToolCallID string `json:"tool_call_id"`
	Output     string `json:"output"`
}

func (h Handler) CreateAssistantRun(w http.ResponseWriter, r *http.Request) {
	identity, threadID, ok := h.assistantRunCollection(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	var input assistantRunCreateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if !validFileToken(input.AssistantID, 128) || input.Model != "" && len(input.Model) > 512 || input.Instructions.Set && input.Instructions.Value != nil && !validAssistantRunInstructions(*input.Instructions.Value) || !validAssistantRunInstructions(input.AdditionalInstructions) || input.MaxCompletionTokens != nil && (*input.MaxCompletionTokens < 1 || *input.MaxCompletionTokens > 1_000_000) || input.Temperature != nil && (*input.Temperature < 0 || *input.Temperature > 2) || input.TopP != nil && (*input.TopP < 0 || *input.TopP > 1) {
		writeError(w, http.StatusBadRequest, "invalid_request", "run parameters are invalid")
		return
	}
	if message := openai.ValidateMetadata(input.Metadata); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	owner := fileOwnerKey(identity)
	existing, _, err := h.assistantRuns.ListRuns(r.Context(), owner, threadID, assistantstate.RunPageOptions{Limit: 1, Order: "desc"})
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	if len(existing) != 0 && assistantstate.ActiveRunStatus(existing[0].Status) {
		writeAssistantRunError(w, assistantstate.ErrConflict)
		return
	}
	assistant, err := h.assistants.GetAssistant(r.Context(), owner, input.AssistantID)
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	var definition assistantSnapshot
	if json.Unmarshal(assistant.Snapshot, &definition) != nil {
		writeAssistantRunError(w, assistantstate.ErrUnavailable)
		return
	}
	model := definition.Model
	if input.Model != "" {
		model = input.Model
	}
	instructions := ""
	if definition.Instructions != nil {
		instructions = *definition.Instructions
	}
	if input.Instructions.Set {
		instructions = ""
		if input.Instructions.Value != nil {
			instructions = *input.Instructions.Value
		}
	}
	if input.AdditionalInstructions != "" {
		if instructions != "" {
			instructions += "\n\n"
		}
		instructions += input.AdditionalInstructions
	}
	tools, ok := assistantRunTools(w, definition.Tools)
	if !ok || !h.authorizeModel(w, identity, model) {
		return
	}
	messages, err := h.assistantRunInput(r, owner, threadID)
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	store := true
	request := openai.ResponseRequest{
		Model: model, Input: messages, Instructions: instructions, Tools: tools, Store: &store, Background: true,
		MaxOutputTokens: input.MaxCompletionTokens, Temperature: input.Temperature, TopP: input.TopP,
	}
	if len(definition.ResponseFormat) != 0 && string(definition.ResponseFormat) != "null" {
		request.Text = map[string]any{"format": json.RawMessage(definition.ResponseFormat)}
	}
	metadata := normalizedMetadata(input.Metadata)
	runID, generated := newAssistantResourceID("run_")
	if !generated {
		writeError(w, http.StatusInternalServerError, "run_id_failed", "run ID generation failed")
		return
	}
	capture := newA2AResponseCapture()
	var storageErr error
	h.serveResponsesAs(capture, r, request, "assistants", func(response openai.ResponseResponse, reqCtx modules.RequestContext) any {
		snapshot := assistantRunSnapshot{
			AssistantID: input.AssistantID, Model: model, Instructions: instructions, Metadata: metadata,
			ResponseID: response.ID, LastError: response.Error, IncompleteDetails: response.IncompleteDetails,
		}
		targetStatus := assistantRunResponseStatus(response.Status)
		action, actionErr := assistantRunAction(response)
		if actionErr != nil {
			snapshot.LastError = &openai.ResponseError{Code: "invalid_provider_payload", Message: "provider returned invalid function calls"}
			targetStatus = "failed"
		}
		if action != nil {
			snapshot.RequiredAction = action
			targetStatus = "requires_action"
		}
		payload, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil || len(payload) > assistantstate.MaxRunSnapshotBytes {
			storageErr = assistantstate.ErrInvalid
			return map[string]any{}
		}
		record, createErr := h.assistantRuns.CreateRun(r.Context(), assistantstate.RunRecord{
			ID: runID, ThreadID: threadID, OwnerKey: fileOwnerKey(reqCtx), Status: "queued", Snapshot: payload,
			RetainUntil: time.Now().Add(h.assistantConfig.RunRetention),
		}, h.assistantConfig.RunOwnerQuota)
		if createErr == nil {
			if targetStatus != "queued" {
				record, createErr = h.transitionAssistantRun(r, record, targetStatus)
			}
		}
		storageErr = createErr
		if createErr != nil {
			if canceler, supported := h.provider.(provider.ResponseCancellationProvider); supported && response.ID != "" {
				_, _ = canceler.CancelResponse(r.Context(), reqCtx, response.ID)
			}
			return map[string]any{}
		}
		value, publicErr := publicAssistantRun(record)
		storageErr = publicErr
		return value
	}, nil, nil, false)
	if storageErr != nil {
		copyResponseHeaders(w, capture.header)
		writeAssistantRunError(w, storageErr)
		return
	}
	copyResponseHeaders(w, capture.header)
	status := capture.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(capture.body.Bytes())
}

func (h Handler) ListAssistantRuns(w http.ResponseWriter, r *http.Request) {
	identity, threadID, ok := h.assistantRunCollection(w, r)
	if !ok {
		return
	}
	options, ok := assistantRunPageOptions(w, r)
	if !ok {
		return
	}
	records, next, err := h.assistantRuns.ListRuns(r.Context(), fileOwnerKey(identity), threadID, options)
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	data := make([]map[string]any, 0, len(records))
	for index, record := range records {
		if assistantstate.ActiveRunStatus(record.Status) {
			record, err = h.reconcileAssistantRun(r, identity, record)
			if err != nil {
				writeAssistantRunError(w, err)
				return
			}
			records[index] = record
		}
		value, decodeErr := publicAssistantRun(record)
		if decodeErr != nil {
			writeAssistantRunError(w, decodeErr)
			return
		}
		data = append(data, value)
	}
	result := map[string]any{"object": "list", "data": data, "has_more": next != ""}
	if len(records) > 0 {
		result["first_id"], result["last_id"] = records[0].ID, records[len(records)-1].ID
	}
	writeJSON(w, http.StatusOK, result)
}

func (h Handler) GetAssistantRun(w http.ResponseWriter, r *http.Request) {
	identity, threadID, runID, ok := h.assistantRunResource(w, r)
	if !ok {
		return
	}
	record, err := h.assistantRuns.GetRun(r.Context(), fileOwnerKey(identity), threadID, runID)
	if err == nil && assistantstate.ActiveRunStatus(record.Status) {
		record, err = h.reconcileAssistantRun(r, identity, record)
	}
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	h.writeAssistantRun(w, record)
}

func (h Handler) CancelAssistantRun(w http.ResponseWriter, r *http.Request) {
	identity, threadID, runID, ok := h.assistantRunResource(w, r)
	if !ok {
		return
	}
	record, err := h.assistantRuns.GetRun(r.Context(), fileOwnerKey(identity), threadID, runID)
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	if !assistantstate.ActiveRunStatus(record.Status) || record.Status == "cancelling" || record.Status == "requires_action" {
		writeError(w, http.StatusConflict, "run_conflict", "run cannot be cancelled in its current state")
		return
	}
	snapshot, err := decodeAssistantRun(record)
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	canceler, supported := h.provider.(provider.ResponseCancellationProvider)
	if !supported {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "run cancellation is not supported")
		return
	}
	resolved, err := canceler.ResolveResponseResource(r.Context(), identity, snapshot.ResponseID)
	if err != nil || resolved != snapshot.Model {
		writeError(w, http.StatusBadGateway, "provider_error", "run cancellation failed")
		return
	}
	response, err := canceler.CancelResponse(r.Context(), identity, snapshot.ResponseID)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	snapshot.LastError, snapshot.IncompleteDetails = response.Error, response.IncompleteDetails
	record.Snapshot, _ = json.Marshal(snapshot)
	source := record.Status
	target := "cancelling"
	if settlements, ok := h.provider.(provider.BackgroundResponseSettlementProvider); ok {
		if settled, settleErr := settlements.BackgroundResponseSettled(r.Context(), identity, snapshot.ResponseID); settleErr == nil && settled {
			target = assistantRunResponseStatus(response.Status)
		}
	}
	record.Status = target
	if target == "completed" || target == "incomplete" {
		record.Status = source
		record, err = h.transitionAssistantRun(r, record, target)
	} else {
		record, err = h.assistantRuns.TransitionRun(r.Context(), record, source, record.Revision)
	}
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	h.writeAssistantRun(w, record)
}

func (h Handler) SubmitAssistantRunToolOutputs(w http.ResponseWriter, r *http.Request) {
	identity, threadID, runID, ok := h.assistantRunResource(w, r)
	if !ok {
		return
	}
	var input assistantRunToolOutputRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if len(input.ToolOutputs) < 1 || len(input.ToolOutputs) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_request", "tool_outputs must contain between 1 and 128 items")
		return
	}
	owner := fileOwnerKey(identity)
	record, err := h.assistantRuns.GetRun(r.Context(), owner, threadID, runID)
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	snapshot, err := decodeAssistantRun(record)
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	if record.Status != "requires_action" || snapshot.RequiredAction == nil {
		writeError(w, http.StatusConflict, "run_conflict", "run is not waiting for tool outputs")
		return
	}
	expected := make(map[string]bool, len(snapshot.RequiredAction.SubmitToolOutputs.ToolCalls))
	toolNames := make([]string, 0, len(snapshot.RequiredAction.SubmitToolOutputs.ToolCalls))
	for _, call := range snapshot.RequiredAction.SubmitToolOutputs.ToolCalls {
		expected[call.ID] = true
		toolNames = append(toolNames, call.Function.Name)
	}
	if !h.authorizeTools(w, identity, toolNames, true) {
		return
	}
	seen := make(map[string]bool, len(input.ToolOutputs))
	responseInput := make([]any, 0, len(input.ToolOutputs))
	for _, output := range input.ToolOutputs {
		if !validFileToken(output.ToolCallID, 128) || !expected[output.ToolCallID] || seen[output.ToolCallID] || len(output.Output) > 1<<20 {
			writeError(w, http.StatusBadRequest, "invalid_request", "tool outputs do not match the pending calls")
			return
		}
		seen[output.ToolCallID] = true
		responseInput = append(responseInput, map[string]any{"type": "function_call_output", "call_id": output.ToolCallID, "output": output.Output})
	}
	if len(seen) != len(expected) {
		writeError(w, http.StatusBadRequest, "invalid_request", "all pending tool outputs are required")
		return
	}
	store := true
	request := openai.ResponseRequest{Model: snapshot.Model, PreviousResponse: snapshot.ResponseID, Input: responseInput, Store: &store, Background: true}
	capture := newA2AResponseCapture()
	var storageErr error
	h.serveResponsesAs(capture, r, request, "assistants", func(response openai.ResponseResponse, reqCtx modules.RequestContext) any {
		if fileOwnerKey(reqCtx) != owner {
			storageErr = assistantstate.ErrNotFound
			return map[string]any{}
		}
		snapshot.ResponseID = response.ID
		snapshot.LastError, snapshot.IncompleteDetails = response.Error, response.IncompleteDetails
		var actionErr error
		snapshot.RequiredAction, actionErr = assistantRunAction(response)
		if actionErr != nil {
			snapshot.LastError = &openai.ResponseError{Code: "invalid_provider_payload", Message: "provider returned invalid function calls"}
			snapshot.RequiredAction = nil
		}
		record.Snapshot, _ = json.Marshal(snapshot)
		target := "queued"
		if actionErr != nil {
			target = "failed"
		} else if snapshot.RequiredAction != nil {
			target = "requires_action"
		} else if response.Status != "queued" {
			target = assistantRunResponseStatus(response.Status)
		}
		source := record.Status
		record.Status = "queued"
		record, storageErr = h.assistantRuns.TransitionRun(r.Context(), record, source, record.Revision)
		if storageErr == nil && target != "queued" {
			record, storageErr = h.transitionAssistantRun(r, record, target)
		}
		if storageErr != nil {
			if canceler, supported := h.provider.(provider.ResponseCancellationProvider); supported && response.ID != "" {
				_, _ = canceler.CancelResponse(r.Context(), reqCtx, response.ID)
			}
			return map[string]any{}
		}
		value, publicErr := publicAssistantRun(record)
		storageErr = publicErr
		return value
	}, nil, nil, false)
	if storageErr != nil {
		copyResponseHeaders(w, capture.header)
		writeAssistantRunError(w, storageErr)
		return
	}
	copyResponseHeaders(w, capture.header)
	status := capture.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(capture.body.Bytes())
}

func (h Handler) reconcileAssistantRun(r *http.Request, identity modules.RequestContext, record assistantstate.RunRecord) (assistantstate.RunRecord, error) {
	snapshot, err := decodeAssistantRun(record)
	if err != nil {
		return record, err
	}
	resources, ok := h.provider.(provider.ResponseResourceProvider)
	if !ok {
		return record, nil
	}
	model, err := resources.ResolveResponseResource(r.Context(), identity, snapshot.ResponseID)
	if err != nil || model != snapshot.Model {
		return record, nil
	}
	response, err := resources.RetrieveResponse(r.Context(), identity, snapshot.ResponseID)
	if err != nil {
		return record, nil
	}
	status := assistantRunResponseStatus(response.Status)
	action, actionErr := assistantRunAction(response)
	if actionErr != nil {
		action = nil
		status = "failed"
		response.Error = &openai.ResponseError{Code: "invalid_provider_payload", Message: "provider returned invalid function calls"}
	}
	if action != nil {
		status = "requires_action"
	}
	if status == "queued" && record.Status == "queued" || status == "in_progress" && record.Status == "in_progress" {
		return record, nil
	}
	if response.Status != "queued" && response.Status != "in_progress" {
		settlements, supported := h.provider.(provider.BackgroundResponseSettlementProvider)
		if !supported {
			return record, nil
		}
		settled, settleErr := settlements.BackgroundResponseSettled(r.Context(), identity, snapshot.ResponseID)
		if settleErr != nil || !settled {
			return record, nil
		}
	}
	snapshot.LastError, snapshot.IncompleteDetails = response.Error, response.IncompleteDetails
	snapshot.RequiredAction = action
	record.Snapshot, _ = json.Marshal(snapshot)
	return h.transitionAssistantRun(r, record, status)
}

func (h Handler) transitionAssistantRun(r *http.Request, record assistantstate.RunRecord, target string) (assistantstate.RunRecord, error) {
	if record.Status == "queued" && (target == "completed" || target == "incomplete" || target == "requires_action") {
		source := record.Status
		record.Status = "in_progress"
		var err error
		record, err = h.assistantRuns.TransitionRun(r.Context(), record, source, record.Revision)
		if err != nil {
			return record, err
		}
	}
	if record.Status == target {
		return record, nil
	}
	if !assistantstate.ValidRunTransition(record.Status, target) {
		return record, nil
	}
	source := record.Status
	record.Status = target
	return h.assistantRuns.TransitionRun(r.Context(), record, source, record.Revision)
}

func (h Handler) assistantRunInput(r *http.Request, owner, threadID string) ([]any, error) {
	result := make([]any, 0)
	after := ""
	for len(result) <= maxAssistantRunMessages {
		records, next, err := h.assistantThreads.ListThreadMessages(r.Context(), owner, threadID, assistantstate.MessagePageOptions{Limit: 100, After: after, Order: "asc"})
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			var message assistantMessageSnapshot
			if json.Unmarshal(record.Snapshot, &message) != nil {
				return nil, assistantstate.ErrUnavailable
			}
			content := make([]any, 0, len(message.Content))
			for _, part := range message.Content {
				switch part.Type {
				case "text":
					if part.Text == nil {
						return nil, assistantstate.ErrUnavailable
					}
					content = append(content, map[string]any{"type": "input_text", "text": part.Text.Value})
				case "image_url":
					if part.ImageURL == nil {
						return nil, assistantstate.ErrUnavailable
					}
					content = append(content, map[string]any{"type": "input_image", "image_url": part.ImageURL.URL, "detail": part.ImageURL.Detail})
				case "image_file":
					if part.ImageFile == nil {
						return nil, assistantstate.ErrUnavailable
					}
					content = append(content, map[string]any{"type": "input_image", "file_id": part.ImageFile.FileID, "detail": part.ImageFile.Detail})
				default:
					return nil, assistantstate.ErrInvalid
				}
			}
			result = append(result, map[string]any{"role": message.Role, "content": content})
			if len(result) > maxAssistantRunMessages {
				return nil, assistantstate.ErrQuotaExceeded
			}
		}
		if next == "" {
			return result, nil
		}
		after = next
	}
	return nil, assistantstate.ErrQuotaExceeded
}

func assistantRunTools(w http.ResponseWriter, tools []assistantTool) ([]openai.ResponseTool, bool) {
	result := make([]openai.ResponseTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" || tool.Function == nil {
			writeError(w, http.StatusBadRequest, "unsupported_operation", "assistant run tool type "+tool.Type+" is not supported")
			return nil, false
		}
		result = append(result, openai.ResponseTool{Type: "function", Name: tool.Function.Name, Description: tool.Function.Description, Parameters: tool.Function.Parameters, Strict: tool.Function.Strict})
	}
	return result, true
}

func validAssistantRunInstructions(value string) bool {
	return len(value) <= 1<<20 && len([]rune(value)) <= 262144
}

func assistantRunResponseStatus(status string) string {
	switch status {
	case "queued":
		return "queued"
	case "in_progress":
		return "in_progress"
	case "completed":
		return "completed"
	case "cancelled", "canceled":
		return "cancelled"
	case "incomplete":
		return "incomplete"
	default:
		return "failed"
	}
}

func assistantRunAction(response openai.ResponseResponse) (*assistantRunRequiredAction, error) {
	calls := make([]assistantRunToolCall, 0)
	seen := make(map[string]bool)
	for _, item := range response.Output {
		if item.Type != "function_call" {
			continue
		}
		if !validFileToken(item.CallID, 128) || seen[item.CallID] || !validFileToken(item.Name, 64) || len(item.Arguments) > 1<<20 || !json.Valid([]byte(item.Arguments)) || len(calls) >= 128 {
			return nil, errAssistantRunProviderPayload
		}
		seen[item.CallID] = true
		calls = append(calls, assistantRunToolCall{ID: item.CallID, Type: "function", Function: assistantRunToolFunction{Name: item.Name, Arguments: item.Arguments}})
	}
	if len(calls) == 0 {
		return nil, nil
	}
	return &assistantRunRequiredAction{Type: "submit_tool_outputs", SubmitToolOutputs: assistantRunSubmitToolOutputs{ToolCalls: calls}}, nil
}

func (h Handler) assistantRunCollection(w http.ResponseWriter, r *http.Request) (modules.RequestContext, string, bool) {
	identity, ok := h.assistantThreadIdentity(w, r)
	if !ok || !h.assistantRunStorageAvailable(w) {
		return modules.RequestContext{}, "", false
	}
	threadID := r.PathValue("thread_id")
	if !validFileToken(threadID, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "thread ID is invalid")
		return modules.RequestContext{}, "", false
	}
	return identity, threadID, true
}

func (h Handler) assistantRunResource(w http.ResponseWriter, r *http.Request) (modules.RequestContext, string, string, bool) {
	identity, threadID, ok := h.assistantRunCollection(w, r)
	if !ok {
		return modules.RequestContext{}, "", "", false
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return modules.RequestContext{}, "", "", false
	}
	runID := r.PathValue("run_id")
	if !validFileToken(runID, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "run ID is invalid")
		return modules.RequestContext{}, "", "", false
	}
	return identity, threadID, runID, true
}

func (h Handler) assistantRunStorageAvailable(w http.ResponseWriter) bool {
	if h.assistants == nil || h.assistantThreads == nil || h.assistantRuns == nil || h.assistantConfig.RunOwnerQuota < 1 || h.assistantConfig.RunRetention <= 0 {
		writeError(w, http.StatusServiceUnavailable, "assistant_run_storage_unavailable", "assistant run storage is unavailable")
		return false
	}
	return true
}

func assistantRunPageOptions(w http.ResponseWriter, r *http.Request) (assistantstate.RunPageOptions, bool) {
	query := r.URL.Query()
	for key, values := range query {
		if key != "limit" && key != "after" && key != "before" && key != "order" || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return assistantstate.RunPageOptions{}, false
		}
	}
	options := assistantstate.RunPageOptions{Limit: 20, After: query.Get("after"), Before: query.Get("before"), Order: query.Get("order")}
	if options.Order == "" {
		options.Order = "desc"
	}
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return assistantstate.RunPageOptions{}, false
		}
		options.Limit = parsed
	}
	if options.Order != "asc" && options.Order != "desc" || options.After != "" && options.Before != "" || options.After != "" && !validFileToken(options.After, 128) || options.Before != "" && !validFileToken(options.Before, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "run pagination parameters are invalid")
		return assistantstate.RunPageOptions{}, false
	}
	return options, true
}

func decodeAssistantRun(record assistantstate.RunRecord) (assistantRunSnapshot, error) {
	var snapshot assistantRunSnapshot
	if json.Unmarshal(record.Snapshot, &snapshot) != nil || snapshot.AssistantID == "" || snapshot.Model == "" || snapshot.ResponseID == "" || !validAssistantRunAction(snapshot.RequiredAction) {
		return assistantRunSnapshot{}, assistantstate.ErrUnavailable
	}
	return snapshot, nil
}

func validAssistantRunAction(action *assistantRunRequiredAction) bool {
	if action == nil {
		return true
	}
	if action.Type != "submit_tool_outputs" || len(action.SubmitToolOutputs.ToolCalls) < 1 || len(action.SubmitToolOutputs.ToolCalls) > 128 {
		return false
	}
	seen := make(map[string]bool, len(action.SubmitToolOutputs.ToolCalls))
	for _, call := range action.SubmitToolOutputs.ToolCalls {
		if call.Type != "function" || !validFileToken(call.ID, 128) || seen[call.ID] || !validFileToken(call.Function.Name, 64) || len(call.Function.Arguments) > 1<<20 || !json.Valid([]byte(call.Function.Arguments)) {
			return false
		}
		seen[call.ID] = true
	}
	return true
}

func publicAssistantRun(record assistantstate.RunRecord) (map[string]any, error) {
	snapshot, err := decodeAssistantRun(record)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"id": record.ID, "object": "thread.run", "created_at": record.CreatedAt.Unix(), "thread_id": record.ThreadID,
		"assistant_id": snapshot.AssistantID, "status": record.Status, "model": snapshot.Model,
		"instructions": snapshot.Instructions, "metadata": snapshot.Metadata, "response_id": snapshot.ResponseID,
		"last_error": snapshot.LastError, "incomplete_details": snapshot.IncompleteDetails,
		"required_action": snapshot.RequiredAction,
	}
	return result, nil
}

func (h Handler) writeAssistantRun(w http.ResponseWriter, record assistantstate.RunRecord) {
	value, err := publicAssistantRun(record)
	if err != nil {
		writeAssistantRunError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeAssistantRunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, assistantstate.ErrNotFound):
		writeError(w, http.StatusNotFound, "run_not_found", "thread, assistant, or run not found")
	case errors.Is(err, assistantstate.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "run_quota_exceeded", "run quota exceeded")
	case errors.Is(err, assistantstate.ErrConflict):
		writeError(w, http.StatusConflict, "run_conflict", "thread already has an active run or run changed concurrently")
	case errors.Is(err, assistantstate.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid run request")
	default:
		writeError(w, http.StatusServiceUnavailable, "assistant_run_storage_unavailable", "assistant run storage is unavailable")
	}
}

func copyResponseHeaders(w http.ResponseWriter, header http.Header) {
	for key, values := range header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
}
