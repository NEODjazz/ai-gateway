package gateway

import (
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestIndependentExecutionsIgnoreRepeatedCorrelationID(t *testing.T) {
	var mu sync.Mutex
	events := map[string]map[string]int{}
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event modules.UsageRequest
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		if events[event.RequestID] == nil {
			events[event.RequestID] = map[string]int{}
		}
		events[event.RequestID][event.Phase]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer billing.Close()
	llm := provider.New(provider.Config{Modules: modules.NewPipeline([]modules.Module{modules.NewRemoteBillingModule(true, billing.URL)})})
	h := Routes(NewHandler(modules.NewPipeline(nil), llm))
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hello"}]}`))
		r.Header.Set("X-Request-ID", "same-correlation")
		r.Header.Set("X-Execution-ID", "client-supplied")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		id := w.Header().Get("X-Execution-ID")
		if w.Code != 200 || id == "" || id == "client-supplied" || seen[id] {
			t.Fatalf("execution not isolated: status=%d id=%q", w.Code, id)
		}
		seen[id] = true
		if w.Header().Get("X-Request-ID") != "same-correlation" {
			t.Fatal("correlation changed")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("independent executions merged in billing: %v", events)
	}
	for id, phases := range events {
		if !seen[id] || phases["reserve"] != 1 || phases["commit"] != 1 {
			t.Fatalf("invalid billing lifecycle: %s %v", id, phases)
		}
	}

}
func TestModernAndLegacyTPMRejectBeforeInference(t *testing.T) {
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		t.Run(field, func(t *testing.T) {
			llm := &chatProvider{}
			h := Routes(NewHandler(modules.NewPipeline([]modules.Module{accessPolicyModule{tpm: 100}}), llm))
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"test"}],"`+field+`":10000}`))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusTooManyRequests || llm.request.Request.Model != "" {
				t.Fatal("token limit bypassed")
			}
		})
	}
}
func TestUnversionedStylesAreNeverImmutable(t *testing.T) {
	mux := http.NewServeMux()
	registerAdminUI(mux)
	for _, name := range []string{"app.css", "app2.css", "app3.css"} {
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, httptest.NewRequest("GET", "/ui/assets/"+name, nil))
		if r.Code != http.StatusOK || strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("unversioned style cached: %s", name)
		}
	}
}
