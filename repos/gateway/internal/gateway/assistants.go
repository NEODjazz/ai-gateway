package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/assistantstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type AssistantRuntimeConfig struct {
	OwnerQuota         int
	ThreadOwnerQuota   int
	MessageThreadQuota int
}

type assistantSnapshot struct {
	Model          string            `json:"model"`
	Name           *string           `json:"name"`
	Description    *string           `json:"description"`
	Instructions   *string           `json:"instructions"`
	Tools          []assistantTool   `json:"tools"`
	ToolResources  json.RawMessage   `json:"tool_resources,omitempty"`
	Metadata       map[string]string `json:"metadata"`
	Temperature    *float64          `json:"temperature,omitempty"`
	TopP           *float64          `json:"top_p,omitempty"`
	ResponseFormat json.RawMessage   `json:"response_format,omitempty"`
}

type assistantTool struct {
	Type     string                     `json:"type"`
	Function *openai.FunctionDefinition `json:"function,omitempty"`
}

type assistantCreateRequest assistantSnapshot

type optionalAssistantString struct {
	Set   bool
	Value *string
}

func (o *optionalAssistantString) UnmarshalJSON(payload []byte) error {
	o.Set = true
	if string(payload) == "null" {
		o.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(payload, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

type optionalAssistantRaw struct {
	Set   bool
	Value json.RawMessage
}

func (o *optionalAssistantRaw) UnmarshalJSON(payload []byte) error {
	o.Set = true
	if string(payload) == "null" {
		o.Value = nil
		return nil
	}
	o.Value = append(o.Value[:0], payload...)
	return nil
}

type optionalAssistantFloat struct {
	Set   bool
	Value *float64
}

func (o *optionalAssistantFloat) UnmarshalJSON(payload []byte) error {
	o.Set = true
	if string(payload) == "null" {
		o.Value = nil
		return nil
	}
	var value float64
	if err := json.Unmarshal(payload, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

type assistantUpdateRequest struct {
	Model          *string                 `json:"model,omitempty"`
	Name           optionalAssistantString `json:"name,omitempty"`
	Description    optionalAssistantString `json:"description,omitempty"`
	Instructions   optionalAssistantString `json:"instructions,omitempty"`
	Tools          *[]assistantTool        `json:"tools,omitempty"`
	ToolResources  optionalAssistantRaw    `json:"tool_resources,omitempty"`
	Metadata       *map[string]string      `json:"metadata,omitempty"`
	Temperature    optionalAssistantFloat  `json:"temperature,omitempty"`
	TopP           optionalAssistantFloat  `json:"top_p,omitempty"`
	ResponseFormat optionalAssistantRaw    `json:"response_format,omitempty"`
}

func (h Handler) WithAssistantStore(store assistantstate.Store, config AssistantRuntimeConfig) Handler {
	h.assistants = store
	if threads, ok := store.(assistantstate.ThreadStore); ok {
		h.assistantThreads = threads
	}
	h.assistantConfig = config
	return h
}

func (h Handler) CreateAssistant(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.assistantIdentity(w, r)
	if !ok || !h.assistantStorageAvailable(w) {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	var input assistantCreateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	snapshot := assistantSnapshot(input)
	normalizeAssistantSnapshot(&snapshot)
	if !validateAssistantSnapshot(w, snapshot) || !h.authorizeAssistantDefinition(w, r.Context(), identity, snapshot) {
		return
	}
	id, ok := newAssistantID()
	if !ok {
		writeError(w, http.StatusInternalServerError, "assistant_id_failed", "assistant ID generation failed")
		return
	}
	payload, err := json.Marshal(snapshot)
	if err != nil || len(payload) > assistantstate.MaxSnapshotBytes {
		writeError(w, http.StatusBadRequest, "invalid_request", "assistant definition exceeds its size limit")
		return
	}
	record, err := h.assistants.CreateAssistant(r.Context(), assistantstate.Record{ID: id, OwnerKey: fileOwnerKey(identity), Snapshot: payload}, h.assistantConfig.OwnerQuota)
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	h.writeAssistant(w, record)
}

func (h Handler) ListAssistants(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.assistantIdentity(w, r)
	if !ok || !h.assistantStorageAvailable(w) {
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if key != "limit" && key != "after" || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return
		}
	}
	limit := 20
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	after := query.Get("after")
	if after != "" && !validFileToken(after, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "after is invalid")
		return
	}
	records, next, err := h.assistants.ListAssistants(r.Context(), fileOwnerKey(identity), limit, after)
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	data := make([]map[string]any, 0, len(records))
	for _, record := range records {
		value, decodeErr := publicAssistant(record)
		if decodeErr != nil {
			writeAssistantError(w, decodeErr)
			return
		}
		data = append(data, value)
	}
	response := map[string]any{"object": "list", "data": data, "has_more": next != ""}
	if len(records) > 0 {
		response["first_id"], response["last_id"] = records[0].ID, records[len(records)-1].ID
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) GetAssistant(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.assistantResource(w, r)
	if !ok {
		return
	}
	record, err := h.assistants.GetAssistant(r.Context(), fileOwnerKey(identity), id)
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	h.writeAssistant(w, record)
}

func (h Handler) UpdateAssistant(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.assistantResource(w, r)
	if !ok {
		return
	}
	var input assistantUpdateRequest
	if !decodeInferenceRequest(w, r, &input) {
		return
	}
	if !assistantUpdatePresent(input) {
		writeError(w, http.StatusBadRequest, "invalid_request", "at least one update field is required")
		return
	}
	owner := fileOwnerKey(identity)
	record, err := h.assistants.GetAssistant(r.Context(), owner, id)
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	var snapshot assistantSnapshot
	if json.Unmarshal(record.Snapshot, &snapshot) != nil {
		writeAssistantError(w, assistantstate.ErrUnavailable)
		return
	}
	applyAssistantUpdate(&snapshot, input)
	normalizeAssistantSnapshot(&snapshot)
	if !validateAssistantSnapshot(w, snapshot) || !h.authorizeAssistantDefinition(w, r.Context(), identity, snapshot) {
		return
	}
	payload, err := json.Marshal(snapshot)
	if err != nil || len(payload) > assistantstate.MaxSnapshotBytes {
		writeError(w, http.StatusBadRequest, "invalid_request", "assistant definition exceeds its size limit")
		return
	}
	updated, err := h.assistants.UpdateAssistant(r.Context(), owner, id, payload, record.Revision)
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	h.writeAssistant(w, updated)
}

func (h Handler) DeleteAssistant(w http.ResponseWriter, r *http.Request) {
	identity, id, ok := h.assistantResource(w, r)
	if !ok {
		return
	}
	if err := h.assistants.DeleteAssistant(r.Context(), fileOwnerKey(identity), id); err != nil {
		writeAssistantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "object": "assistant.deleted", "deleted": true})
}

func (h Handler) assistantIdentity(w http.ResponseWriter, r *http.Request) (modules.RequestContext, bool) {
	return h.authorizeOwnedStorageOperation(w, r, "assistants")
}

func (h Handler) assistantResource(w http.ResponseWriter, r *http.Request) (modules.RequestContext, string, bool) {
	identity, ok := h.assistantIdentity(w, r)
	if !ok || !h.assistantStorageAvailable(w) {
		return modules.RequestContext{}, "", false
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return modules.RequestContext{}, "", false
	}
	id := r.PathValue("id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "assistant ID is invalid")
		return modules.RequestContext{}, "", false
	}
	return identity, id, true
}

func (h Handler) assistantStorageAvailable(w http.ResponseWriter) bool {
	if h.assistants == nil || h.assistantConfig.OwnerQuota < 1 {
		writeError(w, http.StatusServiceUnavailable, "assistant_storage_unavailable", "assistant storage is unavailable")
		return false
	}
	return true
}

func (h Handler) authorizeAssistantDefinition(w http.ResponseWriter, ctx context.Context, identity modules.RequestContext, snapshot assistantSnapshot) bool {
	if !h.authorizeModel(w, identity, snapshot.Model) {
		return false
	}
	tools := make([]string, 0, len(snapshot.Tools))
	for _, tool := range snapshot.Tools {
		if tool.Type == "function" {
			if tool.Function == nil {
				return false
			}
			tools = append(tools, tool.Function.Name)
		} else {
			tools = append(tools, tool.Type)
		}
	}
	return h.authorizeTools(w, identity, tools, true) && h.authorizeAssistantResources(w, ctx, identity, snapshot)
}

func (h Handler) authorizeAssistantResources(w http.ResponseWriter, ctx context.Context, identity modules.RequestContext, snapshot assistantSnapshot) bool {
	if len(snapshot.ToolResources) == 0 {
		return true
	}
	var resources struct {
		CodeInterpreter *struct {
			FileIDs []string `json:"file_ids"`
		} `json:"code_interpreter,omitempty"`
		FileSearch *struct {
			VectorStoreIDs []string `json:"vector_store_ids"`
		} `json:"file_search,omitempty"`
	}
	if err := decodeStrictJSON(snapshot.ToolResources, &resources); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "assistant tool_resources are invalid")
		return false
	}
	configured := map[string]bool{}
	for _, tool := range snapshot.Tools {
		configured[tool.Type] = true
	}
	owner := fileOwnerKey(identity)
	if resources.CodeInterpreter != nil {
		if !configured["code_interpreter"] || len(resources.CodeInterpreter.FileIDs) > 20 || h.files == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "code interpreter resources are unavailable or invalid")
			return false
		}
		seen := map[string]bool{}
		for _, id := range resources.CodeInterpreter.FileIDs {
			if !validFileToken(id, 128) || seen[id] {
				writeError(w, http.StatusBadRequest, "invalid_request", "code interpreter file IDs are invalid")
				return false
			}
			seen[id] = true
			file, err := h.files.Get(ctx, owner, id, false)
			if err != nil || file.Purpose != "assistants" {
				writeError(w, http.StatusBadRequest, "invalid_request", "code interpreter file is unavailable")
				return false
			}
		}
	}
	if resources.FileSearch != nil {
		if !configured["file_search"] || len(resources.FileSearch.VectorStoreIDs) > 1 || h.vectorStores == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "file search resources are unavailable or invalid")
			return false
		}
		for _, id := range resources.FileSearch.VectorStoreIDs {
			if !validFileToken(id, 128) {
				writeError(w, http.StatusBadRequest, "invalid_request", "file search vector store ID is invalid")
				return false
			}
			if _, err := h.vectorStores.GetVectorStore(ctx, owner, id); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "file search vector store is unavailable")
				return false
			}
		}
	}
	return true
}

