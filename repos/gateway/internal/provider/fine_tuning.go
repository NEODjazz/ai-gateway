package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"ai-gateway-gateway/internal/openai"
)

type FineTuningListOptions struct {
	After string
	Limit int
}

type FineTuningClient interface {
	CreateFineTuningJob(context.Context, openai.FineTuningCreateRequest) (openai.FineTuningJob, error)
	ListFineTuningJobs(context.Context, FineTuningListOptions) (openai.FineTuningJobList, error)
	RetrieveFineTuningJob(context.Context, string) (openai.FineTuningJob, error)
	CancelFineTuningJob(context.Context, string) (openai.FineTuningJob, error)
	PauseFineTuningJob(context.Context, string) (openai.FineTuningJob, error)
	ResumeFineTuningJob(context.Context, string) (openai.FineTuningJob, error)
	ListFineTuningEvents(context.Context, string, FineTuningListOptions) (openai.FineTuningEventList, error)
	ListFineTuningCheckpoints(context.Context, string, FineTuningListOptions) (openai.FineTuningCheckpointList, error)
	DeleteFineTunedModel(context.Context, string) (openai.ModelDeletion, error)
}

func (p OpenAICompatible) CreateFineTuningJob(ctx context.Context, input openai.FineTuningCreateRequest) (openai.FineTuningJob, error) {
	var result openai.FineTuningJob
	err := p.fineTuningRequest(ctx, http.MethodPost, "fine_tuning/jobs", nil, input, &result)
	return result, err
}

func (p OpenAICompatible) ListFineTuningJobs(ctx context.Context, options FineTuningListOptions) (openai.FineTuningJobList, error) {
	var result openai.FineTuningJobList
	query, err := fineTuningQuery(options)
	if err != nil {
		return result, err
	}
	err = p.fineTuningRequest(ctx, http.MethodGet, "fine_tuning/jobs", query, nil, &result)
	if err == nil {
		err = validateFineTuningJobPage(result)
	}
	return result, err
}

func (p OpenAICompatible) RetrieveFineTuningJob(ctx context.Context, id string) (openai.FineTuningJob, error) {
	return p.fineTuningJobAction(ctx, http.MethodGet, id, "")
}

func (p OpenAICompatible) CancelFineTuningJob(ctx context.Context, id string) (openai.FineTuningJob, error) {
	return p.fineTuningJobAction(ctx, http.MethodPost, id, "cancel")
}

func (p OpenAICompatible) PauseFineTuningJob(ctx context.Context, id string) (openai.FineTuningJob, error) {
	return p.fineTuningJobAction(ctx, http.MethodPost, id, "pause")
}

func (p OpenAICompatible) ResumeFineTuningJob(ctx context.Context, id string) (openai.FineTuningJob, error) {
	return p.fineTuningJobAction(ctx, http.MethodPost, id, "resume")
}

func (p OpenAICompatible) ListFineTuningEvents(ctx context.Context, id string, options FineTuningListOptions) (openai.FineTuningEventList, error) {
	var result openai.FineTuningEventList
	query, err := fineTuningResourceQuery(id, options)
	if err != nil {
		return result, err
	}
	err = p.fineTuningRequest(ctx, http.MethodGet, fineTuningJobPath(id, "events"), query, nil, &result)
	if err == nil {
		err = validateFineTuningEventPage(result)
	}
	return result, err
}

func (p OpenAICompatible) ListFineTuningCheckpoints(ctx context.Context, id string, options FineTuningListOptions) (openai.FineTuningCheckpointList, error) {
	var result openai.FineTuningCheckpointList
	query, err := fineTuningResourceQuery(id, options)
	if err != nil {
		return result, err
	}
	err = p.fineTuningRequest(ctx, http.MethodGet, fineTuningJobPath(id, "checkpoints"), query, nil, &result)
	if err == nil {
		err = validateFineTuningCheckpointPage(result, id)
	}
	return result, err
}

