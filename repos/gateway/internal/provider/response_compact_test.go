package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type compactLimitReader struct{ read int }

func (r *compactLimitReader) Read(buffer []byte) (int, error) {
	remaining := maxResponseJSONBytes + 1 - r.read
	if remaining <= 0 {
		return 0, io.EOF
	}
	if len(buffer) > remaining {
		buffer = buffer[:remaining]
	}
	for index := range buffer {
		buffer[index] = 'x'
	}
	r.read += len(buffer)
	return len(buffer), nil
}

type compactTestClient struct {
	calls   int
	request openai.ResponseCompactRequest
	result  openai.CompactedResponse
}

func (*compactTestClient) ChatCompletions(context.Context, openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return openai.ChatCompletionResponse{}, nil
}

func (*compactTestClient) Responses(context.Context, openai.ResponseRequest) (openai.ResponseResponse, error) {
	return openai.ResponseResponse{}, nil
}

func (c *compactTestClient) CompactResponse(_ context.Context, request openai.ResponseCompactRequest) (openai.CompactedResponse, error) {
	c.calls++
	c.request = request
	return c.result, nil
}

type compactPostModule struct {
	pre, post int
	response  *openai.CompactedResponse
}

type compactAnonymizerModule struct{}

func (compactAnonymizerModule) Name() string   { return "anonymizer" }
func (compactAnonymizerModule) Required() bool { return true }
func (compactAnonymizerModule) Handle(_ context.Context, req *modules.RequestContext) error {
	req.AnonymizationValues = map[string]string{"{{VALUE_1}}": "private"}
	return nil
}

func (*compactPostModule) Name() string   { return "compact-test" }
func (*compactPostModule) Required() bool { return true }
func (m *compactPostModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.pre++
	req.ResponseRequest.Input = "transformed"
	return nil
}
func (*compactPostModule) PostResponseEnabled() bool { return true }
func (m *compactPostModule) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	m.post++
	m.response = req.CompactedResponse
	return nil
}

func TestRouterCompactResponseRunsModulesAndPreservesUsage(t *testing.T) {
	client := &compactTestClient{result: openai.CompactedResponse{
		ID: "cmp_1", Object: "response.compaction", Output: []json.RawMessage{json.RawMessage(`{"type":"compaction","encrypted_content":"opaque"}`)},
		Usage: openai.ResponseUsage{InputTokens: 21, OutputTokens: 4, TotalTokens: 25},
	}}
	module := &compactPostModule{}
	router := Router{
		endpoints: []Endpoint{{Name: "compact", Type: "openai-compatible", Provider: client, Admission: newAdmissionController(0, 0, 0)}},
		modules:   modules.NewPipeline([]modules.Module{module}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ResponseRequest{Model: "model", Input: "original", Instructions: "shorten"}
	response, err := router.CompactResponse(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request})
	if err != nil {
		t.Fatal(err)
	}
	if module.pre != 1 || module.post != 1 || module.response == nil || module.response.Usage.TotalTokens != 25 {
		t.Fatalf("module lifecycle was not completed: %+v response=%+v", module, response)
	}
	if client.calls != 1 || client.request.Input != "transformed" || client.request.Instructions != "shorten" {
		t.Fatalf("effective request was not sent: %+v", client.request)
	}
}

func TestRouterCompactResponseRequiresAdapterSupport(t *testing.T) {
	client := &affinityResponseClient{id: "regular"}
	router := Router{
		endpoints: []Endpoint{{Name: "regular", Type: "demo", Provider: client, Admission: newAdmissionController(0, 0, 0)}},
		modules:   modules.NewPipeline(nil), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ResponseRequest{Model: "model", Input: "input"}
	_, err := router.CompactResponse(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request})
	if !errors.Is(err, ErrResponseCompactionUnsupported) || client.calls != 0 {
		t.Fatalf("unsupported adapter was called or wrong error returned: calls=%d err=%v", client.calls, err)
	}
}

func TestRouterCompactResponseRejectsAnonymizedState(t *testing.T) {
	client := &compactTestClient{result: openai.CompactedResponse{
		ID: "cmp_1", Object: "response.compaction", Output: []json.RawMessage{json.RawMessage(`{"type":"compaction"}`)},
	}}
	router := Router{
		endpoints: []Endpoint{{Name: "compact", Type: "openai-compatible", Provider: client, Admission: newAdmissionController(0, 0, 0)}},
		modules:   modules.NewPipeline([]modules.Module{compactAnonymizerModule{}}), health: newEndpointHealthTracker(), routeCounter: &atomic.Uint64{},
	}
	request := openai.ResponseRequest{Model: "model", Input: "private"}
	_, err := router.CompactResponse(t.Context(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "model"}, ResponseRequest: &request})
	if !errors.Is(err, ErrResponseCompactionAnonymized) || client.calls != 0 {
		t.Fatalf("anonymized state was sent upstream: calls=%d err=%v", client.calls, err)
	}
}

