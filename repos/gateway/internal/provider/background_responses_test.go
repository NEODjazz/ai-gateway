package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type backgroundJobStore struct {
	mu         sync.Mutex
	job        *asyncstate.Job
	enqueueErr error
}

func (s *backgroundJobStore) EnqueueAsyncJob(_ context.Context, job asyncstate.Job) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enqueueErr != nil {
		return false, s.enqueueErr
	}
	copy := job
	copy.Payload = append([]byte(nil), job.Payload...)
	s.job = &copy
	return true, nil
}

func (s *backgroundJobStore) HasAsyncJob(_ context.Context, kind, resourceID, ownerKey string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.job != nil && s.job.Kind == kind && s.job.ResourceID == resourceID && s.job.OwnerKey == ownerKey, nil
}

func (s *backgroundJobStore) ClaimAsyncJobs(_ context.Context, kind string, _ int, _ time.Duration) ([]asyncstate.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil || s.job.Kind != kind {
		return nil, nil
	}
	s.job.Attempts++
	s.job.LeaseGeneration++
	return []asyncstate.Job{*s.job}, nil
}

func (s *backgroundJobStore) RetryAsyncJob(_ context.Context, _, _ string, generation int64, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil || s.job.LeaseGeneration != generation {
		return asyncstate.ErrLeaseLost
	}
	return nil
}

func (s *backgroundJobStore) CompleteAsyncJob(_ context.Context, _, _ string, generation int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil || s.job.LeaseGeneration != generation {
		return asyncstate.ErrLeaseLost
	}
	s.job = nil
	return nil
}

type backgroundResponseClient struct {
	responseCalls int
	cancelCalls   int
	retrieve      openai.ResponseResponse
	retrieveErr   error
}

func (*backgroundResponseClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, errors.New("unexpected chat call")
}

func (c *backgroundResponseClient) Responses(_ context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	c.responseCalls++
	return openai.ResponseResponse{ID: "resp_background", Model: request.Model, Status: "queued"}, nil
}

func (c *backgroundResponseClient) RetrieveResponse(context.Context, string) (openai.ResponseResponse, error) {
	return c.retrieve, c.retrieveErr
}