func (p OpenAICompatible) DeleteFineTunedModel(ctx context.Context, model string) (openai.ModelDeletion, error) {
	var result openai.ModelDeletion
	if !validFineTunedModelID(model) {
		return result, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "model", Err: errors.New("invalid fine-tuned model ID")}
	}
	err := p.fineTuningRequest(ctx, http.MethodDelete, "models/"+url.PathEscape(model), nil, nil, &result)
	if err == nil && (result.ID != model || result.Object != "model" || !result.Deleted) {
		err = errors.New("invalid upstream model deletion")
	}
	return result, err
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

func (p OpenAICompatible) fineTuningJobAction(ctx context.Context, method, id, action string) (openai.FineTuningJob, error) {
	var result openai.FineTuningJob
	if !validResponseResourceID(id) {
		return result, &Error{Class: FailureClientRequest, StatusCode: http.StatusBadRequest, UpstreamCode: "invalid_request", Param: "fine_tuning_job_id", Err: errors.New("invalid fine-tuning job ID")}
	}
	err := p.fineTuningRequest(ctx, method, fineTuningJobPath(id, action), nil, nil, &result)
	return result, err
}

func fineTuningJobPath(id, suffix string) string {
	path := "fine_tuning/jobs/" + id
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}

func fineTuningResourceQuery(id string, options FineTuningListOptions) (url.Values, error) {
	if !validResponseResourceID(id) {
		return nil, errors.New("invalid fine-tuning job ID")
	}
	return fineTuningQuery(options)
}

func fineTuningQuery(options FineTuningListOptions) (url.Values, error) {
	if options.After != "" && !validResponseResourceQueryToken(options.After) || options.Limit < 0 || options.Limit > 100 {
		return nil, errors.New("invalid fine-tuning pagination")
	}
	query := url.Values{}
	if options.After != "" {
		query.Set("after", options.After)
	}
	if options.Limit > 0 {
		query.Set("limit", strconv.Itoa(options.Limit))
	}
	return query, nil
}

func (p OpenAICompatible) fineTuningRequest(ctx context.Context, method, path string, query url.Values, input, output any) error {
	var body io.Reader = http.NoBody
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	requestURL := providerURL(p.baseURL, path)
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseStatusError(p.providerName(), response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(payload) > 8<<20 {
		return errors.New("upstream fine-tuning response exceeds 8 MiB")
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return err
	}
	return validateFineTuningOutput(output)
}

func validateFineTuningOutput(output any) error {
	switch value := output.(type) {
	case *openai.FineTuningJob:
		return validateFineTuningJob(*value)
	}
	return nil
}

func validateFineTuningJob(job openai.FineTuningJob) error {
	if job.ID == "" || job.Object != "fine_tuning.job" || job.Model == "" || job.TrainingFile == "" || !validFineTuningStatus(job.Status) {
		return errors.New("invalid upstream fine-tuning job")
	}
	return nil
}

func validateFineTuningJobPage(page openai.FineTuningJobList) error {
	if page.Object != "list" || len(page.Data) > 1000 {
		return errors.New("invalid upstream fine-tuning page")
	}
	for _, job := range page.Data {
		if err := validateFineTuningJob(job); err != nil {
			return err
		}
	}
	return nil
}

func validateFineTuningEventPage(page openai.FineTuningEventList) error {
	if page.Object != "list" || len(page.Data) > 1000 {
		return errors.New("invalid upstream fine-tuning event page")
	}
	for _, event := range page.Data {
		if event.ID == "" || event.Object != "fine_tuning.job.event" || event.Message == "" {
			return errors.New("invalid upstream fine-tuning event")
		}
	}
	return nil
}

func validateFineTuningCheckpointPage(page openai.FineTuningCheckpointList, jobID string) error {
	if page.Object != "list" || len(page.Data) > 1000 {
		return errors.New("invalid upstream fine-tuning checkpoint page")
	}
	for _, checkpoint := range page.Data {
		if checkpoint.ID == "" || checkpoint.Object != "fine_tuning.job.checkpoint" || checkpoint.FineTuningJobID != jobID || checkpoint.FineTunedModel == "" || checkpoint.StepNumber < 0 {
			return errors.New("invalid upstream fine-tuning checkpoint")
		}
	}
	return nil
}

func validFineTuningStatus(status string) bool {
	switch status {
	case "validating_files", "queued", "running", "succeeded", "failed", "cancelled", "paused":
		return true
	default:
		return false
	}
}
