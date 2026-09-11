package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/finetunestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

const fineTuningOwnerQuota = 1000
const fineTuningSettlementJobKind = "fine_tuning.settlement.v1"
const fineTuningSettlementBatchSize = 10
const fineTuningSettlementLease = time.Minute

type fineTuningSettlementJob struct {
	RequestID       string            `json:"request_id"`
	SessionID       string            `json:"session_id,omitempty"`
	CredentialID    string            `json:"credential_id"`
	CredentialAlias string            `json:"credential_alias,omitempty"`
	UserID          string            `json:"user_id,omitempty"`
	TeamID          string            `json:"team_id,omitempty"`
	OrganizationID  string            `json:"organization_id,omitempty"`
	Roles           []string          `json:"roles,omitempty"`
	Tags            []string          `json:"tags,omitempty"`
	Provider        string            `json:"provider,omitempty"`
	Model           string            `json:"model"`
	TrainingTokens  int               `json:"training_tokens"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

func (h Handler) WithFineTuningStore(store finetunestate.Store) Handler {
	h.fineTuning = store
	if jobs, ok := store.(asyncstate.Store); ok {
		h.fineTuningJobs = jobs
	}
	return h
}

func (h Handler) CreateFineTuningJob(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	var input openai.FineTuningCreateRequest
	if !decodeInferenceRequest(w, r, &input) || !validateFineTuningCreate(w, input) {
		return
	}
	identity, ok := h.authorizeOwnedStorageOperation(w, r, "fine_tuning")
	if !ok {
		return
	}
	if h.fineTuning == nil || h.files == nil {
		writeError(w, http.StatusServiceUnavailable, "fine_tuning_unavailable", "fine-tuning storage is unavailable")
		return
	}
	if h.resourceBillingPipeline().HasModule("billing") && h.fineTuningJobs == nil {
		writeError(w, http.StatusServiceUnavailable, "fine_tuning_unavailable", "durable fine-tuning settlement storage is unavailable")
		return
	}
	if !h.authorizeBatchModel(w, identity, input.Model) {
		return
	}
	owner := fileOwnerKey(identity)
	trainingFile, ok := h.fineTuningFile(w, r, owner, input.TrainingFile, true)
	if !ok {
		return
	}
	if input.ValidationFile != "" {
		if _, ok := h.fineTuningFile(w, r, owner, input.ValidationFile, false); !ok {
			return
		}
	}
	runtime, ok := h.provider.(provider.FineTuningProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "fine-tuning is not supported")
		return
	}
	trainingTokens, err := fineTuningTrainingTokenEstimate(trainingFile.Content, input.Method)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var billingRequest modules.RequestContext
	billingReserved := false
	billingErr := error(nil)
	job, binding, err := runtime.CreateFineTuningJob(r.Context(), identity, input, func(ctx context.Context, request *modules.RequestContext) error {
		billingRequest = *request
		billingRequest.TrainingTokens = trainingTokens
		billingErr = h.resourceBillingPipeline().RunBillingLifecycle(ctx, &billingRequest, "reserve", nil)
		billingReserved = billingErr == nil && h.resourceBillingPipeline().HasModule("billing")
		return billingErr
	})
	if err != nil {
		if billingReserved {
			_ = h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &billingRequest, "cancel", err)
		}
		if billingErr != nil {
			writeFineTuningBillingFailure(w, billingErr)
			return
		}
		writeProviderFailure(w, err)
		return
	}
	record := finetunestate.Record{OwnerKey: owner, Binding: binding, Job: job}
	created, err := h.createFineTuningRecord(r.Context(), record, billingRequest, billingReserved)
	if err != nil {
		compensation, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		_, _ = runtime.CancelFineTuningJob(compensation, binding, job.ID)
		cancel()
		if billingReserved {
			_ = h.resourceBillingPipeline().RunBillingLifecycle(r.Context(), &billingRequest, "cancel", err)
		}
		writeFineTuningStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, created.Job)
}

func validateFineTuningCreate(w http.ResponseWriter, input openai.FineTuningCreateRequest) bool {
	if input.Model == "" || len(input.Model) > 256 || !validFileToken(input.TrainingFile, 128) || (input.ValidationFile != "" && !validFileToken(input.ValidationFile, 128)) || len(input.Suffix) > 18 || len(input.Metadata) > 16 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid fine-tuning request")
		return false
	}
	for k, v := range input.Metadata {
		if k == "" || len(k) > 64 || len(v) > 512 {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid fine-tuning metadata")
			return false
		}
	}
	if len(input.Method) > 0 {
		trim := bytes.TrimSpace(input.Method)
		if len(trim) > 64<<10 || len(trim) < 2 || trim[0] != '{' {
			writeError(w, http.StatusBadRequest, "invalid_request", "method must be a bounded object")
			return false
		}
	}
	return true
}
func (h Handler) fineTuningFile(w http.ResponseWriter, r *http.Request, owner, id string, includeContent bool) (filestate.File, bool) {
	file, err := h.files.Get(r.Context(), owner, id, includeContent)
	if err != nil {
		writeFineTuningStoreError(w, err)
		return filestate.File{}, false
	}
	if file.Purpose != "fine-tune" {
		writeError(w, http.StatusBadRequest, "invalid_request", "training and validation files must have purpose=fine-tune")
		return filestate.File{}, false
	}
	return file, true
}

func fineTuningTrainingTokenEstimate(content []byte, method json.RawMessage) (int, error) {
	tokens := openai.EstimateContextTokens(string(content))
	epochs := 1
	if len(method) > 0 {
		var value struct {
			Supervised struct {
				Hyperparameters struct {
					NEpochs json.RawMessage `json:"n_epochs"`
				} `json:"hyperparameters"`
			} `json:"supervised"`
		}
		if json.Unmarshal(method, &value) != nil {
			return 0, errors.New("method must be valid JSON")
		}
		if raw := value.Supervised.Hyperparameters.NEpochs; len(raw) > 0 && string(raw) != `"auto"` {
			if json.Unmarshal(raw, &epochs) != nil || epochs < 1 || epochs > 50 {
				return 0, errors.New("n_epochs must be auto or an integer between 1 and 50")
			}
		}
	}
	if tokens > 1_000_000_000/epochs {
		return 0, errors.New("training data token estimate exceeds the supported billing range")
	}
	return tokens * epochs, nil
}

func newFineTuningSettlementJob(request modules.RequestContext) fineTuningSettlementJob {
	return fineTuningSettlementJob{
		RequestID: request.RequestID, SessionID: request.SessionID, CredentialID: request.CredentialID,
		CredentialAlias: request.CredentialAlias, UserID: request.UserID, TeamID: request.TeamID,
		OrganizationID: request.OrganizationID, Roles: append([]string(nil), request.Roles...),
		Tags: append([]string(nil), request.Tags...), Provider: request.Request.Provider,
		Model: request.Request.Model, TrainingTokens: request.TrainingTokens,
		Metadata: fineTuningSettlementMetadata(request.Metadata),
	}
}

func fineTuningSettlementMetadata(metadata map[string]string) map[string]string {
	result := make(map[string]string)
	for key, value := range metadata {
		if key == "gateway.api_type" || key == "gateway.fine_tuning_usage_exact" || strings.HasPrefix(key, "provider.") || strings.HasPrefix(key, "model_catalog.") || strings.HasPrefix(key, "billing.") {
			result[key] = value
		}
	}
	return result
}

func (job fineTuningSettlementJob) requestContext() modules.RequestContext {
	return modules.RequestContext{
		RequestID: job.RequestID, SessionID: job.SessionID, CredentialID: job.CredentialID,
		CredentialAlias: job.CredentialAlias, UserID: job.UserID, TeamID: job.TeamID,
		OrganizationID: job.OrganizationID, Roles: append([]string(nil), job.Roles...),
		Tags: append([]string(nil), job.Tags...), TrainingTokens: job.TrainingTokens,
		Request: openai.ChatCompletionRequest{Provider: job.Provider, Model: job.Model}, Metadata: fineTuningSettlementMetadata(job.Metadata),
	}
}

func (h Handler) createFineTuningRecord(ctx context.Context, record finetunestate.Record, request modules.RequestContext, reserved bool) (finetunestate.Record, error) {
	if !reserved {
		return h.fineTuning.CreateFineTuningRecord(ctx, record, fineTuningOwnerQuota)
	}
	outbox, ok := h.fineTuning.(finetunestate.AtomicOutboxStore)
	if !ok || h.fineTuningJobs == nil {
		return finetunestate.Record{}, finetunestate.ErrUnavailable
	}
	payload, err := json.Marshal(newFineTuningSettlementJob(request))
	if err != nil {
		return finetunestate.Record{}, finetunestate.ErrInvalid
	}
	job := asyncstate.Job{Kind: fineTuningSettlementJobKind, ResourceID: record.Job.ID, OwnerKey: record.OwnerKey, EndpointID: record.Binding.Endpoint, ExecutionID: request.RequestID, Payload: payload}
	return outbox.CreateFineTuningRecordWithJob(ctx, record, fineTuningOwnerQuota, job)
}

func (h Handler) ProcessFineTuningSettlements(ctx context.Context) (int, error) {
	if h.fineTuningJobs == nil || h.fineTuning == nil {
		return 0, finetunestate.ErrUnavailable
	}
	jobs, err := h.fineTuningJobs.ClaimAsyncJobs(ctx, fineTuningSettlementJobKind, fineTuningSettlementBatchSize, fineTuningSettlementLease)
	if err != nil {
		return 0, err
	}
	var failures []error
	for _, job := range jobs {
		if err := h.processFineTuningSettlement(ctx, job); err != nil {
			failures = append(failures, fmt.Errorf("fine-tuning %s: %w", job.ResourceID, err))
		}
	}
	return len(jobs), errors.Join(failures...)
}

func (h Handler) processFineTuningSettlement(ctx context.Context, claimed asyncstate.Job) error {
	var job fineTuningSettlementJob
	if json.Unmarshal(claimed.Payload, &job) != nil || job.RequestID != claimed.ExecutionID || job.CredentialID == "" || job.Model == "" || job.TrainingTokens < 1 {
		return h.retryFineTuningSettlement(ctx, claimed, errors.New("invalid durable settlement payload"))
	}
	record, err := h.fineTuning.GetFineTuningRecord(ctx, claimed.OwnerKey, claimed.ResourceID)
	if err != nil {
		return h.retryFineTuningSettlement(ctx, claimed, err)
	}
	runtime, ok := h.provider.(provider.FineTuningProvider)
	if !ok {
		return h.retryFineTuningSettlement(ctx, claimed, errors.New("fine-tuning provider is unavailable"))
	}
	current, err := runtime.RetrieveFineTuningJob(ctx, record.Binding, record.Job.ID)
	if err != nil {
		return h.retryFineTuningSettlement(ctx, claimed, err)
	}
	if !fineTuningTerminal(current.Status) {
		if _, err := h.fineTuning.UpdateFineTuningRecord(ctx, claimed.OwnerKey, current); err != nil {
			return h.retryFineTuningSettlement(ctx, claimed, err)
		}
		return h.retryFineTuningSettlement(ctx, claimed, nil)
	}
	request := job.requestContext()
	if current.Status == "succeeded" {
		if current.TrainedTokens != nil && *current.TrainedTokens > 0 && *current.TrainedTokens <= 1_000_000_000 {
			request.TrainingTokens = int(*current.TrainedTokens)
			if request.Metadata == nil {
				request.Metadata = map[string]string{}
			}
			request.Metadata["gateway.fine_tuning_usage_exact"] = "true"
		}
		if err := h.resourceBillingPipeline().RunBillingLifecycle(ctx, &request, "commit", nil); err != nil {
			return h.retryFineTuningSettlement(ctx, claimed, err)
		}
	} else {
		if err := h.resourceBillingPipeline().RunBillingLifecycle(ctx, &request, "cancel", errors.New("fine-tuning job "+current.Status)); err != nil {
			return h.retryFineTuningSettlement(ctx, claimed, err)
		}
	}
	if _, err := h.fineTuning.UpdateFineTuningRecord(ctx, claimed.OwnerKey, current); err != nil {
		return h.retryFineTuningSettlement(ctx, claimed, err)
	}
	return h.fineTuningJobs.CompleteAsyncJob(ctx, claimed.Kind, claimed.ResourceID, claimed.LeaseGeneration)
}

func fineTuningTerminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

func (h Handler) fineTuningSettlementPending(ctx context.Context, owner, id string) (bool, error) {
	if h.fineTuningJobs == nil {
		return false, nil
	}
	return h.fineTuningJobs.HasAsyncJob(ctx, fineTuningSettlementJobKind, id, owner)
}

func (h Handler) retryFineTuningSettlement(ctx context.Context, job asyncstate.Job, cause error) error {
	retryErr := h.fineTuningJobs.RetryAsyncJob(ctx, job.Kind, job.ResourceID, job.LeaseGeneration, videoSettlementRetry(job.Attempts))
	return errors.Join(cause, retryErr)
}

func RunFineTuningSettlementWorker(ctx context.Context, handler Handler) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := handler.ProcessFineTuningSettlements(ctx); err != nil && ctx.Err() == nil {
			log.Printf("fine-tuning settlement processing failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func writeFineTuningBillingFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, modules.ErrBudgetExceeded) {
		writeError(w, http.StatusTooManyRequests, "budget_exceeded", "budget exceeded")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "billing_unavailable", "billing is unavailable")
}

func (h Handler) ListFineTuningJobs(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.fineTuningOwner(w, r)
	if !ok {
		return
	}
	options, ok := fineTuningOptions(w, r)
	if !ok {
		return
	}
	records, next, err := h.fineTuning.ListFineTuningRecords(r.Context(), owner, options.Limit, options.After)
	if err != nil {
		writeFineTuningStoreError(w, err)
		return
	}
	data := make([]openai.FineTuningJob, len(records))
	for i := range records {
		data[i] = records[i].Job
	}
	writeJSON(w, http.StatusOK, openai.FineTuningJobList{Object: "list", Data: data, HasMore: next != ""})
}
func (h Handler) GetFineTuningJob(w http.ResponseWriter, r *http.Request) {
	h.fineTuningJobAction(w, r, "retrieve")
}
func (h Handler) CancelFineTuningJob(w http.ResponseWriter, r *http.Request) {
	h.fineTuningJobAction(w, r, "cancel")
}
func (h Handler) PauseFineTuningJob(w http.ResponseWriter, r *http.Request) {
	h.fineTuningJobAction(w, r, "pause")
}
func (h Handler) ResumeFineTuningJob(w http.ResponseWriter, r *http.Request) {
	h.fineTuningJobAction(w, r, "resume")
}
func (h Handler) fineTuningJobAction(w http.ResponseWriter, r *http.Request, action string) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	owner, ok := h.fineTuningOwner(w, r)
	if !ok {
		return
	}
	record, ok := h.fineTuningRecord(w, r, owner)
	if !ok {
		return
	}
	runtime := h.provider.(provider.FineTuningProvider)
	var job openai.FineTuningJob
	var err error
	switch action {
	case "retrieve":
		job, err = runtime.RetrieveFineTuningJob(r.Context(), record.Binding, record.Job.ID)
	case "cancel":
		job, err = runtime.CancelFineTuningJob(r.Context(), record.Binding, record.Job.ID)
	case "pause":
		job, err = runtime.PauseFineTuningJob(r.Context(), record.Binding, record.Job.ID)
	case "resume":
		job, err = runtime.ResumeFineTuningJob(r.Context(), record.Binding, record.Job.ID)
	}
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	if fineTuningTerminal(job.Status) {
		pending, pendingErr := h.fineTuningSettlementPending(r.Context(), owner, job.ID)
		if pendingErr != nil {
			writeFineTuningStoreError(w, pendingErr)
			return
		}
		if pending {
			writeJSON(w, http.StatusOK, record.Job)
			return
		}
	}
	updated, err := h.fineTuning.UpdateFineTuningRecord(r.Context(), owner, job)
	if err != nil {
		writeFineTuningStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated.Job)
}

func (h Handler) ListFineTuningEvents(w http.ResponseWriter, r *http.Request) {
	h.fineTuningSubresources(w, r, false)
}
func (h Handler) ListFineTuningCheckpoints(w http.ResponseWriter, r *http.Request) {
	h.fineTuningSubresources(w, r, true)
}

func (h Handler) DeleteFineTunedModel(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	owner, ok := h.fineTuningOwner(w, r)
	if !ok {
		return
	}
	model := r.PathValue("model")
	if !validFineTunedModelID(model) {
		writeError(w, http.StatusBadRequest, "invalid_request", "fine-tuned model ID is invalid")
		return
	}
	record, err := h.fineTuning.FindFineTuningRecordByModel(r.Context(), owner, model)
	if err != nil {
		writeFineTuningStoreError(w, err)
		return
	}
	deleted, err := h.provider.(provider.FineTuningProvider).DeleteFineTunedModel(r.Context(), record.Binding, model)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deleted)
}

func validFineTunedModelID(model string) bool {
	if len(model) == 0 || len(model) > 256 {
		return false
	}
	for _, c := range model {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}

func (h Handler) fineTuningSubresources(w http.ResponseWriter, r *http.Request, checkpoints bool) {
	owner, ok := h.fineTuningOwner(w, r)
	if !ok {
		return
	}
	record, ok := h.fineTuningRecord(w, r, owner)
	if !ok {
		return
	}
	options, ok := fineTuningOptions(w, r)
	if !ok {
		return
	}
	runtime := h.provider.(provider.FineTuningProvider)
	if checkpoints {
		result, err := runtime.ListFineTuningCheckpoints(r.Context(), record.Binding, record.Job.ID, options)
		if err != nil {
			writeProviderFailure(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	result, err := runtime.ListFineTuningEvents(r.Context(), record.Binding, record.Job.ID, options)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h Handler) fineTuningOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	identity, ok := h.authorizeOwnedStorageOperation(w, r, "fine_tuning")
	if !ok {
		return "", false
	}
	if h.fineTuning == nil {
		writeError(w, http.StatusServiceUnavailable, "fine_tuning_unavailable", "fine-tuning storage is unavailable")
		return "", false
	}
	if h.resourceBillingPipeline().HasModule("billing") && h.fineTuningJobs == nil {
		writeError(w, http.StatusServiceUnavailable, "fine_tuning_unavailable", "durable fine-tuning settlement storage is unavailable")
		return "", false
	}
	if _, ok := h.provider.(provider.FineTuningProvider); !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "fine-tuning is not supported")
		return "", false
	}
	return fileOwnerKey(identity), true
}
func (h Handler) fineTuningRecord(w http.ResponseWriter, r *http.Request, owner string) (finetunestate.Record, bool) {
	id := r.PathValue("id")
	if !validFileToken(id, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "fine-tuning job ID is invalid")
		return finetunestate.Record{}, false
	}
	record, err := h.fineTuning.GetFineTuningRecord(r.Context(), owner, id)
	if err != nil {
		writeFineTuningStoreError(w, err)
		return finetunestate.Record{}, false
	}
	return record, true
}
func fineTuningOptions(w http.ResponseWriter, r *http.Request) (provider.FineTuningListOptions, bool) {
	q := r.URL.Query()
	for k, v := range q {
		if (k != "after" && k != "limit") || len(v) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+k)
			return provider.FineTuningListOptions{}, false
		}
	}
	o := provider.FineTuningListOptions{After: q.Get("after"), Limit: 20}
	if o.After != "" && !validFileToken(o.After, 256) {
		writeError(w, http.StatusBadRequest, "invalid_request", "after is invalid")
		return o, false
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return o, false
		}
		o.Limit = n
	}
	return o, true
}
func writeFineTuningStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, finetunestate.ErrNotFound), errors.Is(err, filestate.ErrNotFound):
		writeError(w, http.StatusNotFound, "fine_tuning_job_not_found", "fine-tuning job or file not found")
	case errors.Is(err, finetunestate.ErrQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "fine_tuning_job_limit_exceeded", "too many fine-tuning jobs")
	case errors.Is(err, finetunestate.ErrConflict):
		writeError(w, http.StatusConflict, "fine_tuning_job_conflict", "fine-tuning job already exists")
	default:
		writeError(w, http.StatusServiceUnavailable, "fine_tuning_unavailable", "fine-tuning storage is unavailable")
	}
}
