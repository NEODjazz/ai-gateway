package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCacheDiagnosticsSeparatesExactAndSemantic(t *testing.T) {
	metrics := NewMetrics()
	metrics.ObserveCache("get", "hit")
	metrics.ObserveCache("get", "miss")
	metrics.ObserveCache("set", "ok")
	metrics.ObserveCache("semantic_get", "hit")
	metrics.ObserveCache("semantic_get", "hit")
	metrics.ObserveCache("semantic_get", "miss")
	metrics.ObserveCache("semantic_set", "error")
	handler := NewHandlerWithMetrics(modulesPipeline("admin"), nil, nil, nil, metrics).WithCacheDiagnostics(CacheRuntimeConfig{ExactTTLSeconds: 60, ExactMaxBytes: 1024, SemanticTTLSeconds: 120, SemanticMaxEntries: 10, SemanticMaxBytes: 2048})
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/cache/diagnostics", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"hit_ratio":0.5`) || !strings.Contains(body, `"hit_ratio":0.6666666666666666`) || !strings.Contains(body, `"semantic_get":{"hit":2,"miss":1}`) {
		t.Fatalf("unexpected cache diagnostics: status=%d body=%s", response.Code, body)
	}
}
