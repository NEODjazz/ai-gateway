package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestOpenAICompatibleCountsResponseInputTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses/input_tokens" || r.Header.Get("Authorization") != "Bearer provider-key" {
			t.Fatalf("unexpected request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "model" || body["instructions"] != "be concise" || body["input"] != "hello" {
			t.Fatalf("request fields were lost: %#v", body)
		}
		if _, found := body["provider"]; found {
			t.Fatalf("gateway provider selector leaked upstream: %#v", body)
		}
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":37}`))
	}))
	defer server.Close()

	result, err := NewOpenAICompatible(server.URL+"/v1", "provider-key", false).CountResponseInputTokens(t.Context(), openai.ResponseInputTokenCountRequest{
		Provider: "deployment", Model: "model", Input: "hello", Instructions: "be concise",
		Tools: []openai.ResponseTool{{Type: "function", Name: "lookup", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil || result.Object != "response.input_tokens" || result.InputTokens != 37 {
		t.Fatalf("unexpected result: %+v err=%v", result, err)
	}
}

func TestOpenAICompatibleRejectsInvalidResponseInputTokenCounts(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"object":"response.input_tokens"}`,
		`{"object":"response.input_tokens","input_tokens":-1}`,
		`{"object":"other","input_tokens":1}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			_, err := NewOpenAICompatible(server.URL, "", false).CountResponseInputTokens(t.Context(), openai.ResponseInputTokenCountRequest{Model: "m", Input: "x"})
			if err == nil {
				t.Fatal("invalid provider token count was accepted")
			}
		})
	}
}

type responseCountTestClient struct {
	calls   int
	request openai.ResponseInputTokenCountRequest
}

func (*responseCountTestClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}

func (*responseCountTestClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}

func (c *responseCountTestClient) CountResponseInputTokens(_ context.Context, request openai.ResponseInputTokenCountRequest) (openai.ResponseInputTokenCount, error) {
	c.calls++
	c.request = request
	return openai.ResponseInputTokenCount{Object: "response.input_tokens", InputTokens: 9}, nil
}

type responseCountTransform struct{ calls int }

func (*responseCountTransform) Name() string   { return "anonymizer" }
func (*responseCountTransform) Required() bool { return true }
func (m *responseCountTransform) Handle(_ context.Context, req *modules.RequestContext) error {
	m.calls++
	req.ResponseRequest.Input = "transformed"
	return nil
}

func TestRouterResponseInputTokenCountUsesExplicitAdapterAndPolicies(t *testing.T) {
	client := &responseCountTestClient{}
	transform := &responseCountTransform{}
	router := Router{
		endpoints: []Endpoint{
			{Name: "unsupported", Type: "demo", Provider: &affinityResponseClient{}, Admission: newAdmissionController(0, 0, 0)},
			{Name: "counter", Type: "openai-compatible", Provider: client, Admission: newAdmissionController(0, 0, 0)},
		},
		modules: modules.NewPipeline([]modules.Module{transform}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ResponseRequest{Model: "model", Input: "original"}
	result, err := router.CountResponseInputTokens(t.Context(), modules.RequestContext{
		Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request,
	})
	if err != nil || result.InputTokens != 9 || client.calls != 1 || transform.calls != 1 || client.request.Input != "transformed" {
		t.Fatalf("unexpected count lifecycle: result=%+v client=%+v transform=%d err=%v", result, client, transform.calls, err)
	}
}

func TestRouterResponseInputTokenCountRequiresExplicitSupport(t *testing.T) {
	router := Router{
		endpoints: []Endpoint{{Name: "unsupported", Type: "demo", Provider: &affinityResponseClient{}, Admission: newAdmissionController(0, 0, 0)}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ResponseRequest{Model: "model", Input: "x"}
	_, err := router.CountResponseInputTokens(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request})
	if !errors.Is(err, ErrResponseInputTokenCountUnsupported) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRouterResponseInputTokenCountScopesPreviousResponseToOwner(t *testing.T) {
	client := &responseCountTestClient{}
	endpoint := Endpoint{Name: "counter", ProviderID: "provider", Type: "openai-compatible", Models: []string{"model"}, Capabilities: []string{"responses"}, Provider: client, Admission: newAdmissionController(0, 0, 0)}
	backend := &ownershipTestStore{data: map[string][]byte{}}
	router := Router{
		endpoints: []Endpoint{endpoint}, modules: modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
		ownership: newResponseOwnershipStore(time.Hour, backend),
	}
	owner := modules.RequestContext{CredentialID: "owner", UserID: "user"}
	binding := responseOwnership{Endpoint: endpoint.Name, Model: "model", Deployment: responseDeploymentIdentity(endpoint)}
	if err := router.ownership.put(t.Context(), owner, "resp_owned", binding); err != nil {
		t.Fatal(err)
	}
	request := openai.ResponseRequest{Model: "model", Input: "x", PreviousResponse: "resp_owned"}
	owner.Request = openai.ChatCompletionRequest{Model: "model"}
	owner.ResponseRequest = &request
	if _, err := router.CountResponseInputTokens(t.Context(), owner); err != nil || client.calls != 1 {
		t.Fatalf("owner count failed: calls=%d err=%v", client.calls, err)
	}
	other := owner
	other.CredentialID = "other"
	if _, err := router.CountResponseInputTokens(t.Context(), other); !errors.Is(err, ErrResponseNotFound) || client.calls != 1 {
		t.Fatalf("cross-owner previous response reached provider: calls=%d err=%v", client.calls, err)
	}
}

func TestOpenAICompatibleResponseInputTokenCountDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := NewOpenAICompatible(server.URL, "secret", false).CountResponseInputTokens(t.Context(), openai.ResponseInputTokenCountRequest{Model: "m", Input: "x"})
	if err == nil || redirected.Load() || !strings.Contains(err.Error(), "307") {
		t.Fatalf("redirect handling was unsafe: redirected=%v err=%v", redirected.Load(), err)
	}
}
