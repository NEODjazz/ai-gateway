package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

func TestChatPreservesSafeUpstreamFailureAfterRouting(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		wantCode   string
		retryAfter string
	}{
		{name: "rate limit", status: http.StatusTooManyRequests, wantCode: "upstream_rate_limited", retryAfter: "3"},
		{name: "authentication", status: http.StatusUnauthorized, wantCode: "upstream_authentication_failed"},
		{name: "unavailable", status: http.StatusServiceUnavailable, wantCode: "upstream_unavailable", retryAfter: "2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if test.retryAfter != "" {
					w.Header().Set("Retry-After", test.retryAfter)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"error":{"code":"upstream_detail","message":"private prompt and credential"}}`))
			}))
			t.Cleanup(upstream.Close)
			runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{
				Name: "remote", Type: "openai-compatible", BaseURL: upstream.URL,
				Models: []string{"model"}, Capabilities: []string{"chat"},
			}}})
			handler := Routes(NewHandler(modules.NewPipeline(nil), runtime))
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || response.Header().Get("Retry-After") != test.retryAfter || calls != 1 {
				t.Fatalf("status=%d retry-after=%q calls=%d", response.Code, response.Header().Get("Retry-After"), calls)
			}
			body := response.Body.String()
			if !strings.Contains(body, `"code":"`+test.wantCode+`"`) || strings.Contains(body, "upstream_detail") || strings.Contains(body, "private prompt") || strings.Contains(body, "credential") {
				t.Fatalf("unsafe gateway response: %s", body)
			}
		})
	}
}

func TestChatFailureReportsLastUpstreamAttempt(t *testing.T) {
	firstCalls, lastCalls := 0, 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls++
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(first.Close)
	last := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		lastCalls++
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(last.Close)
	runtime := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{
		{Name: "first", Type: "openai-compatible", BaseURL: first.URL, Models: []string{"model"}, Capabilities: []string{"chat"}, Priority: 1},
		{Name: "last", Type: "openai-compatible", BaseURL: last.URL, Models: []string{"model"}, Capabilities: []string{"chat"}, Priority: 2},
	}})
	handler := Routes(NewHandler(modules.NewPipeline(nil), runtime))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if firstCalls != 1 || lastCalls != 1 || response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "2" || !strings.Contains(response.Body.String(), `"code":"upstream_unavailable"`) {
		t.Fatalf("first=%d last=%d status=%d retry-after=%q body=%s", firstCalls, lastCalls, response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
}