func TestBackgroundResponseReportsCauseAfterSchedulingRetry(t *testing.T) {
	retrieveErr := errors.New("provider unavailable")
	jobs := &backgroundJobStore{}
	request := openai.ResponseRequest{Model: "public-model", Input: "prompt", Background: true}
	req := modules.RequestContext{RequestID: "execution", CredentialID: "credential", Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request}
	client := &backgroundResponseClient{retrieveErr: retrieveErr}
	endpoint := Endpoint{Name: "deployment", ProviderID: "provider", Type: "openai-compatible", Models: []string{request.Model}, Capabilities: []string{"responses", "background_responses"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	ownership := newResponseOwnershipStore(time.Hour, &ownershipTestStore{data: map[string][]byte{}})
	router := Router{endpoints: []Endpoint{endpoint}, endpointState: &endpointRegistry{}, modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: ownership, asyncJobs: jobs}
	router.endpointState.current.Store(&router.endpoints)
	if err := ownership.put(t.Context(), req, "resp_background", responseOwnership{Endpoint: endpoint.Name, Model: request.Model, Deployment: responseDeploymentIdentity(endpoint)}); err != nil {
		t.Fatal(err)
	}

	job := newBackgroundResponseJob(req)
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	jobs.job = &asyncstate.Job{Kind: backgroundResponseJobKind, ResourceID: "resp_background", OwnerKey: backgroundResponseOwner(req), EndpointID: "deployment", ExecutionID: req.RequestID, Payload: payload}
	processed, err := router.ProcessBackgroundResponses(t.Context())
	if processed != 1 || !errors.Is(err, retrieveErr) || jobs.job == nil || jobs.job.Attempts != 1 {
		t.Fatalf("processed=%d err=%v job=%+v", processed, err, jobs.job)
	}
}

func (c *backgroundResponseClient) CancelResponse(_ context.Context, id string) (openai.ResponseResponse, error) {
	c.cancelCalls++
	return openai.ResponseResponse{ID: id, Status: "cancelled"}, nil
}

type backgroundLifecycleRecorder struct {
	pre, post, failure int
	requestID          string
	totalTokens        int
	anonymizationMode  string
	anonymizationRules string
}

func (*backgroundLifecycleRecorder) Name() string   { return "billing" }
func (*backgroundLifecycleRecorder) Required() bool { return true }
func (r *backgroundLifecycleRecorder) Handle(context.Context, *modules.RequestContext) error {
	r.pre++
	return nil
}
func (*backgroundLifecycleRecorder) PostResponseEnabled() bool { return true }
func (r *backgroundLifecycleRecorder) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	r.post++
	r.requestID = req.RequestID
	r.totalTokens = req.ResponsesResponse.Usage.TotalTokens
	r.anonymizationMode = req.Metadata["provider.modules.anonymizer.mode"]
	r.anonymizationRules = req.Metadata["provider.modules.anonymizer.rules"]
	return nil
}
func (r *backgroundLifecycleRecorder) HandleFailure(context.Context, *modules.RequestContext, error) error {
	r.failure++
	return nil
}

func TestBackgroundResponseDefersSettlementAndSurvivesRouterRestart(t *testing.T) {
	store := true
	request := openai.ResponseRequest{Model: "public-model", Input: "prompt-must-not-be-persisted", Store: &store, Background: true}
	req := modules.RequestContext{
		RequestID: "execution-original", CredentialID: "credential", UserID: "user",
		Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request,
		Metadata: map[string]string{
			"policy.modules.anonymizer.mode":     "custom",
			"policy.modules.anonymizer.rules":    "email,phone",
			"policy.modules.anonymizer.profiles": "background-pii",
		},
	}
	jobs := &backgroundJobStore{}
	ownership := &ownershipTestStore{data: map[string][]byte{}}
	client := &backgroundResponseClient{retrieve: openai.ResponseResponse{ID: "resp_background", Model: request.Model, Status: "completed", Usage: openai.ResponseUsage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7}}}
	recorder := &backgroundLifecycleRecorder{}
	endpoint := Endpoint{Name: "deployment", ProviderID: "provider", Type: "openai-compatible", Models: []string{request.Model}, Capabilities: []string{"responses", "background_responses"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	router := Router{endpoints: []Endpoint{endpoint}, endpointState: &endpointRegistry{}, modules: modules.NewPipeline([]modules.Module{recorder}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: newResponseOwnershipStore(time.Hour, ownership), asyncJobs: jobs}
	router.endpointState.current.Store(&router.endpoints)

	response, err := router.Responses(t.Context(), req)
	if err != nil || response.Status != "queued" || recorder.pre != 1 || recorder.post != 0 || recorder.failure != 0 {
		t.Fatalf("response=%+v lifecycle=%+v err=%v", response, recorder, err)
	}
	if jobs.job == nil || bytes.Contains(jobs.job.Payload, []byte("prompt-must-not-be-persisted")) {
		t.Fatalf("unsafe or missing durable job: %+v", jobs.job)
	}
	if settled, err := router.BackgroundResponseSettled(t.Context(), req, response.ID); err != nil || settled {
		t.Fatalf("pending settled=%t err=%v", settled, err)
	}

	restarted := router
	processed, err := restarted.ProcessBackgroundResponses(t.Context())
	if err != nil || processed != 1 || recorder.post != 1 || recorder.requestID != "execution-original" || recorder.totalTokens != 7 || recorder.anonymizationMode != "custom" || recorder.anonymizationRules != "email,phone" || jobs.job != nil {
		t.Fatalf("processed=%d lifecycle=%+v pending=%+v err=%v", processed, recorder, jobs.job, err)
	}
	if settled, err := restarted.BackgroundResponseSettled(t.Context(), req, response.ID); err != nil || !settled {
		t.Fatalf("completed settled=%t err=%v", settled, err)
	}
}

func TestBackgroundResponseQueueFailureCancelsReservationAndUpstream(t *testing.T) {
	store := true
	request := openai.ResponseRequest{Model: "m", Input: "secret", Store: &store, Background: true}
	req := modules.RequestContext{RequestID: "execution", CredentialID: "credential", Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request}
	jobs := &backgroundJobStore{enqueueErr: errors.New("database unavailable")}
	ownership := &ownershipTestStore{data: map[string][]byte{}}
	client := &backgroundResponseClient{}
	recorder := &backgroundLifecycleRecorder{}
	endpoint := Endpoint{Name: "deployment", ProviderID: "provider", Type: "openai-compatible", Models: []string{"m"}, Capabilities: []string{"responses", "background_responses"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	router := Router{endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline([]modules.Module{recorder}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: newResponseOwnershipStore(time.Hour, ownership), asyncJobs: jobs}

	if _, err := router.Responses(t.Context(), req); !errors.Is(err, ErrBackgroundResponseStorageUnavailable) {
		t.Fatalf("error=%v", err)
	}
	if client.cancelCalls != 1 || recorder.pre != 1 || recorder.post != 0 || recorder.failure != 1 {
		t.Fatalf("cancel=%d lifecycle=%+v", client.cancelCalls, recorder)
	}
	if _, found := ownership.data[responseOwnershipKey(req, "resp_background")]; found {
		t.Fatal("compensated response retained an ownership binding")
	}
}

func TestBackgroundResponseRequiresJobStorageBeforeReservation(t *testing.T) {
	store := true
	request := openai.ResponseRequest{Model: "m", Input: "secret", Store: &store, Background: true}
	req := modules.RequestContext{RequestID: "execution", CredentialID: "credential", Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request}
	client := &backgroundResponseClient{}
	recorder := &backgroundLifecycleRecorder{}
	endpoint := Endpoint{Name: "deployment", ProviderID: "provider", Type: "openai-compatible", Models: []string{"m"}, Capabilities: []string{"responses", "background_responses"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	router := Router{endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline([]modules.Module{recorder}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: newResponseOwnershipStore(time.Hour, &ownershipTestStore{data: map[string][]byte{}})}
	if _, err := router.Responses(t.Context(), req); !errors.Is(err, ErrBackgroundResponseStorageUnavailable) {
		t.Fatalf("error=%v", err)
	}
	if client.responseCalls != 0 || recorder.pre != 0 || recorder.post != 0 || recorder.failure != 0 {
		t.Fatalf("provider=%d lifecycle=%+v", client.responseCalls, recorder)
	}
}

func TestBackgroundResponseRequiresExplicitDeploymentCapability(t *testing.T) {
	store := true
	request := openai.ResponseRequest{Model: "m", Input: "hello", Store: &store, Background: true}
	req := modules.RequestContext{RequestID: "execution", CredentialID: "credential", Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request}
	client := &backgroundResponseClient{}
	recorder := &backgroundLifecycleRecorder{}
	endpoint := Endpoint{Name: "deployment", ProviderID: "provider", Type: "openai-compatible", Models: []string{"m"}, Capabilities: []string{"responses"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	router := Router{endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline([]modules.Module{recorder}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: newResponseOwnershipStore(time.Hour, &ownershipTestStore{data: map[string][]byte{}}), asyncJobs: &backgroundJobStore{}}
	if _, err := router.Responses(t.Context(), req); !errors.Is(err, ErrBackgroundResponsesUnsupported) {
		t.Fatalf("error=%v", err)
	}
	if client.responseCalls != 0 || recorder.pre != 0 {
		t.Fatalf("provider=%d reserve=%d", client.responseCalls, recorder.pre)
	}
}
