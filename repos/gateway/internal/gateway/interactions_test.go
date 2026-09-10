package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

func TestInteractionsUsesResponsesPolicyRoutingAndBilling(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["model"] != "upstream" || body["instructions"] != "be concise" || body["previous_response_id"] != "resp_previous" || body["max_output_tokens"] != float64(32) || body["store"] != false || len(body["tools"].([]any)) != 1 {
			t.Errorf("mapped request=%#v", body)
		}
		text, _ := body["text"].(map[string]any)
		if text["format"] == nil {
			t.Errorf("response format lost: %#v", text)
		}
		_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"upstream","output":[{"id":"call","type":"function_call","call_id":"call_1","name":"weather","arguments":"{\"city\":\"Paris\"}"},{"id":"message","type":"message","role":"assistant","content":[{"type":"output_text","text":"sunny"}]}],"usage":{"input_tokens":7,"output_tokens":5,"total_tokens":12,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":3}}}`)
	}))
	defer upstream.Close()
	recorder := &statelessUsageRecorder{}
	router := provider.New(provider.Config{
		Endpoints: []config.ProviderEndpointConfig{{Name: "deployment", Type: "openai-compatible", BaseURL: upstream.URL, Models: []string{"public"}, ModelAliases: map[string]string{"public": "upstream"}, Capabilities: []string{"responses", "tools", "structured_output"}}},
		Modules:   modules.NewPipeline([]modules.Module{recorder}),
	})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"public"}, tools: []string{"weather"}}}}), router))
	body := `{"provider":"deployment","model":"public","input":"hello","system_instruction":"be concise","previous_interaction_id":"resp_previous","store":false,"tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}],"response_format":{"type":"json_schema","name":"answer","schema":{"type":"object"}},"generation_config":{"max_output_tokens":32}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer gateway-test-key")
	request.Header.Set("X-Request-ID", "external-correlation")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Execution-ID") == "" || !strings.Contains(response.Body.String(), `"object":"interaction"`) || !strings.Contains(response.Body.String(), `"type":"function_call"`) || !strings.Contains(response.Body.String(), `"text":"sunny"`) || !strings.Contains(response.Body.String(), `"total_tokens":12`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if calls.Load() != 1 || len(recorder.totals) != 1 || recorder.totals[0] != 12 || recorder.ids[0] == "" {
		t.Fatalf("calls=%d ids=%v totals=%v", calls.Load(), recorder.ids, recorder.totals)
	}
}

func TestInteractionsRejectsUnsupportedModesBeforeExecution(t *testing.T) {
	for _, field := range []string{`"stream":true`, `"background":true`, `"agent":"research"`, `"generation_config":{"seed":1}`, `"unknown":true`} {
		response := httptest.NewRecorder()
		Handler{}.Interactions(response, httptest.NewRequest(http.MethodPost, "/v1/interactions", strings.NewReader(`{"model":"model","input":"hello",`+field+`}`)))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("field=%s status=%d body=%s", field, response.Code, response.Body.String())
		}
	}
}