func validateAssistantSnapshot(w http.ResponseWriter, snapshot assistantSnapshot) bool {
	if snapshot.Model == "" || len(snapshot.Model) > 256 || strings.TrimSpace(snapshot.Model) != snapshot.Model || !validAssistantOptionalText(snapshot.Name, 256) || !validAssistantOptionalText(snapshot.Description, 512) || !validAssistantOptionalText(snapshot.Instructions, 256<<10) {
		writeError(w, http.StatusBadRequest, "invalid_request", "assistant model or text fields are invalid")
		return false
	}
	if len(snapshot.Tools) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_request", "assistant tools exceed the supported limit")
		return false
	}
	seen := map[string]bool{}
	for _, tool := range snapshot.Tools {
		identifier := tool.Type
		switch tool.Type {
		case "code_interpreter", "file_search":
			if tool.Function != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "built-in assistant tools cannot include a function")
				return false
			}
		case "function":
			if tool.Function == nil || !validAssistantFunction(*tool.Function) {
				writeError(w, http.StatusBadRequest, "invalid_request", "assistant function tool is invalid")
				return false
			}
			identifier += ":" + tool.Function.Name
		default:
			writeError(w, http.StatusBadRequest, "invalid_request", "assistant tool type is unsupported")
			return false
		}
		if seen[identifier] {
			writeError(w, http.StatusBadRequest, "invalid_request", "assistant tools must be unique")
			return false
		}
		seen[identifier] = true
	}
	if message := openai.ValidateMetadata(snapshot.Metadata); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return false
	}
	if !validAssistantRawObject(snapshot.ToolResources, 256<<10) || !validAssistantRaw(snapshot.ResponseFormat, 64<<10) || snapshot.Temperature != nil && (math.IsNaN(*snapshot.Temperature) || math.IsInf(*snapshot.Temperature, 0) || *snapshot.Temperature < 0 || *snapshot.Temperature > 2) || snapshot.TopP != nil && (math.IsNaN(*snapshot.TopP) || math.IsInf(*snapshot.TopP, 0) || *snapshot.TopP < 0 || *snapshot.TopP > 1) {
		writeError(w, http.StatusBadRequest, "invalid_request", "assistant tool resources, response format or sampling controls are invalid")
		return false
	}
	return true
}

