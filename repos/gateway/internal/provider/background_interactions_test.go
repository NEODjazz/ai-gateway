package provider

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type backgroundInteractionClient struct {
	createCalls   atomic.Int32
	retrieveCalls atomic.Int32
	cancelCalls   atomic.Int32
	retrieve      openai.InteractionResponse
}

func (*backgroundInteractionClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, errors.New("unexpected chat call")
}

func (*backgroundInteractionClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, errors.New("unexpected responses call")
}

func (c *backgroundInteractionClient) Interactions(_ context.Context, request openai.InteractionRequest) (openai.InteractionResponse, error) {
	c.createCalls.Add(1)
	return openai.InteractionResponse{ID: "interaction_background", Object: "interaction", Model: request.Model, Agent: request.Agent, Status: "queued"}, nil
}

func (c *backgroundInteractionClient) RetrieveInteraction(context.Context, string) (openai.InteractionResponse, error) {
	c.retrieveCalls.Add(1)
	return c.retrieve, nil
}

func (c *backgroundInteractionClient) CancelInteraction(_ context.Context, id string) (openai.InteractionResponse, error) {
	c.cancelCalls.Add(1)
	return openai.InteractionResponse{ID: id, Object: "interaction", Status: "cancelled"}, nil
}

func (*backgroundInteractionClient) DeleteInteraction(context.Context, string) error { return nil }