func TestCompactedResponseDecoderEnforcesBodyLimitAndFinalItem(t *testing.T) {
	reader := &compactLimitReader{}
	if _, err := decodeCompactedResponse(reader); err == nil || reader.read != maxResponseJSONBytes+1 {
		t.Fatalf("read=%d err=%v", reader.read, err)
	}
	invalid := `{"id":"cmp_1","object":"response.compaction","output":[{"type":"message"}],"usage":{"total_tokens":1}}`
	if _, err := decodeCompactedResponse(strings.NewReader(invalid)); err == nil {
		t.Fatal("compacted response without final compaction item was accepted")
	}
}

func TestCompactedResponseRequiresExactUsage(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		wantError   bool
	}{
		{"missing", "", true},
		{"null", `,"usage":null`, true},
		{"input missing", `,"usage":{"output_tokens":2,"total_tokens":2}`, true},
		{"output missing", `,"usage":{"input_tokens":3,"total_tokens":3}`, true},
		{"total missing", `,"usage":{"input_tokens":3,"output_tokens":2}`, true},
		{"inconsistent", `,"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":6}`, true},
		{"zero", `,"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, false},
		{"reported", `,"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"id":"cmp_1","object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque"}]` + tc.usage + `}`
			response, err := decodeCompactedResponse(strings.NewReader(body))
			if tc.wantError && err == nil {
				t.Fatalf("invalid usage accepted: %+v", response)
			}
			if !tc.wantError && (err != nil || response.Usage.TotalTokens != response.Usage.InputTokens+response.Usage.OutputTokens) {
				t.Fatalf("valid usage rejected: response=%+v err=%v", response, err)
			}
		})
	}
}

func TestAzureCompactResponseRequiresExactUsage(t *testing.T) {
	for _, tc := range []struct {
		name, basePath, authType, wantPath string
	}{
		{name: "resource API key", authType: "api_key", wantPath: "/openai/v1/responses/compact"},
		{name: "Foundry Entra", basePath: "/api/projects/project-a", authType: "entra", wantPath: "/api/projects/project-a/openai/v1/responses/compact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.wantPath || tc.authType == "api_key" && r.Header.Get("api-key") != "credential" ||
					tc.authType == "entra" && r.Header.Get("Authorization") != "Bearer credential" {
					t.Errorf("unexpected Azure compact request: path=%s headers=%v", r.URL.Path, r.Header)
				}
				_, _ = fmt.Fprint(w, `{"id":"cmp_1","object":"response.compaction","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":3,"total_tokens":3}}`)
			}))
			t.Cleanup(server.Close)
			client := NewAzureOpenAI(server.URL+tc.basePath, "credential", false, "", tc.authType)
			_, err := client.CompactResponse(t.Context(), openai.ResponseCompactRequest{Model: "deployment", Input: "hello"})
			if err == nil || !strings.Contains(err.Error(), "usage") {
				t.Fatalf("incomplete Azure compact usage was accepted: %v", err)
			}
		})
	}
}
