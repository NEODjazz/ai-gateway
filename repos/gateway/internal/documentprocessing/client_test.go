package documentprocessing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

const testOwner = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const testTaskID = "12345678-1234-1234-1234-123456789abc"

func TestClientAsyncContract(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Tenant-ID") != testOwner {
			t.Error("owner binding missing")
		}
		if r.Header.Get("X-Api-Key") != "internal-test-key" {
			t.Error("internal authentication missing")
		}
		switch r.URL.Path {
		case "/v1/convert/source/async":
			// Decode generic JSON to check the v1 source shape and safe options.
			var raw map[string]any
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatal(err)
			}
			source := raw["sources"].([]any)[0].(map[string]any)
			if source["base64_string"] != "JVBERi0=" || source["kind"] != "file" || source["filename"] != "document.pdf" {
				t.Fatalf("source=%v", source)
			}
			if raw["options"].(map[string]any)["abort_on_error"] != true {
				t.Fatal("partial conversion enabled")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"task_id": testTaskID, "task_status": "pending"})
		case "/v1/status/poll/" + testTaskID:
			_ = json.NewEncoder(w).Encode(map[string]string{"task_id": testTaskID, "task_status": "success"})
		case "/v1/result/" + testTaskID:
			_, _ = w.Write([]byte(`{"status":"success","document":{"md_content":"Page one\n\nPage two"},"errors":[]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{URL: server.URL, APIKey: "internal-test-key", Timeout: time.Second, PollInterval: time.Nanosecond, MaxTextBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	text, err := client.Convert(t.Context(), openai.ResponseFileAttachment{Filename: "private-name.pdf", Data: "JVBERi0="}, testOwner)
	if err != nil || text != "Page one\n\nPage two" || calls != 3 {
		t.Fatalf("text=%q calls=%d err=%v", text, calls, err)
	}
}

func TestClientFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"queue full", "private diagnostics", 503, ErrBusy},
		{"auth failed", "secret", 401, ErrUnavailable},
		{"partial", `{"status":"partial_success","document":{"md_content":"incomplete"}}`, 200, ErrFailed},
		{"errors", `{"status":"success","document":{"md_content":"text"},"errors":[{}]}`, 200, ErrFailed},
		{"empty", `{"status":"success","document":{"md_content":" "}}`, 200, ErrFailed},
		{"oversize", `{"status":"success","document":{"md_content":"` + strings.Repeat("x", 65) + `"}}`, 200, ErrTooLarge},
		{"invalid json", `not-json`, 200, ErrFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					_, _ = w.Write([]byte(`{"task_id":"` + testTaskID + `","task_status":"success"}`))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			client, _ := New(Config{URL: server.URL, APIKey: "internal-test-key", Timeout: time.Second, PollInterval: time.Nanosecond, MaxTextBytes: 64})
			_, err := client.Convert(t.Context(), openai.ResponseFileAttachment{}, testOwner)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private diagnostics") {
				t.Fatal("upstream error leaked")
			}
		})
	}
}

func TestClientCancellationAndRedirect(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { upstreamCalls++ }))
	t.Cleanup(upstream.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, upstream.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	client, _ := New(Config{URL: server.URL, APIKey: "internal-test-key", Timeout: time.Second, PollInterval: time.Nanosecond, MaxTextBytes: 64})
	if _, err := client.Convert(t.Context(), openai.ResponseFileAttachment{}, testOwner); !errors.Is(err, ErrUnavailable) || upstreamCalls != 0 {
		t.Fatalf("redirect calls=%d err=%v", upstreamCalls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.Convert(ctx, openai.ResponseFileAttachment{}, testOwner); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestClientRejectsUnsafeConfig(t *testing.T) {
	for _, address := range []string{"file:///tmp/input", "http://user:secret@localhost", "http://localhost?secret=1", "ftp://localhost", ""} {
		if _, err := New(Config{URL: address, APIKey: "test", Timeout: time.Second, PollInterval: time.Second, MaxTextBytes: 64}); err == nil {
			t.Fatalf("accepted %q", address)
		}
	}
}