func validAssistantOptionalText(value *string, maximum int) bool {
	return value == nil || utf8.ValidString(*value) && utf8.RuneCountInString(*value) <= maximum
}

func validAssistantFunction(function openai.FunctionDefinition) bool {
	if function.Name == "" || len(function.Name) > 64 || len(function.Description) > 1024 || function.PromptCacheBreakpoint != nil {
		return false
	}
	for _, character := range function.Name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	payload, err := json.Marshal(function.Parameters)
	return err == nil && len(payload) <= 64<<10
}

func validAssistantRawObject(value json.RawMessage, maximum int) bool {
	if len(value) == 0 {
		return true
	}
	var object map[string]any
	return len(value) <= maximum && json.Unmarshal(value, &object) == nil && object != nil
}

func validAssistantRaw(value json.RawMessage, maximum int) bool {
	return len(value) == 0 || len(value) <= maximum && json.Valid(value)
}

func assistantUpdatePresent(input assistantUpdateRequest) bool {
	return input.Model != nil || input.Name.Set || input.Description.Set || input.Instructions.Set || input.Tools != nil || input.ToolResources.Set || input.Metadata != nil || input.Temperature.Set || input.TopP.Set || input.ResponseFormat.Set
}

func applyAssistantUpdate(snapshot *assistantSnapshot, input assistantUpdateRequest) {
	if input.Model != nil {
		snapshot.Model = *input.Model
	}
	if input.Name.Set {
		snapshot.Name = input.Name.Value
	}
	if input.Description.Set {
		snapshot.Description = input.Description.Value
	}
	if input.Instructions.Set {
		snapshot.Instructions = input.Instructions.Value
	}
	if input.Tools != nil {
		snapshot.Tools = append([]assistantTool(nil), (*input.Tools)...)
	}
	if input.ToolResources.Set {
		snapshot.ToolResources = append(json.RawMessage(nil), input.ToolResources.Value...)
	}
	if input.Metadata != nil {
		snapshot.Metadata = normalizedMetadata(*input.Metadata)
	}
	if input.Temperature.Set {
		snapshot.Temperature = input.Temperature.Value
	}
	if input.TopP.Set {
		snapshot.TopP = input.TopP.Value
	}
	if input.ResponseFormat.Set {
		snapshot.ResponseFormat = append(json.RawMessage(nil), input.ResponseFormat.Value...)
	}
}

