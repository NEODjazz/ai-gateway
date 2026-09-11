package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/finetunestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

const fineTuningOwnerQuota = 1000

func (h Handler) WithFineTuningStore(store finetunestate.Store) Handler {
	h.fineTuning = store
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
		billingErr = h.pipeline.RunBillingLifecycle(ctx, &billingRequest, "reserve", nil)
		billingReserved = billingErr == nil && h.pipeline.HasModule("billing")
		return billingErr
	})
	if err != nil {
		if billingReserved {
			_ = h.pipeline.RunBillingLifecycle(r.Context(), &billingRequest, "cancel", err)
		}
		if billingErr != nil {
			writeFineTuningBillingFailure(w, billingErr)
			return
		}
		writeProviderFailure(w, err)
		return
	}
	created, err := h.fineTuning.CreateFineTuningRecord(r.Context(), finetunestate.Record{OwnerKey: owner, Binding: binding, Job: job}, fineTuningOwnerQuota)
	if err != nil {
		compensation, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		_, _ = runtime.CancelFineTuningJob(compensation, binding, job.ID)
		cancel()
		if billingReserved {
			_ = h.pipeline.RunBillingLifecycle(r.Context(), &billingRequest, "cancel", err)
		}
		writeFineTuningStoreError(w, err)
		return
	}
	if billingReserved {
		if err = h.pipeline.RunBillingLifecycle(r.Context(), &billingRequest, "commit", nil); err != nil {
			compensation, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
			cancelled, cancelErr := runtime.CancelFineTuningJob(compensation, binding, job.ID)
			cancel()
			if cancelErr == nil {
				_, _ = h.fineTuning.UpdateFineTuningRecord(r.Context(), owner, cancelled)
			}
			_ = h.pipeline.RunBillingLifecycle(r.Context(), &billingRequest, "cancel", err)
			writeFineTuningBillingFailure(w, err)
			return
		}
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
