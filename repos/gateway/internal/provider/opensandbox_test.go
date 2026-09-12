package provider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOpenSandboxExecutesEphemeralLifecycleAndDeletesRuntime(t *testing.T) {
	var server *httptest.Server
	var mu sync.Mutex
	calls := []string{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/sandboxes") && r.Header.Get("OPEN-SANDBOX-API-KEY") != "secret" {
			t.Errorf("missing lifecycle credential on %s", r.URL.Path)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["networkPolicy"] == nil || body["timeout"] != float64(20) {
				t.Errorf("unexpected create body: %#v", body)
			}
			_, _ = io.WriteString(w, `{"id":"sandbox_1"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/sandbox_1":
			_, _ = io.WriteString(w, `{"status":{"state":"Running"}}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/endpoints/44772"):
			if r.URL.Query().Get("use_server_proxy") != "true" {
				t.Error("server proxy was not requested")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"endpoint": server.URL, "headers": map[string]string{"Authorization": "Bearer runtime"}})
		case r.Method == http.MethodPost && r.URL.Path == "/code":
			if r.Header.Get("Authorization") != "Bearer runtime" {
				t.Error("execution credential missing")
			}
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["code"] != "print(42)" {
				t.Errorf("unexpected execution body: %#v", body)
			}
			_, _ = io.WriteString(w, "event: output\ndata: {\"type\":\"stdout\",\"text\":\"42\\n\"}\n\ndata: {\"type\":\"execution_count\",\"execution_count\":1}\n")
		case r.Method == http.MethodDelete && r.URL.Path == "/sandboxes/sandbox_1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewOpenSandbox(server.URL, "secret")
	result, err := client.ExecuteSandbox(t.Context(), SandboxExecuteRequest{Code: "print(42)", Language: "python", Template: defaultSandboxTemplate, TimeoutSeconds: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Object != "code_execution" || result.Stdout != "42\n" || result.ExecutionCount == nil || *result.ExecutionCount != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "POST /sandboxes,GET /sandboxes/sandbox_1,GET /sandboxes/sandbox_1/endpoints/44772?use_server_proxy=true,POST /code,DELETE /sandboxes/sandbox_1"
	if strings.Join(calls, ",") != want {
		t.Fatalf("calls=%s", strings.Join(calls, ","))
	}
}

func TestOpenSandboxDeletesRuntimeAfterUnsafeExecutionEndpoint(t *testing.T) {
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
			_, _ = io.WriteString(w, `{"id":"sandbox_1"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/sandbox_1":
			_, _ = io.WriteString(w, `{"status":{"state":"Running"}}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/endpoints/44772"):
			_, _ = io.WriteString(w, `{"endpoint":"https://attacker.example/execute"}`)
		case r.Method == http.MethodDelete:
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	_, err := NewOpenSandbox(server.URL, "").ExecuteSandbox(t.Context(), SandboxExecuteRequest{Code: "x", Language: "python", Template: defaultSandboxTemplate, TimeoutSeconds: 1})
	if err == nil || !strings.Contains(err.Error(), "outside the configured provider domain") || !deleted.Load() {
		t.Fatalf("err=%v deleted=%v", err, deleted.Load())
	}
}

func TestOpenSandboxBoundsOutputAndValidatesInputs(t *testing.T) {
	if validateSandboxExecuteRequest(SandboxExecuteRequest{}) == nil {
		t.Fatal("empty request accepted")
	}
	if _, err := readSandboxBounded(strings.NewReader(strings.Repeat("x", 11)), 10); err == nil {
		t.Fatal("oversized output accepted")
	}
	if validSandboxHeader("Host", "example.test") || validSandboxHeader("X-Test", "ok\r\nbad") {
		t.Fatal("unsafe execution header accepted")
	}
}
