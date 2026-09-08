package provider

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type ownershipTestStore struct {
	data map[string][]byte
	err  error
}

type ownershipResponseClient struct {
	calls int
}

type ownershipPostModule struct {
	postCalls int
}

func (*ownershipPostModule) Name() string   { return "ownership-post" }
func (*ownershipPostModule) Required() bool { return true }
func (*ownershipPostModule) Handle(context.Context, *modules.RequestContext) error {
	return nil
}
func (*ownershipPostModule) PostResponseEnabled() bool { return true }
func (m *ownershipPostModule) HandlePostResponse(context.Context, *modules.RequestContext) error {
	m.postCalls++
	return nil
}

func (p *ownershipResponseClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, errors.New("unexpected chat request")
}

func (p *ownershipResponseClient) Responses(_ context.Context, request openai.ResponseRequest) (openai.ResponseResponse, error) {
	p.calls++
	return openai.ResponseResponse{ID: "resp_owned", Model: request.Model, Status: "completed"}, nil
}

func (p *ownershipResponseClient) StreamResponses(ctx context.Context, request openai.ResponseRequest, _ ResponseStreamWriter) (openai.ResponseResponse, error) {
	return p.Responses(ctx, request)
}

func (s *ownershipTestStore) Get(_ context.Context, k string) ([]byte, bool, error) {
	v, ok := s.data[k]
	return v, ok, s.err
}
func (s *ownershipTestStore) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	if s.err != nil {
		return s.err
	}
	s.data[k] = append([]byte(nil), v...)
	return nil
}

func TestResponseOwnershipIsolation(t *testing.T) {
	backend := &ownershipTestStore{data: map[string][]byte{}}
	store := responseOwnershipStore{store: backend, ttl: time.Hour}
	owner := modules.RequestContext{CredentialID: "key1", UserID: "user1"}
	binding := responseOwnership{Endpoint: "e", Model: "m", Deployment: responseDeploymentIdentity(Endpoint{Name: "e", ProviderID: "p", Type: "openai-compatible", BaseURL: "https://example.com", CredentialID: "c"})}
	if err := store.put(t.Context(), owner, "resp_1", binding); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.get(t.Context(), owner, "resp_1")
	if err != nil || !found || got != binding {
		t.Fatalf("binding=%+v found=%v err=%v", got, found, err)
	}
	for _, other := range []modules.RequestContext{{CredentialID: "key2", UserID: "user1"}, {CredentialID: "key1", UserID: "user2"}, {UserID: "user1"}} {
		if _, found, err := store.get(t.Context(), other, "resp_1"); found || err != nil {
			t.Fatalf("cross-owner access: %v %v", found, err)
		}
	}
	backend.err = errors.New("private backend details")
	if _, _, err := store.get(t.Context(), owner, "resp_1"); !errors.Is(err, ErrResponseOwnershipUnavailable) {
		t.Fatal(err)
	}
	if err := store.put(t.Context(), owner, "resp_1", binding); !errors.Is(err, ErrResponseOwnershipUnavailable) {
		t.Fatal(err)
	}
	backend.err = nil
	backend.data[responseOwnershipKey(owner, "resp_1")] = []byte(`{}`)
	if _, _, err := store.get(t.Context(), owner, "resp_1"); !errors.Is(err, ErrResponseOwnershipUnavailable) {
		t.Fatal("corrupt record accepted")
	}
}

func TestResponseDeploymentIdentityDetectsReplacement(t *testing.T) {
	original := Endpoint{Name: "e", ProviderID: "p", Type: "openai-compatible", BaseURL: "https://example.com", CredentialID: "c"}
	want := responseDeploymentIdentity(original)
	for _, field := range []string{"name", "provider", "type", "url", "credential"} {
		changed := original
		switch field {
		case "name":
			changed.Name = "other"
		case "provider":
			changed.ProviderID = "other"
		case "type":
			changed.Type = "other"
		case "url":
			changed.BaseURL = "https://other.example"
		case "credential":
			changed.CredentialID = "other"
		}
		if responseDeploymentIdentity(changed) == want {
			t.Fatalf("replacement ignored: %s", field)
		}
	}
}