func normalizeAssistantSnapshot(snapshot *assistantSnapshot) {
	if snapshot.Tools == nil {
		snapshot.Tools = []assistantTool{}
	}
	snapshot.Metadata = normalizedMetadata(snapshot.Metadata)
	if string(snapshot.ToolResources) == "null" {
		snapshot.ToolResources = nil
	}
	if string(snapshot.ResponseFormat) == "null" {
		snapshot.ResponseFormat = nil
	}
}

func newAssistantID() (string, bool) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", false
	}
	return "asst_" + hex.EncodeToString(value[:]), true
}

func publicAssistant(record assistantstate.Record) (map[string]any, error) {
	var snapshot assistantSnapshot
	if json.Unmarshal(record.Snapshot, &snapshot) != nil {
		return nil, assistantstate.ErrUnavailable
	}
	payload, _ := json.Marshal(snapshot)
	var result map[string]any
	if json.Unmarshal(payload, &result) != nil {
		return nil, assistantstate.ErrUnavailable
	}
	result["id"], result["object"], result["created_at"] = record.ID, "assistant", record.CreatedAt.Unix()
	return result, nil
}

func (h Handler) writeAssistant(w http.ResponseWriter, record assistantstate.Record) {
	value, err := publicAssistant(record)
	if err != nil {
		writeAssistantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeAssistantError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, assistantstate.ErrNotFound):
		writeError(w, http.StatusNotFound, "assistant_not_found", "assistant not found")
	case errors.Is(err, assistantstate.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "assistant_quota_exceeded", "assistant quota exceeded")
	case errors.Is(err, assistantstate.ErrConflict):
		writeError(w, http.StatusConflict, "assistant_conflict", "assistant was modified concurrently")
	case errors.Is(err, assistantstate.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid assistant request")
	default:
		writeError(w, http.StatusServiceUnavailable, "assistant_storage_unavailable", "assistant storage is unavailable")
	}
}
