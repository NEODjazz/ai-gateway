package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

const backgroundResponseJobKind = "responses.background.v1"
const backgroundResponseLease = time.Minute
const backgroundResponseBatchSize = 10

var ErrBackgroundResponseStorageUnavailable = errors.New("background response storage is unavailable")
var ErrBackgroundResponsesUnsupported = errors.New("background responses are not supported by the selected deployment")

// BackgroundResponseProcessor is run by the application lifecycle. Each call
// claims a bounded batch so multiple gateway replicas can safely share work.
type BackgroundResponseProcessor interface {
	ProcessBackgroundResponses(context.Context) (int, error)
}

type BackgroundResponseSettlementProvider interface {
	BackgroundResponseSettled(context.Context, modules.RequestContext, string) (bool, error)
}

type backgroundResponseJob struct {
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
	Metadata        map[string]string `json:"metadata,omitempty"`
}

func backgroundResponsePending(response openai.ResponseResponse) bool {
	return response.Status == "queued" || response.Status == "in_progress"
}

func backgroundResponseOwner(req modules.RequestContext) string {
	if req.CredentialID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(req.CredentialID + "\x00" + req.UserID))
	return hex.EncodeToString(sum[:])
}

func backgroundJobMetadata(metadata map[string]string) map[string]string {
	result := make(map[string]string)
	for key, value := range metadata {
		if key == "gateway.api_type" || strings.HasPrefix(key, "provider.") || strings.HasPrefix(key, "model_catalog.") || strings.HasPrefix(key, "billing.") {
			result[key] = value
		}
	}
	return result
}

func newBackgroundResponseJob(req modules.RequestContext) backgroundResponseJob {
	request := req.ResponseRequest
	job := backgroundResponseJob{
		RequestID: req.RequestID, SessionID: req.SessionID, CredentialID: req.CredentialID,
		CredentialAlias: req.CredentialAlias, UserID: req.UserID, TeamID: req.TeamID,
		OrganizationID: req.OrganizationID, Roles: append([]string(nil), req.Roles...),
		Tags: append([]string(nil), req.Tags...), Metadata: backgroundJobMetadata(req.Metadata),
	}
	if request != nil {
		job.Provider = request.Provider
		job.Model = request.Model
	}
	return job
}

func (job backgroundResponseJob) requestContext() modules.RequestContext {
	store := true
	request := openai.ResponseRequest{Provider: job.Provider, Model: job.Model, Store: &store, Background: true}
	return modules.RequestContext{
		RequestID: job.RequestID, SessionID: job.SessionID, CredentialID: job.CredentialID,
		CredentialAlias: job.CredentialAlias, UserID: job.UserID, TeamID: job.TeamID,
		OrganizationID: job.OrganizationID, Roles: append([]string(nil), job.Roles...),
		Tags: append([]string(nil), job.Tags...), Request: openai.ChatCompletionRequest{Provider: job.Provider, Model: job.Model},
		ResponseRequest: &request, Metadata: backgroundJobMetadata(job.Metadata),
	}
}

func (r Router) enqueueBackgroundResponse(ctx context.Context, req modules.RequestContext, response openai.ResponseResponse, endpoint Endpoint) error {
	if interfaceIsNil(r.asyncJobs) {
		return ErrBackgroundResponseStorageUnavailable
	}
	job := newBackgroundResponseJob(req)
	payload, err := json.Marshal(job)
	if err != nil {
		return ErrBackgroundResponseStorageUnavailable
	}
	_, err = r.asyncJobs.EnqueueAsyncJob(ctx, asyncstate.Job{
		Kind: backgroundResponseJobKind, ResourceID: response.ID, OwnerKey: backgroundResponseOwner(req),
		EndpointID: endpoint.Name, ExecutionID: req.RequestID, Payload: payload,
	})
	if err != nil {
		return errors.Join(ErrBackgroundResponseStorageUnavailable, err)
	}
	return nil
}

