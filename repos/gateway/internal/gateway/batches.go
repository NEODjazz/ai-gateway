package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/batchstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

const (
	batchJobKind       = "batch.item.v1"
	batchOwnerQuota    = 100
	batchWorkerSize    = 10
	batchItemTimeout   = 15 * time.Minute
	batchWorkerLease   = 16 * time.Minute
	batchResultMaxSize = 64 << 20
	batchResultMinLine = 512
	batchMaxLineBytes  = 4 << 20
)

type batchJob struct {
	Ordinal int `json:"ordinal"`
}

func (h Handler) batchStorageAvailable(w http.ResponseWriter) bool {
	if h.batches == nil || h.batchJobs == nil || h.files == nil || h.fileConfig.MaxBytes < 1 {
		writeError(w, http.StatusServiceUnavailable, "batch_storage_unavailable", "batch storage is unavailable")
		return false
	}
	return true
}

func (h Handler) CreateBatch(w http.ResponseWriter, r *http.Request) {
	var request openai.BatchCreateRequest
	if !decodeInferenceRequest(w, r, &request) {
		return
	}
	if message := request.Validate(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", message)
		return
	}
	identity, ok := h.authorizeOwnedStorageOperation(w, r, "batches")
	if !ok {
		return
	}
	if !h.batchStorageAvailable(w) {
		return
	}
	owner := fileOwnerKey(identity)
	input, err := h.files.Get(r.Context(), owner, request.InputFileID, true)
	if err != nil {
		writeBatchStoreError(w, err)
		return
	}
	if input.Purpose != "batch" {
		writeError(w, http.StatusBadRequest, "invalid_request", "input_file_id must reference a file with purpose=batch")
		return
	}
	items, err := h.decodeBatchItems(w, r.Context(), identity, request.Endpoint, input.Content)
	if err != nil {
		if !errors.Is(err, errBatchResponseWritten) {
			writeError(w, http.StatusBadRequest, "invalid_batch_file", err.Error())
		}
		return
	}
	if len(items) > h.batchResultLimit()/batchResultMinLine {
		writeError(w, http.StatusBadRequest, "invalid_batch_file", "batch contains too many requests for the configured output file limit")
		return
	}
	if request.Metadata == nil {
		request.Metadata = map[string]string{}
	}
	metadata, _ := json.Marshal(request.Metadata)
	identity.APIKey = ""
	identity.RequestID = ""
	identity.Metadata = map[string]string{"gateway.api_type": "batch"}
	identityPayload, err := json.Marshal(identity)
	if err != nil || len(identityPayload) > batchstate.MaxIdentityBytes {
		writeError(w, http.StatusBadRequest, "invalid_request", "batch identity is too large")
		return
	}
	batchID := "batch_" + newExecutionID()
	var outputExpirySeconds int64
	if request.OutputExpiresAfter != nil {
		outputExpirySeconds = request.OutputExpiresAfter.Seconds
	}
	batch := batchstate.Batch{ID: batchID, OwnerKey: owner, InputFileID: request.InputFileID, Endpoint: request.Endpoint, CompletionWindow: request.CompletionWindow, OutputExpirySeconds: outputExpirySeconds, Status: "queued", Metadata: metadata, Identity: identityPayload, Total: len(items), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
	jobs := make([]asyncstate.Job, len(items))
	for index := range items {
		items[index].BatchID = batchID
		items[index].OwnerKey = owner
		items[index].Ordinal = index
		items[index].State = "pending"
		items[index].ExecutionID = newExecutionID()
		payload, _ := json.Marshal(batchJob{Ordinal: index})
		jobs[index] = asyncstate.Job{Kind: batchJobKind, ResourceID: batchID + ":" + strconv.Itoa(index), OwnerKey: owner, EndpointID: "gateway", ExecutionID: items[index].ExecutionID, Payload: payload}
	}
	created, err := h.batches.CreateBatch(r.Context(), batch, items, jobs, batchOwnerQuota)
	if err != nil {
		writeBatchStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicBatch(created))
}

var errBatchResponseWritten = errors.New("batch response already written")

func (h Handler) decodeBatchItems(w http.ResponseWriter, ctx context.Context, identity modules.RequestContext, endpoint string, payload []byte) ([]batchstate.Item, error) {
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 64<<10), batchMaxLineBytes)
	items := make([]batchstate.Item, 0)
	seen := map[string]struct{}{}
	for scanner.Scan() {
		lineNumber := len(items) + 1
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			return nil, fmt.Errorf("line %d is empty", lineNumber)
		}
		var line openai.BatchRequestLine
		if err := decodeStrictJSON(scanner.Bytes(), &line); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if line.CustomID == "" || len(line.CustomID) > 128 {
			return nil, fmt.Errorf("line %d: custom_id is invalid", lineNumber)
		}
		if _, exists := seen[line.CustomID]; exists {
			return nil, fmt.Errorf("line %d: custom_id is duplicated", lineNumber)
		}
		seen[line.CustomID] = struct{}{}
		if line.Method != http.MethodPost {
			return nil, fmt.Errorf("line %d: method must be POST", lineNumber)
		}
		if line.URL != endpoint {
			return nil, fmt.Errorf("line %d: url must match endpoint", lineNumber)
		}
		normalized, model, tools, err := validateBatchBody(endpoint, line.Body)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if endpoint == "/v1/ocr" {
			var request openai.OCRRequest
			if json.Unmarshal(normalized, &request) != nil {
				return nil, fmt.Errorf("line %d: normalized OCR request is invalid", lineNumber)
			}
			if request.Document.Type == "file" {
				resolved, resolveErr := h.resolveOCRFile(ctx, fileOwnerKey(identity), request.Document.FileID)
				if resolveErr != nil {
					writeOCRFileError(w, resolveErr)
					return nil, errBatchResponseWritten
				}
				request.Document = resolved
				normalized, err = json.Marshal(request)
				if err != nil {
					return nil, fmt.Errorf("line %d: encode resolved OCR request: %w", lineNumber, err)
				}
			}
		}
		if endpoint == "/v1/messages" {
			var request messagesRequest
			if json.Unmarshal(normalized, &request) != nil {
				return nil, fmt.Errorf("line %d: normalized Messages request is invalid", lineNumber)
			}
			resolveErr := h.resolveMessagesRequestFileReferences(ctx, identity, &request)
			if resolveErr != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber, resolveErr)
			}
			normalized, err = json.Marshal(request)
			if err != nil {
				return nil, fmt.Errorf("line %d: encode resolved Messages request: %w", lineNumber, err)
			}
			if len(normalized) > batchMaxLineBytes {
				return nil, fmt.Errorf("line %d: resolved Messages request exceeds the 4 MiB limit", lineNumber)
			}
		}
		if !h.authorizeBatchModel(w, identity, model) {
			return nil, errBatchResponseWritten
		}
		itemIdentity := identity
		itemIdentity.RequestID = ""
		itemIdentity.Request = openai.ChatCompletionRequest{}
		if !h.prepareModelFallbacks(w, ctx, &itemIdentity, model) {
			return nil, errBatchResponseWritten
		}
		if len(tools) > 0 && !h.authorizeTools(w, itemIdentity, tools, true) {
			return nil, errBatchResponseWritten
		}
		itemIdentity.APIKey = ""
		identityPayload, marshalErr := json.Marshal(itemIdentity)
		if marshalErr != nil || len(identityPayload) > batchstate.MaxIdentityBytes {
			return nil, fmt.Errorf("line %d: effective policy is too large", lineNumber)
		}
		items = append(items, batchstate.Item{CustomID: line.CustomID, URL: line.URL, Body: normalized, Identity: identityPayload})
		if len(items) > openai.MaxBatchRequests {
			return nil, fmt.Errorf("batch contains more than %d requests", openai.MaxBatchRequests)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read JSONL: %w", err)
	}
	if len(items) == 0 {
		return nil, errors.New("batch file contains no requests")
	}
	return items, nil
}

func decodeStrictJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must contain exactly one JSON value")
	}
	return nil
}

func validateBatchBody(endpoint string, body []byte) ([]byte, string, []string, error) {
	var model string
	var tools []string
	var normalized any
	switch endpoint {
	case "/v1/chat/completions":
		var request openai.ChatCompletionRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if request.Stream {
			return nil, "", nil, errors.New("streaming is not supported in batches")
		}
		if message := request.ChatGenerationOptions.Validate(); message != "" {
			return nil, "", nil, errors.New(message)
		}
		if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
			return nil, "", nil, errors.New("max_tokens and max_completion_tokens are mutually exclusive")
		}
		if request.StreamOptions != nil {
			return nil, "", nil, errors.New("stream_options requires streaming and is not supported in batches")
		}
		if err := openai.ValidateLegacyFunctionRequest(request); err != nil {
			return nil, "", nil, err
		}
		if _, err := openai.ChatImageAttachments(request.Messages); err != nil {
			return nil, "", nil, err
		}
		if _, err := openai.ChatAudioAttachments(request.Messages); err != nil {
			return nil, "", nil, err
		}
		if _, err := openai.ChatFileAttachments(request.Messages); err != nil {
			return nil, "", nil, err
		}
		if _, err := openai.ChatVideoAttachments(request.Messages); err != nil {
			return nil, "", nil, err
		}
		model = request.Model
		var valid bool
		tools, valid = chatToolIdentifiers(request.Tools, request.Functions)
		if !valid {
			return nil, "", nil, errors.New("tools contain an invalid function name")
		}
		normalized = request
	case "/v1/messages":
		var request messagesRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		chat, err := request.chat()
		if err != nil {
			return nil, "", nil, err
		}
		if request.Stream {
			return nil, "", nil, errors.New("streaming Messages is not supported in batches")
		}
		model = request.Model
		var valid bool
		tools, valid = chatToolIdentifiers(chat.Tools, chat.Functions)
		if !valid {
			return nil, "", nil, errors.New("tools contain an invalid function name")
		}
		tools = append(tools, skillExecutionIdentifiers(chat.AnthropicSkills)...)
		if chat.AnthropicCodeExecution {
			tools = append(tools, "code_execution")
		}
		if chat.AnthropicToolSearch != "" {
			tools = append(tools, "tool_search")
		}
		tools = append(tools, anthropicClientToolIdentifiers(chat.AnthropicClientTools)...)
		tools = append(tools, anthropicClientToolsetIdentifiers(chat.AnthropicClientToolsets)...)
		normalized = request
	case "/v1/completions":
		var request openai.CompletionRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if request.Stream {
			return nil, "", nil, errors.New("streaming is not supported in batches")
		}
		if message := validateCompletionRequest(request); message != "" {
			return nil, "", nil, errors.New(message)
		}
		model = request.Model
		normalized = request
	case "/v1/responses":
		var request openai.ResponseRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if request.Stream || request.Background {
			return nil, "", nil, errors.New("stream and background are not supported in batches")
		}
		if message := request.Validate(); message != "" {
			return nil, "", nil, errors.New(message)
		}
		if _, err := openai.ResponseImageAttachments(request.Input); err != nil {
			return nil, "", nil, err
		}
		if _, err := openai.ResponseAudioAttachments(request.Input); err != nil {
			return nil, "", nil, err
		}
		if _, err := openai.ResponseFileAttachments(request.Input); err != nil {
			return nil, "", nil, err
		}
		model = request.Model
		var valid bool
		tools, valid = responseToolIdentifiers(request.Tools)
		if !valid {
			return nil, "", nil, errors.New("tools contain an invalid function name")
		}
		normalized = request
	case "/v1/responses/compact":
		var request openai.ResponseCompactRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if message := validateResponseCompactRequest(request); message != "" {
			return nil, "", nil, errors.New(message)
		}
		model = request.Model
		normalized = request
	case "/v1/embeddings":
		var request openai.EmbeddingRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if strings.TrimSpace(request.Model) == "" {
			return nil, "", nil, errors.New("model is required")
		}
		if _, err := openai.InspectEmbeddingInput(request.Input); err != nil {
			return nil, "", nil, err
		}
		if message := openai.ValidateMetadata(request.Metadata); message != "" {
			return nil, "", nil, errors.New(message)
		}
		if request.EncodingFormat != "" && request.EncodingFormat != "float" && request.EncodingFormat != "base64" {
			return nil, "", nil, errors.New("encoding_format must be float or base64")
		}
		if request.OutputDType != "" && request.OutputDType != "float" && request.OutputDType != "int8" && request.OutputDType != "uint8" && request.OutputDType != "binary" && request.OutputDType != "ubinary" {
			return nil, "", nil, errors.New("output_dtype is invalid")
		}
		if request.InputType != "" && request.InputType != "search_document" && request.InputType != "search_query" && request.InputType != "classification" && request.InputType != "clustering" {
			return nil, "", nil, errors.New("input_type is invalid")
		}
		if request.Dimensions != nil && *request.Dimensions <= 0 {
			return nil, "", nil, errors.New("dimensions must be positive")
		}
		model = request.Model
		normalized = request
	case "/v1/moderations":
		var request openai.ModerationRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if request.Model == "" {
			request.Model = "omni-moderation-latest"
		}
		if _, err := openai.InspectModerationInput(request.Input); err != nil {
			return nil, "", nil, err
		}
		if message := openai.ValidateMetadata(request.Metadata); message != "" {
			return nil, "", nil, errors.New(message)
		}
		model = request.Model
		normalized = request
	case "/v1/rerank":
		var request openai.RerankRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if message := validateRerankRequest(request); message != "" {
			return nil, "", nil, errors.New(message)
		}
		model = request.Model
		normalized = request
	case "/v1/search":
		var request openai.SearchRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if message := request.Validate(); message != "" {
			return nil, "", nil, errors.New(message)
		}
		model, _ = request.RoutingModel()
		normalized = request
	case "/v1/images/generations":
		var request openai.ImageGenerationRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if message := request.Validate(); message != "" {
			return nil, "", nil, errors.New(message)
		}
		if request.Stream {
			return nil, "", nil, errors.New("stream is not supported for batch image generation")
		}
		model = request.Model
		normalized = request
	case "/v1/images/edits":
		var request openai.ImageEditRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if message := request.Validate(); message != "" {
			return nil, "", nil, errors.New(message)
		}
		if request.Stream {
			return nil, "", nil, errors.New("streaming image edits are not supported in batches")
		}
		model = request.Model
		normalized = request
	case "/v1/images/variations":
		var request openai.ImageVariationRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if message := request.Validate(); message != "" {
			return nil, "", nil, errors.New(message)
		}
		model = request.Model
		normalized = request
	case "/v1/audio/speech":
		var request openai.AudioSpeechRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if message := request.Validate(); message != "" {
			return nil, "", nil, errors.New(message)
		}
		if request.StreamFormat == "sse" {
			return nil, "", nil, errors.New("stream_format=sse is not supported in batches")
		}
		model = request.Model
		normalized = request
	case "/v1/audio/transcriptions", "/v1/audio/translations":
		var request openai.AudioTranscriptionRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if message := request.Validate(); message != "" {
			return nil, "", nil, errors.New(message)
		}
		if request.Stream {
			return nil, "", nil, errors.New("streaming audio is not supported in batches")
		}
		model = request.Model
		normalized = request
	case "/v1/ocr":
		var request openai.OCRRequest
		if err := decodeStrictJSON(body, &request); err != nil {
			return nil, "", nil, err
		}
		if message := request.Validate(); message != "" {
			return nil, "", nil, errors.New(message)
		}
		model = request.Model
		normalized = request
	default:
		return nil, "", nil, errors.New("unsupported endpoint")
	}
	if strings.TrimSpace(model) == "" {
		return nil, "", nil, errors.New("model is required")
	}
	encoded, err := json.Marshal(normalized)
	return encoded, model, tools, err
}

