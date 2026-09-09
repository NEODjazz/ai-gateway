package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

type moderationLifecycleRecorder struct {
	pre   int
	post  int
	model string
}

func (*moderationLifecycleRecorder) Name() string   { return "moderation-recorder" }
func (*moderationLifecycleRecorder) Required() bool { return true }
func (m *moderationLifecycleRecorder) Handle(_ context.Context, req *modules.RequestContext) error {
	m.pre++
	if req.ModerationRequest != nil {
		m.model = req.ModerationRequest.Model
	}
	return nil
}
func (*moderationLifecycleRecorder) PostResponseEnabled() bool { return true }
func (m *moderationLifecycleRecorder) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.post++
	if req.ModerationResponse == nil {
		return context.Canceled
	}
	return nil
}

func TestModerationsEndToEndProviderLifecycle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/moderations" || r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Fatalf("unexpected upstream request: %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var request openai.ModerationRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "upstream-safe" || openai.ModerationInputText(request.Input) != "inspect" {
			t.Fatalf("unexpected request: %+v", request)
		}
		value := false
		_ = json.NewEncoder(w).Encode(openai.ModerationResponse{ID: "modr-e2e", Model: request.Model, Results: []openai.ModerationResult{{Categories: map[string]*bool{"violence": &value}, CategoryScores: map[string]float64{"violence": .01}, CategoryAppliedInputTypes: map[string][]string{"violence": {"text"}}}}})
	}))
	defer upstream.Close()
	recorder := &moderationLifecycleRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "safe", Type: "openai-compatible", BaseURL: upstream.URL + "/v1", APIKey: "upstream-key", Models: []string{"public-safe"}, ModelAliases: map[string]string{"public-safe": "upstream-safe"}, Capabilities: []string{"moderation"}}}, Modules: modules.NewPipeline([]modules.Module{recorder})})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public-safe"}}}}), router))
	request := httptest.NewRequest(http.MethodPost, "/v1/moderations", strings.NewReader(`{"model":"public-safe","input":[{"type":"text","text":"inspect"}]}`))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"modr-e2e"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if recorder.pre != 1 || recorder.post != 1 || recorder.model != "upstream-safe" {
		t.Fatalf("lifecycle pre=%d post=%d model=%q", recorder.pre, recorder.post, recorder.model)
	}
}