func (r Router) BackgroundResponseSettled(ctx context.Context, req modules.RequestContext, responseID string) (bool, error) {
	if interfaceIsNil(r.asyncJobs) {
		return false, ErrBackgroundResponseStorageUnavailable
	}
	pending, err := r.asyncJobs.HasAsyncJob(ctx, backgroundResponseJobKind, responseID, backgroundResponseOwner(req))
	if err != nil {
		return false, errors.Join(ErrBackgroundResponseStorageUnavailable, err)
	}
	return !pending, nil
}

func (r Router) compensateBackgroundResponse(ctx context.Context, req modules.RequestContext, responseID, model string, endpoint Endpoint) {
	if client, ok := endpoint.Provider.(responseCancelClient); ok {
		_, _ = callResponseLifecycle(r, ctx, endpoint, "responses.cancel", func(callCtx context.Context) (openai.ResponseResponse, error) {
			return client.CancelResponse(callCtx, responseID)
		})
	}
	binding := responseOwnership{Endpoint: endpoint.Name, Model: model, Deployment: responseDeploymentIdentity(endpoint), Resource: "response"}
	_ = r.ownership.remove(ctx, req, responseID, binding)
}

func (r Router) ProcessBackgroundResponses(ctx context.Context) (int, error) {
	if interfaceIsNil(r.asyncJobs) {
		return 0, ErrBackgroundResponseStorageUnavailable
	}
	jobs, err := r.asyncJobs.ClaimAsyncJobs(ctx, backgroundResponseJobKind, backgroundResponseBatchSize, backgroundResponseLease)
	if err != nil {
		return 0, err
	}
	var failures []error
	for _, job := range jobs {
		if err := r.processBackgroundResponse(ctx, job); err != nil {
			failures = append(failures, fmt.Errorf("response %s: %w", job.ResourceID, err))
		}
	}
	return len(jobs), errors.Join(failures...)
}

func (r Router) processBackgroundResponse(ctx context.Context, claimed asyncstate.Job) error {
	var job backgroundResponseJob
	if json.Unmarshal(claimed.Payload, &job) != nil || job.RequestID != claimed.ExecutionID || job.CredentialID == "" || job.Model == "" {
		return r.retryBackgroundResponse(ctx, claimed, errors.New("invalid persisted background response job"))
	}
	req := job.requestContext()
	response, err := r.RetrieveResponse(ctx, req, claimed.ResourceID)
	if err != nil {
		return r.retryBackgroundResponse(ctx, claimed, err)
	}
	if backgroundResponsePending(response) {
		return r.retryBackgroundResponse(ctx, claimed, nil)
	}
	if response.Status == "failed" || response.Status == "cancelled" {
		if req.Metadata == nil {
			req.Metadata = map[string]string{}
		}
		req.Metadata["provider.status"] = "error"
		if response.Error != nil {
			req.Metadata["provider.error"] = response.Error.Message
		}
	}
	req.ResponsesResponse = &response
	if err := r.modules.RunPostResponse(ctx, &req); err != nil && !errors.Is(err, modules.ErrContentRejected) {
		return r.retryBackgroundResponse(ctx, claimed, err)
	}
	return r.asyncJobs.CompleteAsyncJob(ctx, claimed.Kind, claimed.ResourceID, claimed.LeaseGeneration)
}

func (r Router) retryBackgroundResponse(ctx context.Context, job asyncstate.Job, cause error) error {
	retryErr := r.asyncJobs.RetryAsyncJob(ctx, job.Kind, job.ResourceID, job.LeaseGeneration, backgroundResponseRetry(job.Attempts))
	return errors.Join(cause, retryErr)
}

func backgroundResponseRetry(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 6)
	return min(delay, time.Minute)
}

func RunBackgroundResponseWorker(ctx context.Context, processor BackgroundResponseProcessor) {
	if processor == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := processor.ProcessBackgroundResponses(ctx); err != nil && ctx.Err() == nil {
			log.Printf("background response processing failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