func (h Handler) authorizeBatchModel(w http.ResponseWriter, req modules.RequestContext, model string) bool {
	if !modelAllowed(model, req.AllowedModels) {
		writeError(w, http.StatusForbidden, "model_not_allowed", "credential is not allowed to use model "+strconv.Quote(model))
		return false
	}
	if req.AccessGroupsEvaluated && (len(req.AccessGroupModels) == 0 || !modelAllowed(model, req.AccessGroupModels)) {
		writeError(w, http.StatusForbidden, "access_group_model_not_allowed", "assigned access groups do not allow the requested model")
		return false
	}
	if h.access != nil {
		if allowed, _ := h.access.TagModelAllowed(req.Tags, model); !allowed {
			writeError(w, http.StatusForbidden, "tag_model_not_allowed", "credential tags do not allow the requested model")
			return false
		}
	}
	return true
}

func (h Handler) GetBatch(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeBatchOperation(w, r)
	if !ok {
		return
	}
	batch, err := h.batches.GetBatch(r.Context(), fileOwnerKey(req), r.PathValue("id"))
	if err != nil {
		writeBatchStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicBatch(batch))
}
func (h Handler) ListBatches(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeBatchOperation(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if (key != "after" && key != "limit") || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return
		}
	}
	limit := 20
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	batches, next, err := h.batches.ListBatches(r.Context(), fileOwnerKey(req), limit, query.Get("after"))
	if err != nil {
		writeBatchStoreError(w, err)
		return
	}
	data := make([]openai.Batch, len(batches))
	for i := range batches {
		data[i] = publicBatch(batches[i])
	}
	result := openai.BatchList{Object: "list", Data: data, HasMore: next != ""}
	if len(data) > 0 {
		result.FirstID = data[0].ID
		result.LastID = data[len(data)-1].ID
	}
	writeJSON(w, http.StatusOK, result)
}
func (h Handler) CancelBatch(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeBatchOperation(w, r)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	batch, err := h.batches.CancelBatch(r.Context(), fileOwnerKey(req), r.PathValue("id"))
	if err != nil {
		writeBatchStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicBatch(batch))
}
func (h Handler) authorizeBatchOperation(w http.ResponseWriter, r *http.Request) (modules.RequestContext, bool) {
	req, ok := h.authorizeOwnedStorageOperation(w, r, "batches")
	if !ok {
		return modules.RequestContext{}, false
	}
	if !h.batchStorageAvailable(w) {
		return modules.RequestContext{}, false
	}
	id := r.PathValue("id")
	if id != "" && !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "batch ID is invalid")
		return modules.RequestContext{}, false
	}
	return req, true
}

