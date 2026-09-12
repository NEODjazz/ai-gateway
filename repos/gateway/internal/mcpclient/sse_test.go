package mcpclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestLegacySSEInitializationListAndCall(t *testing.T) {
	events := make(chan string, 8)
	var posts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer server-secret" || r.Header.Get("MCP-Protocol-Version") != LegacyProtocolVersion {
			t.Fatalf("headers=%v", r.Header)
		}
		if r.Method == http.MethodGet {
			if r.URL.Path != "/sse" || r.Header.Get("Accept") != "text/event-stream" {
				t.Fatalf("GET %s headers=%v", r.URL.String(), r.Header)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: endpoint\ndata: /message?session=test\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case event := <-events:
					_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", event)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		if r.Method != http.MethodPost || r.URL.Path != "/message" || r.URL.Query().Get("session") != "test" {
			t.Fatalf("POST %s", r.URL.String())
		}
		posts.Add(1)
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		switch request.Method {
		case "initialize":
			events <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%q}}`, request.ID, LegacyProtocolVersion)
		case "notifications/initialized":
		case "tools/list":
			events <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"weather","inputSchema":{"type":"object"}}]}}`, request.ID)
		case "tools/call":
			if !strings.Contains(string(request.Params), `"name":"weather"`) {
				t.Fatalf("params=%s", request.Params)
			}
			events <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"sunny"}]}}`, request.ID)
		default:
			t.Fatalf("method=%s", request.Method)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	client, err := NewSSE(server.URL+"/sse", "server-secret")
	if err != nil {
		t.Fatal(err)
	}
	client.http = server.Client()
	defer client.Close()
	page, err := client.ListTools(t.Context(), "")
	if err != nil || len(page.Tools) != 1 || page.Tools[0].Name != "weather" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	result, err := client.CallTool(t.Context(), "weather", map[string]any{"city": "Paris"})
	if err != nil || len(result.Content) != 1 || !strings.Contains(string(result.Content[0]), "sunny") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if posts.Load() != 4 {
		t.Fatalf("posts=%d", posts.Load())
	}
}

func TestLegacySSERejectsCrossOriginAndUnsafeMessageEndpoints(t *testing.T) {
	client, err := NewSSE("https://mcp.example.test/sse", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, endpoint := range []string{"https://other.example.test/message", "//other.example.test/message", "https://user@mcp.example.test/message", "/message#fragment", "/message\nX-Test: value", strings.Repeat("x", 4097)} {
		if _, err := client.resolvePostEndpoint(endpoint); err == nil {
			t.Fatalf("accepted endpoint %q", endpoint)
		}
	}
	resolved, err := client.resolvePostEndpoint("/message?session=opaque")
	if err != nil || resolved.String() != "https://mcp.example.test/message?session=opaque" {
		t.Fatalf("resolved=%v err=%v", resolved, err)
	}
}

func TestLegacySSERequiresEndpointAsFirstEvent(t *testing.T) {
	client, err := NewSSE("https://mcp.example.test/sse", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	endpoint := make(chan string, 1)
	client.readStream(io.NopCloser(strings.NewReader("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")), endpoint)
	select {
	case err := <-client.streamErrors:
		if !strings.Contains(err.Error(), "did not start") {
			t.Fatalf("error=%v", err)
		}
	default:
		t.Fatal("missing stream validation error")
	}
}

func TestLegacySSEBoundsMultilineEvent(t *testing.T) {
	client, err := NewSSE("https://mcp.example.test/sse", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	endpoint := make(chan string, 1)
	var event strings.Builder
	event.WriteString("event: endpoint\n")
	line := strings.Repeat("x", 64<<10)
	for event.Len() <= maxBodyBytes {
		event.WriteString("data: ")
		event.WriteString(line)
		event.WriteByte('\n')
	}
	event.WriteByte('\n')
	client.readStream(io.NopCloser(strings.NewReader(event.String())), endpoint)
	select {
	case err := <-client.streamErrors:
		if !strings.Contains(err.Error(), "exceeds limit") {
			t.Fatalf("error=%v", err)
		}
	default:
		t.Fatal("missing event size error")
	}
}
