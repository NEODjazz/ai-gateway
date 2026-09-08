package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

type failedAffinitySessionStore struct{ reads int }

func (s *failedAffinitySessionStore) Get(context.Context, string) ([]byte, bool, error) {
	s.reads++
	return nil, false, errors.New("sensitive storage failure")
}
func (*failedAffinitySessionStore) Set(context.Context, string, []byte, time.Duration) error {
	return nil
}

func TestResponsesAffinityLookupFailureStopsExecution(t *testing.T) {
	for _, stream := range []bool{false, true} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
		defer server.Close()
		sessions := &failedAffinitySessionStore{}
		pre := &countingAccessModule{}
		router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "openai-compatible", BaseURL: server.URL, Models: []string{"m"}, Stream: true}}, AffinityTTL: time.Hour, SessionStore: sessions, Modules: modules.NewPipeline([]modules.Module{pre})})
		handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}}}}), router))
		body := `{"model":"m","input":"continue","previous_response_id":"resp-existing"`
		if stream {
			body += `,"stream":true`
		}
		body += "}"
		request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer gateway-test-key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 503 || pre.calls != 0 || calls != 0 || sessions.reads != 1 || strings.Contains(response.Body.String(), "sensitive") || !strings.Contains(response.Body.String(), "response_affinity_unavailable") {
			t.Fatalf("affinity failure: stream=%v status=%d calls=%d reads=%d body=%s", stream, response.Code, calls, sessions.reads, response.Body.String())
		}
	}
}