func publicBatch(value batchstate.Batch) openai.Batch {
	var metadata map[string]string
	_ = json.Unmarshal(value.Metadata, &metadata)
	result := openai.Batch{ID: value.ID, Object: "batch", Endpoint: value.Endpoint, Errors: nil, InputFileID: value.InputFileID, CompletionWindow: value.CompletionWindow, Status: value.Status, OutputFileID: value.OutputFileID, ErrorFileID: value.ErrorFileID, CreatedAt: value.CreatedAt.Unix(), ExpiresAt: value.ExpiresAt.Unix(), RequestCounts: openai.BatchRequestCounts{Total: value.Total, Completed: value.Completed, Failed: value.Failed}, Metadata: metadata}
	setTime := func(target *int64, value *time.Time) {
		if value != nil {
			*target = value.Unix()
		}
	}
	setTime(&result.InProgressAt, value.InProgressAt)
	setTime(&result.FinalizingAt, value.FinalizingAt)
	setTime(&result.CompletedAt, value.CompletedAt)
	setTime(&result.FailedAt, value.FailedAt)
	setTime(&result.ExpiredAt, value.ExpiredAt)
	setTime(&result.CancellingAt, value.CancellingAt)
	setTime(&result.CancelledAt, value.CancelledAt)
	return result
}
func writeBatchStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, batchstate.ErrNotFound), errors.Is(err, filestate.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "batch or input file not found")
	case errors.Is(err, batchstate.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "batch_limit_exceeded", "too many active batches")
	case errors.Is(err, batchstate.ErrConflict):
		writeError(w, http.StatusConflict, "batch_conflict", "batch cannot be changed in its current state")
	case errors.Is(err, filestate.ErrQuotaExceeded):
		writeError(w, http.StatusRequestEntityTooLarge, "file_quota_exceeded", "file storage quota exceeded")
	default:
		writeError(w, http.StatusServiceUnavailable, "batch_storage_unavailable", "batch storage is unavailable")
	}
}

