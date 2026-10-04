package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func TestAgentMCPDurableLoopThroughNativeResponsesHTTPAdapter(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openai.ResponseRequest
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if r.URL.Path != "/v1/responses" || decoder.Decode(&request) != nil || len(request.Tools) != 1 {
			t.Error("invalid native Responses request")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		schema, _ := json.Marshal(request.Tools[0].Parameters)
		if !strings.Contains(string(schema), `"minimum":9007199254740993`) {
			t.Error("native adapter rounded schema constraint")
		}
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "response_one", "object": "response", "model": "test-model", "status": "completed", "output": []any{map[string]any{"id": "fc_one", "type": "function_call", "name": request.Tools[0].Name, "call_id": "call_native", "arguments": `{"city":"Paris","count":9007199254740993}`}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}})
			return
		}
		encoded, _ := json.Marshal(request.Input)
		if !strings.Contains(string(encoded), "function_call_output") || !strings.Contains(string(encoded), "sunny") {
			t.Error("native adapter lost the actual MCP output")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "response_two", "object": "response", "model": "test-model", "status": "completed", "output": []any{map[string]any{"id": "msg_two", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Native final answer", "annotations": []any{}}}}}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 4, "total_tokens": 24}})
	}))
	t.Cleanup(upstream.Close)
	h, _, client := agentMCPApprovalHandler(t)
	client.page.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},"count":{"type":"integer","minimum":9007199254740993}},"required":["city"]}`)
	billing := &mcpBillingRecorder{}
	pipeline := modules.NewPipeline([]modules.Module{mcpRuntimeAuth{}, billing})
	llm, err := provider.NewWithError(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"test-model"}, Capabilities: []string{"responses", "tools"}}}, Modules: pipeline})
	if err != nil {
		t.Fatal(err)
	}
	h.provider, h.pipeline = llm, pipeline
	task := agentMCPResultTask(t, agentMCPSend(h, "SendMessage", ""))
	if task.Status.State != "TASK_STATE_INPUT_REQUIRED" || client.callCalls != 0 {
		t.Fatal("native tool request was not paused")
	}
	completed := agentMCPResultTask(t, agentMCPTaskRPC(h, "SendMessage", agentMCPDecision(t, task, true, "decision"), ""))
	if completed.Status.State != "TASK_STATE_COMPLETED" || calls.Load() != 2 || client.callCalls != 1 || *completed.Artifacts[0].Parts[0].Text != "Native final answer" {
		t.Fatal("native Responses adapter did not finish the MCP loop")
	}
	if client.callArgs["count"] != json.Number("9007199254740993") {
		t.Fatal("durable approval rounded integer tool arguments")
	}
	var commits int
	for _, phase := range billing.phases {
		if phase == "commit" {
			commits++
		}
	}
	if commits < 3 {
		t.Fatalf("actual provider/tool settlement missing: phases=%v", billing.phases)
	}
}