func TestStoredResponsesRequireAndPersistOwnership(t *testing.T) {
	store := true
	request := openai.ResponseRequest{Model: "public-model", Input: "hello", Store: &store}
	context := modules.RequestContext{
		CredentialID:    "credential",
		UserID:          "user",
		Request:         openai.ChatCompletionRequest{Model: request.Model},
		ResponseRequest: &request,
	}
	client := &ownershipResponseClient{}
	endpoint := Endpoint{Name: "deployment", ProviderID: "provider", Type: "test", Models: []string{"public-model"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}

	withoutStore := Router{endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}}
	if _, err := withoutStore.Responses(t.Context(), context); !errors.Is(err, ErrResponseOwnershipUnavailable) || client.calls != 0 {
		t.Fatalf("missing ownership store reached provider: calls=%d err=%v", client.calls, err)
	}

	backend := &ownershipTestStore{data: map[string][]byte{}}
	router := withoutStore
	router.ownership = newResponseOwnershipStore(time.Hour, backend)
	response, err := router.Responses(t.Context(), context)
	if err != nil || response.ID != "resp_owned" || client.calls != 1 {
		t.Fatalf("response=%+v calls=%d err=%v", response, client.calls, err)
	}
	binding, found, err := router.ownership.get(t.Context(), context, response.ID)
	if err != nil || !found || binding.Endpoint != endpoint.Name || binding.Model != request.Model || binding.Deployment != responseDeploymentIdentity(endpoint) {
		t.Fatalf("binding=%+v found=%v err=%v", binding, found, err)
	}
	if _, err := router.Responses(t.Context(), context); err != nil || client.calls != 2 {
		t.Fatalf("stored response was served from cache: calls=%d err=%v", client.calls, err)
	}
}

func TestStoredStreamingResponsePersistsOwnership(t *testing.T) {
	store := true
	request := openai.ResponseRequest{Model: "public-model", Input: "hello", Store: &store, Stream: true}
	req := modules.RequestContext{CredentialID: "credential", UserID: "user", Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request}
	backend := &ownershipTestStore{data: map[string][]byte{}}
	client := &ownershipResponseClient{}
	endpoint := Endpoint{Name: "deployment", Type: "test", Models: []string{"public-model"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	router := Router{endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: newResponseOwnershipStore(time.Hour, backend)}
	response, handled, err := router.StreamResponses(t.Context(), req, func(string, string) error { return nil })
	if err != nil || !handled || response.ID != "resp_owned" || client.calls != 1 {
		t.Fatalf("response=%+v handled=%v calls=%d err=%v", response, handled, client.calls, err)
	}
	if _, found, err := router.ownership.get(t.Context(), req, response.ID); err != nil || !found {
		t.Fatalf("ownership found=%v err=%v", found, err)
	}
}

func TestStoredResponseSettlesPostModulesWhenOwnershipWriteFails(t *testing.T) {
	store := true
	request := openai.ResponseRequest{Model: "public-model", Input: "hello", Store: &store}
	req := modules.RequestContext{CredentialID: "credential", UserID: "user", Request: openai.ChatCompletionRequest{Model: request.Model}, ResponseRequest: &request}
	backend := &ownershipTestStore{data: map[string][]byte{}, err: errors.New("storage failed")}
	client := &ownershipResponseClient{}
	post := &ownershipPostModule{}
	endpoint := Endpoint{Name: "deployment", Type: "test", Models: []string{"public-model"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	router := Router{endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline([]modules.Module{post}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{}, ownership: newResponseOwnershipStore(time.Hour, backend)}
	if _, err := router.Responses(t.Context(), req); !errors.Is(err, ErrResponseOwnershipUnavailable) || client.calls != 1 || post.postCalls != 1 {
		t.Fatalf("calls=%d post=%d err=%v", client.calls, post.postCalls, err)
	}
}

func (s *ownershipTestStore) SetIfAbsentOrEqual(ctx context.Context, k string, v []byte, ttl time.Duration) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if old, found := s.data[k]; found {
		return string(old) == string(v), nil
	}
	return true, s.Set(ctx, k, v, ttl)
}

func TestResponseOwnershipCannotBeReassigned(t *testing.T) {
	backend := &ownershipTestStore{data: map[string][]byte{}}
	store := responseOwnershipStore{store: backend, ttl: time.Hour}
	owner := modules.RequestContext{CredentialID: "key", UserID: "user"}
	binding := responseOwnership{Endpoint: "first", Model: "m", Deployment: responseDeploymentIdentity(Endpoint{Name: "first"})}
	for i := 0; i < 2; i++ {
		if err := store.put(t.Context(), owner, "resp_1", binding); err != nil {
			t.Fatal(err)
		}
	}
	changed := binding
	changed.Endpoint = "second"
	if err := store.put(t.Context(), owner, "resp_1", changed); !errors.Is(err, ErrResponseOwnershipConflict) {
		t.Fatalf("collision=%v", err)
	}
	got, found, err := store.get(t.Context(), owner, "resp_1")
	if err != nil || !found || got != binding {
		t.Fatal("original owner record changed")
	}
}