func (h Handler) ProcessBatchItems(ctx context.Context) (int, error) {
	if h.batchJobs == nil || h.batches == nil {
		return 0, batchstate.ErrUnavailable
	}
	jobs, err := h.batchJobs.ClaimAsyncJobs(ctx, batchJobKind, batchWorkerSize, batchWorkerLease)
	if err != nil {
		return 0, err
	}
	failures := make(chan error, len(jobs))
	var workers sync.WaitGroup
	for _, job := range jobs {
		job := job
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := h.processBatchItem(ctx, job); err != nil {
				failures <- fmt.Errorf("%s: %w", job.ResourceID, err)
			}
		}()
	}
	workers.Wait()
	close(failures)
	var joined []error
	for err := range failures {
		joined = append(joined, err)
	}
	return len(jobs), errors.Join(joined...)
}
func (h Handler) processBatchItem(ctx context.Context, job asyncstate.Job) error {
	var payload batchJob
	if json.Unmarshal(job.Payload, &payload) != nil || payload.Ordinal < 0 {
		return h.retryBatchJob(ctx, job, errors.New("invalid batch job payload"))
	}
	resource := strings.SplitN(job.ResourceID, ":", 2)
	if len(resource) != 2 {
		return h.retryBatchJob(ctx, job, errors.New("invalid batch job resource ID"))
	}
	batch, err := h.batches.GetBatch(ctx, job.OwnerKey, resource[0])
	if err != nil {
		return h.retryBatchJob(ctx, job, err)
	}
	if batch.Status == "cancelled" || batch.Status == "completed" || batch.Status == "failed" || batch.Status == "expired" {
		return h.batchJobs.CompleteAsyncJob(ctx, job.Kind, job.ResourceID, job.LeaseGeneration)
	}
	if !time.Now().Before(batch.ExpiresAt) {
		if _, err := h.batches.ExpireBatch(ctx, job.OwnerKey, batch.ID); err != nil && !errors.Is(err, batchstate.ErrConflict) {
			return h.retryBatchJob(ctx, job, err)
		}
		return h.batchJobs.CompleteAsyncJob(ctx, job.Kind, job.ResourceID, job.LeaseGeneration)
	}
	item, err := h.batches.GetBatchItem(ctx, job.OwnerKey, batch.ID, payload.Ordinal)
	if err != nil {
		return h.retryBatchJob(ctx, job, err)
	}
	if item.State == "pending" {
		batch, err = h.batches.StartBatch(ctx, job.OwnerKey, batch.ID)
		if err != nil {
			if errors.Is(err, batchstate.ErrConflict) {
				return h.batchJobs.CompleteAsyncJob(ctx, job.Kind, job.ResourceID, job.LeaseGeneration)
			}
			return h.retryBatchJob(ctx, job, err)
		}
		itemCtx, cancel := context.WithTimeout(ctx, batchItemTimeout)
		result, failed, retry := h.executeBatchItem(itemCtx, batch, item)
		cancel()
		if retry {
			return h.retryBatchJob(ctx, job, errBatchRateLimited)
		}
		item.Result = result
		batch, err = h.batches.FinishBatchItem(ctx, item, failed)
		if err != nil {
			return h.retryBatchJob(ctx, job, err)
		}
	} else {
		batch, err = h.batches.GetBatch(ctx, job.OwnerKey, batch.ID)
		if err != nil {
			return h.retryBatchJob(ctx, job, err)
		}
	}
	if batch.Status == "finalizing" {
		if _, err = h.finalizeBatch(ctx, batch); err != nil {
			return h.retryBatchJob(ctx, job, err)
		}
	}
	return h.batchJobs.CompleteAsyncJob(ctx, job.Kind, job.ResourceID, job.LeaseGeneration)
}

