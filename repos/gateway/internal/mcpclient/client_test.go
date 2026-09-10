package mcpclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStreamableHTTPInitializationListAndCall(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Accept") != "application/json, text/event-stream" || r.Header.Get("MCP-Protocol-Version") != ProtocolVersion {
			t.Fatalf("request=%s headers=%v", r.Method, r.Header)
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "session-1" {
			t.Fatalf("missing session for %s", request.Method)
		}
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-1")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"%s","capabilities":{"tools":{}}}}`, request.ID, ProtocolVersion)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tools\":[{\"name\":\"weather\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n", request.ID)
		case "tools/call":
			if !strings.Contains(string(request.Params), `"name":"weather"`) {
				t.Fatalf("params=%s", request.Params)
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"sunny"}],"structuredContent":{"temperature":22}}}`, request.ID)
		default:
			t.Fatalf("method=%s", request.Method)
		}
	}))
	defer server.Close()
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	page, err := client.ListTools(t.Context(), "")
	if err != nil || len(page.Tools) != 1 || page.Tools[0].Name != "weather" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	result, err := client.CallTool(t.Context(), "weather", map[string]any{"city": "Paris"})
	if err != nil || result.IsError || len(result.Content) != 1 || !strings.Contains(string(result.Content[0]), "sunny") || !strings.Contains(string(result.StructuredContent), "22") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if requests.Load() != 4 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestClientRejectsUnsafeEndpointsAndAddresses(t *testing.T) {
	for _, endpoint := range []string{"http://example.com/mcp", "https://user@example.com/mcp", "https://example.com/mcp?token=x", "https://example.com/mcp#fragment"} {
		if _, err := New(endpoint); err == nil {
			t.Fatalf("accepted endpoint %q", endpoint)
		}
	}
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "::1", "fc00::1"} {
		if publicIP(net.ParseIP(address)) {
			t.Fatalf("accepted non-public address %s", address)
		}
	}
	if !publicIP(net.ParseIP("203.0.113.10")) {
		t.Fatal("public address rejected")
	}
	for _, session := range []string{"", "has space", "line\nbreak", strings.Repeat("x", 1025)} {
		if validSessionID(session) {
			t.Fatalf("accepted session ID %q", session)
		}
	}
}

func TestClientRejectsMismatchedAndOversizedResponses(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":999,"result":{"protocolVersion":"2025-06-18"}}`)
	}))
	defer server.Close()
	client, _ := New(server.URL)
	client.http = server.Client()
	if err := client.Initialize(t.Context()); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err=%v", err)
	}
	if err := decodeBoundedJSON(strings.NewReader(strings.Repeat("x", maxBodyBytes+1)), &map[string]any{}); err == nil {
		t.Fatal("oversized response accepted")
	}
	ready := &Client{ready: true}
	if _, err := ready.CallTool(t.Context(), "tool", map[string]any{"payload": strings.Repeat("x", maxRequestBytes)}); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversized request err=%v", err)
	}
}

func TestToolSchemasAndAnnotationsMustBeObjects(t *testing.T) {
	for _, test := range []struct {
		raw         string
		requireType bool
		valid       bool
	}{
		{raw: `{"type":"object","properties":{}}`, requireType: true, valid: true},
		{raw: `{"type":"array"}`, requireType: true},
		{raw: `null`, requireType: false},
		{raw: `[]`, requireType: false},
		{raw: `{"readOnlyHint":true}`, requireType: false, valid: true},
	} {
		if got := validJSONObject(json.RawMessage(test.raw), test.requireType); got != test.valid {
			t.Fatalf("raw=%s requireType=%t got=%t", test.raw, test.requireType, got)
		}
	}
}