func TestBackgroundInteractionDefersSettlementAndSurvivesRouterRestart(t *testing.T) {
	store := true
	interaction := openai.InteractionRequest{Model: "public-model", Input: "prompt-must-not-be-persisted", Store: &store, Background: true}
	shared, message := interaction.NativeResponseRequest()
	if message != "" {
		t.Fatal(message)
	}
	req := modules.RequestContext{RequestID: "interaction-execution", CredentialID: "credential", UserID: "user", Request: openai.ChatCompletionRequest{Model: interaction.Model}, ResponseRequest: &shared}
	jobs := &backgroundJobStore{}
	ownership := &ownershipTestStore{data: map[string][]byte{}}
	client := &backgroundInteractionClient{retrieve: openai.InteractionResponse{ID: "interaction_background", Object: "interaction", Model: interaction.Model, Status: "completed", Usage: openai.InteractionUsage{TotalInputTokens: 5, TotalOutputTokens: 2, TotalTokens: 7}}}
	recorder := &backgroundLifecycleRecorder{}
	endpoint := Endpoint{Name: "deployment", ProviderID: "provider", Type: "gemini", Models: []string{interaction.Model}, Capabilities: []string{"interactions", "background_interactions"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	router := Router{endpoints: []Endpoint{endpoint}, endpointState: &endpointRegistry{}, modules: modules.NewPipeline([]modules.Module{recorder}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: newResponseOwnershipStore(time.Hour, ownership), asyncJobs: jobs}
	router.endpointState.current.Store(&router.endpoints)

	response, err := router.Interactions(t.Context(), req, interaction)
	if err != nil || response.Status != "queued" || recorder.pre != 1 || recorder.post != 0 || recorder.failure != 0 || client.createCalls.Load() != 1 {
		t.Fatalf("response=%+v lifecycle=%+v creates=%d err=%v", response, recorder, client.createCalls.Load(), err)
	}
	if jobs.job == nil || jobs.job.Kind != backgroundInteractionJobKind || bytes.Contains(jobs.job.Payload, []byte("prompt-must-not-be-persisted")) {
		t.Fatalf("unsafe or missing durable job: %+v", jobs.job)
	}

	restarted := router
	processed, err := restarted.ProcessBackgroundResponses(t.Context())
	if err != nil || processed != 1 || recorder.post != 1 || recorder.requestID != "interaction-execution" || recorder.totalTokens != 7 || jobs.job != nil || client.retrieveCalls.Load() != 1 {
		t.Fatalf("processed=%d lifecycle=%+v pending=%+v retrieves=%d err=%v", processed, recorder, jobs.job, client.retrieveCalls.Load(), err)
	}
}

func TestBackgroundInteractionQueueFailureCancelsReservationAndUpstream(t *testing.T) {
	store := true
	interaction := openai.InteractionRequest{Model: "model", Input: "secret", Store: &store, Background: true}
	shared, _ := interaction.NativeResponseRequest()
	req := modules.RequestContext{RequestID: "execution", CredentialID: "credential", Request: openai.ChatCompletionRequest{Model: interaction.Model}, ResponseRequest: &shared}
	jobs := &backgroundJobStore{enqueueErr: errors.New("database unavailable")}
	ownership := &ownershipTestStore{data: map[string][]byte{}}
	client := &backgroundInteractionClient{}
	recorder := &backgroundLifecycleRecorder{}
	endpoint := Endpoint{Name: "deployment", Type: "gemini", Models: []string{interaction.Model}, Capabilities: []string{"interactions", "background_interactions"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	router := Router{endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline([]modules.Module{recorder}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: newResponseOwnershipStore(time.Hour, ownership), asyncJobs: jobs}

	if _, err := router.Interactions(t.Context(), req, interaction); !errors.Is(err, ErrBackgroundResponseStorageUnavailable) {
		t.Fatalf("error=%v", err)
	}
	if client.cancelCalls.Load() != 1 || recorder.pre != 1 || recorder.post != 0 || recorder.failure != 1 {
		t.Fatalf("cancel=%d lifecycle=%+v", client.cancelCalls.Load(), recorder)
	}
	if _, found := ownership.data[responseOwnershipKey(req, "interaction_background")]; found {
		t.Fatal("compensated interaction retained an ownership binding")
	}
}

func TestBackgroundInteractionRequiresDurableStorageBeforeReservation(t *testing.T) {
	store := true
	interaction := openai.InteractionRequest{Model: "model", Input: "secret", Store: &store, Background: true}
	shared, _ := interaction.NativeResponseRequest()
	req := modules.RequestContext{RequestID: "execution", CredentialID: "credential", Request: openai.ChatCompletionRequest{Model: interaction.Model}, ResponseRequest: &shared}
	client := &backgroundInteractionClient{}
	recorder := &backgroundLifecycleRecorder{}
	endpoint := Endpoint{Name: "deployment", Type: "gemini", Models: []string{interaction.Model}, Capabilities: []string{"interactions", "background_interactions"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	router := Router{endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline([]modules.Module{recorder}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: newResponseOwnershipStore(time.Hour, &ownershipTestStore{data: map[string][]byte{}})}

	if _, err := router.Interactions(t.Context(), req, interaction); !errors.Is(err, ErrBackgroundResponseStorageUnavailable) {
		t.Fatalf("error=%v", err)
	}
	if client.createCalls.Load() != 0 || recorder.pre != 0 {
		t.Fatalf("creates=%d reserve=%d", client.createCalls.Load(), recorder.pre)
	}
}

func TestBackgroundInteractionRequiresExplicitDeploymentCapability(t *testing.T) {
	store := true
	interaction := openai.InteractionRequest{Model: "model", Input: "hello", Store: &store, Background: true}
	shared, _ := interaction.NativeResponseRequest()
	req := modules.RequestContext{RequestID: "execution", CredentialID: "credential", Request: openai.ChatCompletionRequest{Model: interaction.Model}, ResponseRequest: &shared}
	client := &backgroundInteractionClient{}
	recorder := &backgroundLifecycleRecorder{}
	endpoint := Endpoint{Name: "deployment", Type: "gemini", Models: []string{interaction.Model}, Capabilities: []string{"interactions"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	router := Router{endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline([]modules.Module{recorder}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: newResponseOwnershipStore(time.Hour, &ownershipTestStore{data: map[string][]byte{}}), asyncJobs: &backgroundJobStore{}}

	if _, err := router.Interactions(t.Context(), req, interaction); !errors.Is(err, ErrBackgroundInteractionsUnsupported) {
		t.Fatalf("error=%v", err)
	}
	if client.createCalls.Load() != 0 || recorder.pre != 0 {
		t.Fatalf("creates=%d reserve=%d", client.createCalls.Load(), recorder.pre)
	}
}