func (h Handler) executeBatchItem(ctx context.Context, batch batchstate.Batch, item batchstate.Item) ([]byte, bool, bool) {
	var identity modules.RequestContext
	if json.Unmarshal(item.Identity, &identity) != nil {
		return batchErrorResult(item, "invalid_batch_state", "stored batch identity is invalid"), true, false
	}
	identity.RequestID = item.ExecutionID
	if identity.Metadata == nil {
		identity.Metadata = map[string]string{}
	}
	identity.Metadata["gateway.api_type"] = "batch"
	identity.Metadata["gateway.batch_id"] = batch.ID
	identity.Metadata["gateway.batch_custom_id"] = item.CustomID
	status, body, err := h.callBatchProvider(ctx, &identity, item.URL, item.Body)
	if errors.Is(err, errBatchRateLimited) {
		return nil, false, true
	}
	if err != nil {
		log.Printf("batch item %s/%s failed: %v", batch.ID, item.CustomID, err)
		return batchErrorResult(item, "provider_error", "batch item execution failed"), true, false
	}
	line := openai.BatchOutputLine{ID: "batch_req_" + item.ExecutionID, CustomID: item.CustomID, Response: &openai.BatchOutputResponse{StatusCode: status, RequestID: item.ExecutionID, Body: body}}
	encoded, _ := json.Marshal(line)
	itemLimit := h.batchResultLimit() / max(batch.Total, 1)
	if len(encoded)+1 > itemLimit {
		return batchErrorResult(item, "batch_result_too_large", "batch item result exceeds storage limit"), true, false
	}
	return encoded, false, false
}

func (h Handler) callBatchProvider(ctx context.Context, req *modules.RequestContext, endpoint string, body []byte) (int, json.RawMessage, error) {
	switch endpoint {
	case "/v1/chat/completions":
		var value openai.ChatCompletionRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.Request = value
		if !h.allowBatchRate(ctx, *req, estimateChatTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		response, err := h.provider.ChatCompletions(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/messages":
		var value messagesRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		chat, err := value.chat()
		if err != nil {
			return 0, nil, err
		}
		req.Request = chat
		req.Metadata["gateway.api_type"] = "messages"
		if err := h.bindSkillExecution(ctx, req); err != nil {
			return 0, nil, err
		}
		if !h.allowBatchRate(ctx, *req, estimateChatTokens(chat)) {
			return 0, nil, errBatchRateLimited
		}
		response, err := h.provider.ChatCompletions(ctx, *req)
		if err != nil {
			return 0, nil, err
		}
		if err := h.recordSkillExecution(ctx, *req, response); err != nil {
			return 0, nil, err
		}
		adapted, err := messagesResponsePayload(response)
		if err != nil {
			return 0, nil, err
		}
		payload, _ := json.Marshal(adapted)
		return http.StatusOK, payload, nil
	case "/v1/responses":
		var value openai.ResponseRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.ResponseRequest = &value
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model, Messages: responseMessages(value)}
		if !h.allowBatchRate(ctx, *req, estimateResponseTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		response, err := h.provider.Responses(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/responses/compact":
		var value openai.ResponseCompactRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		responseRequest := openai.ResponseRequest{Provider: value.Provider, Model: value.Model, Input: value.Input, Instructions: value.Instructions}
		req.ResponseRequest = &responseRequest
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model, Messages: responseMessages(responseRequest)}
		if !h.allowBatchRate(ctx, *req, estimateResponseCompactTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.ResponseCompactProvider)
		if !ok {
			return 0, nil, provider.ErrResponseCompactionUnsupported
		}
		response, err := client.CompactResponse(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/completions":
		var value openai.CompletionRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.CompletionRequest = &value
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model}
		if !h.allowBatchRate(ctx, *req, estimateCompletionTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.CompletionProvider)
		if !ok {
			return 0, nil, provider.ErrCompletionsUnsupported
		}
		response, err := client.Completions(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/embeddings":
		var value openai.EmbeddingRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.EmbeddingRequest = &value
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model}
		if !h.allowBatchRate(ctx, *req, estimateEmbeddingTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.EmbeddingProvider)
		if !ok {
			return 0, nil, errors.New("embeddings unsupported")
		}
		response, err := client.Embeddings(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/moderations":
		var value openai.ModerationRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.ModerationRequest = &value
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model}
		if !h.allowBatchRate(ctx, *req, openai.ModerationInputTokenCount(value.Input)) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.ModerationProvider)
		if !ok {
			return 0, nil, errors.New("moderations unsupported")
		}
		response, err := client.Moderations(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/rerank":
		var value openai.RerankRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.RerankRequest = &value
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model}
		if !h.allowBatchRate(ctx, *req, estimateRerankTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.RerankProvider)
		if !ok {
			return 0, nil, errors.New("rerank unsupported")
		}
		response, err := client.Rerank(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/search":
		var value openai.SearchRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		model, err := value.RoutingModel()
		if err != nil {
			return 0, nil, err
		}
		queries, err := value.Queries()
		if err != nil {
			return 0, nil, err
		}
		messages := make([]openai.Message, len(queries))
		for index, query := range queries {
			messages[index] = openai.Message{Role: "user", Content: query}
		}
		req.SearchRequest = &value
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: model, Messages: messages}
		if !h.allowBatchRate(ctx, *req, openai.SearchReserveTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.SearchProvider)
		if !ok {
			return 0, nil, errors.New("search unsupported")
		}
		response, err := client.Search(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/images/generations":
		var value openai.ImageGenerationRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.ImageGenerationRequest = &value
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model}
		if !h.allowBatchRate(ctx, *req, estimateImageGenerationTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.ImageGenerationProvider)
		if !ok {
			return 0, nil, errors.New("image generation unsupported")
		}
		response, err := client.GenerateImage(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/images/edits":
		var value openai.ImageEditRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.ImageEditRequest = &value
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model}
		if !h.allowBatchRate(ctx, *req, estimateImageEditTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.ImageEditProvider)
		if !ok {
			return 0, nil, errors.New("image edit unsupported")
		}
		response, err := client.EditImage(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/images/variations":
		var value openai.ImageVariationRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.ImageVariationRequest = &value
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model}
		if !h.allowBatchRate(ctx, *req, estimateImageVariationTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.ImageVariationProvider)
		if !ok {
			return 0, nil, errors.New("image variation unsupported")
		}
		response, err := client.CreateImageVariation(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/audio/speech":
		var value openai.AudioSpeechRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.AudioSpeechRequest = &value
		req.InputCharacters = value.InputCharacters()
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model, Messages: []openai.Message{{Role: "user", Content: value.Input}}}
		if !h.allowBatchRate(ctx, *req, estimateAudioSpeechTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.AudioSpeechProvider)
		if !ok {
			return 0, nil, errors.New("audio speech unsupported")
		}
		response, err := client.GenerateSpeech(ctx, *req)
		payload, _ := json.Marshal(openai.BatchAudioSpeechResponse{Data: response.Data, ContentType: response.ContentType, Model: response.Model, Usage: response.Usage})
		return http.StatusOK, payload, err
	case "/v1/audio/transcriptions", "/v1/audio/translations":
		var value openai.AudioTranscriptionRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.AudioTranscriptionRequest = &value
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model}
		if endpoint == "/v1/audio/translations" {
			req.Metadata["gateway.api_type"] = "audio_translation"
		}
		if !h.allowBatchRate(ctx, *req, estimateAudioTranscriptionTokens(value)) {
			return 0, nil, errBatchRateLimited
		}
		var response openai.AudioTranscriptionResponse
		var err error
		if endpoint == "/v1/audio/translations" {
			client, ok := h.provider.(provider.AudioTranslationProvider)
			if !ok {
				return 0, nil, errors.New("audio translation unsupported")
			}
			response, err = client.TranslateAudio(ctx, *req)
		} else {
			client, ok := h.provider.(provider.AudioTranscriptionProvider)
			if !ok {
				return 0, nil, errors.New("audio transcription unsupported")
			}
			response, err = client.TranscribeAudio(ctx, *req)
		}
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	case "/v1/ocr":
		var value openai.OCRRequest
		if err := json.Unmarshal(body, &value); err != nil {
			return 0, nil, err
		}
		req.OCRRequest = &value
		req.InputPages = value.ReservePages()
		req.Request = openai.ChatCompletionRequest{Provider: value.Provider, Model: value.Model}
		if !h.allowBatchRate(ctx, *req, value.InputTokens()) {
			return 0, nil, errBatchRateLimited
		}
		client, ok := h.provider.(provider.OCRProvider)
		if !ok {
			return 0, nil, errors.New("OCR unsupported")
		}
		response, err := client.OCR(ctx, *req)
		payload, _ := json.Marshal(response)
		return http.StatusOK, payload, err
	}
	return 0, nil, errors.New("unsupported endpoint")
}

var errBatchRateLimited = errors.New("batch item rate limited")

func (h Handler) allowBatchRate(ctx context.Context, req modules.RequestContext, tokens int) bool {
	key := req.CredentialID
	if req.TeamID != "" {
		key = "team:" + req.TeamID + ":credential:" + req.CredentialID
	}
	allowed, _, err := h.rateLimits.Allow(ctx, key, RateLimit{Requests: req.RateLimitRPM, Tokens: req.RateLimitTPM}, tokens)
	return err == nil && allowed
}
func batchErrorResult(item batchstate.Item, code, message string) []byte {
	payload, _ := json.Marshal(openai.BatchOutputLine{ID: "batch_req_" + item.ExecutionID, CustomID: item.CustomID, Error: &openai.BatchOutputLineError{Code: code, Message: message}})
	return payload
}
func (h Handler) retryBatchJob(ctx context.Context, job asyncstate.Job, cause error) error {
	retryErr := h.batchJobs.RetryAsyncJob(ctx, job.Kind, job.ResourceID, job.LeaseGeneration, backgroundRetry(job.Attempts))
	return errors.Join(cause, retryErr)
}
func backgroundRetry(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 6)
	return min(delay, time.Minute)
}

func (h Handler) finalizeBatch(ctx context.Context, batch batchstate.Batch) (batchstate.Batch, error) {
	completed, err := h.batches.ListBatchResults(ctx, batch.OwnerKey, batch.ID, false)
	if err != nil {
		return batchstate.Batch{}, err
	}
	failed, err := h.batches.ListBatchResults(ctx, batch.OwnerKey, batch.ID, true)
	if err != nil {
		return batchstate.Batch{}, err
	}
	outputID, err := h.storeBatchResults(ctx, batch, "output", completed)
	if err != nil {
		return batchstate.Batch{}, err
	}
	errorID, err := h.storeBatchResults(ctx, batch, "errors", failed)
	if err != nil {
		return batchstate.Batch{}, err
	}
	return h.batches.FinalizeBatch(ctx, batch.OwnerKey, batch.ID, outputID, errorID)
}
func (h Handler) storeBatchResults(ctx context.Context, batch batchstate.Batch, kind string, items []batchstate.Item) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	limit := h.batchResultLimit()
	var content bytes.Buffer
	for _, item := range items {
		if content.Len()+len(item.Result)+1 > limit {
			return "", errors.New("batch result exceeds storage limit")
		}
		content.Write(item.Result)
		content.WriteByte('\n')
	}
	id := "file_" + strings.TrimPrefix(batch.ID, "batch_") + "_" + kind
	file := filestate.File{ID: id, OwnerKey: batch.OwnerKey, Filename: batch.ID + "_" + kind + ".jsonl", Purpose: "batch_output", ContentType: "application/jsonl", Bytes: int64(content.Len()), Content: content.Bytes(), ExpiresAfterSeconds: batch.OutputExpirySeconds}
	_, err := h.files.Create(ctx, file, h.fileConfig.OwnerQuotaBytes)
	if errors.Is(err, filestate.ErrConflict) {
		existing, getErr := h.files.Get(ctx, batch.OwnerKey, id, true)
		if getErr == nil && bytes.Equal(existing.Content, file.Content) {
			return id, nil
		}
		return "", errors.New("batch output file conflict")
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func (h Handler) batchResultLimit() int {
	limit := batchResultMaxSize
	if h.fileConfig.MaxBytes < int64(limit) {
		limit = int(h.fileConfig.MaxBytes)
	}
	return limit
}
func RunBatchWorker(ctx context.Context, handler Handler) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := handler.ProcessBatchItems(ctx); err != nil && ctx.Err() == nil {
			log.Printf("batch processing failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
