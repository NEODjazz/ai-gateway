package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func generateCall(handler http.Handler, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", path, strings.NewReader(body))
	request.Header.Set("x-goog-api-key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestGenerateContentParsesVersionedModelIDs(t *testing.T) {
	for _, test := range []struct {
		value, model, action string
		valid                bool
	}{
		{"phi3:latest:generateContent", "phi3:latest", "generateContent", true},
		{"publishers/acme/models/family:2026:streamGenerateContent", "publishers/acme/models/family:2026", "streamGenerateContent", true},
		{"model:countTokens", "model", "countTokens", true},
		{"model:unknown", "", "", false},
		{":generateContent", "", "", false},
	} {
		model, action, valid := generateModelAction(test.value)
		if model != test.model || action != test.action || valid != test.valid {
			t.Fatalf("%q parsed as model=%q action=%q valid=%t", test.value, model, action, valid)
		}
	}
}

func TestGenerateContentRoutesModelIDContainingColon(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "id", Model: "phi3:latest", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}, Usage: openai.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}}}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	response := generateCall(handler, "/v1beta/models/phi3:latest:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`, "")
	if response.Code != http.StatusOK || upstream.calls != 1 || upstream.request.Request.Model != "phi3:latest" {
		t.Fatalf("status=%d calls=%d request=%+v body=%s", response.Code, upstream.calls, upstream.request.Request, response.Body.String())
	}
}
func TestGenerateContentNativeJSON(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "id", Model: "m", ServiceTier: "standard", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}, ExtraContent: &openai.ToolCallExtraContent{Google: &openai.GoogleToolCallContent{ThoughtSignature: "opaque"}}}}}, FinishReason: "tool_calls"}}, Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 7, TotalTokens: 17, CompletionTokensDetails: &openai.CompletionTokenDetails{ReasoningTokens: 4}}}}
	handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
	response := generateCall(handler, "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}],"serviceTier":"standard","store":true,"generationConfig":{"maxOutputTokens":20,"topK":10,"presencePenalty":0.2,"frequencyPenalty":-0.1,"responseLogprobs":true,"logprobs":3,"responseModalities":["TEXT"],"candidateCount":1}}`, "")
	request := upstream.request.Request
	if response.Code != 200 || upstream.calls != 1 || request.MaxCompletionTokens == nil || *request.MaxCompletionTokens != 20 || request.TopK == nil || *request.TopK != 10 || request.PresencePenalty == nil || *request.PresencePenalty != 0.2 || request.FrequencyPenalty == nil || *request.FrequencyPenalty != -0.1 || request.Logprobs == nil || !*request.Logprobs || request.TopLogprobs == nil || *request.TopLogprobs != 3 || request.N != nil || request.Modalities != nil || request.ServiceTier != "standard_only" || request.Store == nil || !*request.Store {
		t.Fatalf("request: %d %s", response.Code, response.Body.String())
	}
	for _, want := range []string{`"functionCall"`, `"thoughtSignature":"opaque"`, `"finishReason":"STOP"`, `"thoughtsTokenCount":4`, `"candidatesTokenCount":3`, `"totalTokenCount":17`, `"serviceTier":"standard"`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("missing %s: %s", want, response.Body.String())
		}
	}
}

func TestGenerateUsageMapsServiceTier(t *testing.T) {
	usage := openai.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
	for internal, native := range map[string]string{"auto": "unspecified", "default": "unspecified", "standard": "standard", "standard_only": "standard", "flex": "flex", "priority": "priority"} {
		t.Run(internal, func(t *testing.T) {
			result, err := generateUsage(usage, internal)
			if err != nil || result["serviceTier"] != native {
				t.Fatalf("serviceTier=%v err=%v", result["serviceTier"], err)
			}
		})
	}
	if _, err := generateUsage(usage, "unknown"); err == nil {
		t.Fatal("invalid service tier was accepted")
	}
	usage.PromptTokens = 5
	usage.ProviderToolInputTokens = 3
	usage.TotalTokens = 6
	result, err := generateUsage(usage, "")
	if err != nil || result["promptTokenCount"] != 2 || result["toolUsePromptTokenCount"] != 3 {
		t.Fatalf("tool usage=%v err=%v", result, err)
	}
	usage.ProviderToolInputTokens = 6
	if _, err := generateUsage(usage, ""); err == nil {
		t.Fatal("tool input usage above total prompt usage was accepted")
	}
}

func TestGenerateContentResolvesOwnedFileDataAfterAuthentication(t *testing.T) {
	identity := modules.RequestContext{CredentialID: "credential", UserID: "user"}
	owner := fileOwnerKey(identity)
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_image": {ID: "file_image", OwnerKey: owner, Filename: "image.png", Purpose: "user_data", ContentType: "image/png", Bytes: 12, Content: []byte("\x89PNG\r\n\x1a\nbody")},
		"file_pdf":   {ID: "file_pdf", OwnerKey: owner, Filename: "report.pdf", Purpose: "user_data", ContentType: "application/pdf", Bytes: 12, Content: []byte("%PDF-1.7\nrow")},
		"file_text":  {ID: "file_text", OwnerKey: owner, Filename: "notes.txt", Purpose: "user_data", ContentType: "text/plain", Bytes: 5, Content: []byte("notes")},
		"file_audio": {ID: "file_audio", OwnerKey: owner, Filename: "audio.wav", Purpose: "user_data", ContentType: "audio/wav", Bytes: 12, Content: []byte("RIFF\x00\x00\x00\x00WAVE")},
		"file_video": {ID: "file_video", OwnerKey: owner, Filename: "video.mp4", Purpose: "user_data", ContentType: "video/mp4", Bytes: 12, Content: []byte("\x00\x00\x00\x0cftypmp42")},
		"file_avi":   {ID: "file_avi", OwnerKey: owner, Filename: "video.avi", Purpose: "user_data", ContentType: "video/avi", Bytes: 19, Content: []byte("RIFF\x00\x00\x00\x00AVI payload")},
		"file_other": {ID: "file_other", OwnerKey: "another-owner", Filename: "other.png", Purpose: "user_data", ContentType: "image/png", Bytes: 12, Content: []byte("\x89PNG\r\n\x1a\nbody")},
	}}
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "id", Model: "m", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}, Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&lifecycleAuthModule{}}), upstream).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 32 << 20, OwnerQuotaBytes: 64 << 20}))
	body := `{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"file_image"}},{"fileData":{"mimeType":"application/pdf","fileUri":"file_pdf"},"mediaResolution":{"level":"MEDIA_RESOLUTION_ULTRA_HIGH"}},{"fileData":{"mimeType":"text/plain","fileUri":"file_text"}},{"fileData":{"mimeType":"audio/wav","fileUri":"file_audio"}},{"fileData":{"mimeType":"video/mp4","fileUri":"file_video"},"mediaProcessing":"AGENTIC"},{"fileData":{"mimeType":"video/avi","fileUri":"file_avi"}}]}]}`
	response := generateCall(handler, "/v1beta/models/m:generateContent", body, "gateway-test-key")
	request := upstream.request.Request
	images, imageErr := openai.ChatImageAttachments(request.Messages)
	filesFound, fileErr := openai.ChatFileAttachments(request.Messages)
	audio, audioErr := openai.ChatAudioAttachments(request.Messages)
	videos, videoErr := openai.ChatVideoAttachments(request.Messages)
	parts := request.Messages[0].Content.([]any)
	resolution, resolutionErr := openai.GeminiPartMediaResolution(parts[1].(map[string]any))
	processing, processingErr := openai.GeminiPartMediaProcessing(parts[4].(map[string]any))
	if response.Code != http.StatusOK || upstream.calls != 1 || imageErr != nil || len(images) != 1 || fileErr != nil || len(filesFound) != 1 || !openai.HasChatTextDocuments(request) || audioErr != nil || len(audio) != 1 || videoErr != nil || len(videos) != 2 || videos[1].MediaType != "video/avi" || resolutionErr != nil || resolution == nil || resolution.Level != "MEDIA_RESOLUTION_ULTRA_HIGH" || processingErr != nil || processing != "AGENTIC" {
		t.Fatalf("status=%d calls=%d images=%d/%v files=%d/%v audio=%d/%v videos=%d/%v request=%+v body=%s", response.Code, upstream.calls, len(images), imageErr, len(filesFound), fileErr, len(audio), audioErr, len(videos), videoErr, request, response.Body.String())
	}

	mismatch := generateCall(handler, "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"fileData":{"mimeType":"image/jpeg","fileUri":"file_image"}}]}]}`, "gateway-test-key")
	if mismatch.Code != http.StatusBadRequest || upstream.calls != 1 {
		t.Fatalf("mismatch status=%d calls=%d body=%s", mismatch.Code, upstream.calls, mismatch.Body.String())
	}
	foreign := generateCall(handler, "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"file_other"}}]}]}`, "gateway-test-key")
	if foreign.Code != http.StatusBadRequest || upstream.calls != 1 || !strings.Contains(foreign.Body.String(), "unavailable") {
		t.Fatalf("foreign status=%d calls=%d body=%s", foreign.Code, upstream.calls, foreign.Body.String())
	}
	unauthorized := Routes(NewHandler(modules.NewPipeline([]modules.Module{rejectingMessagesAuth{}}), upstream))
	rejected := generateCall(unauthorized, "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"file_missing"}}]}]}`, "gateway-test-key")
	if rejected.Code != http.StatusUnauthorized || upstream.calls != 1 {
		t.Fatalf("unauthorized status=%d calls=%d body=%s", rejected.Code, upstream.calls, rejected.Body.String())
	}
}

func TestGenerateContentReturnsThoughtParts(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "id", Model: "m", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "answer", Reasoning: []openai.ReasoningBlock{{Type: "thinking", Thinking: "private plan", Signature: "c2lnbmVk"}}}, FinishReason: "stop"}}, Usage: openai.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}}}
	response := generateCall(Routes(NewHandler(modules.NewPipeline(nil), upstream)), "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"text":"question"}]}]}`, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"text":"private plan","thought":true,"thoughtSignature":"c2lnbmVk"`) || !strings.Contains(response.Body.String(), `"text":"answer"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGenerateContentReturnsTextPartSignature(t *testing.T) {
	native, err := openai.AddGeminiPartSignature(nil, 0, "c2lnbmVk")
	if err != nil {
		t.Fatal(err)
	}
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "id", Model: "m", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "answer", NativeContent: native}, FinishReason: "stop"}}, Usage: openai.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}}}
	response := generateCall(Routes(NewHandler(modules.NewPipeline(nil), upstream)), "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"text":"question"}]}]}`, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"text":"answer","thoughtSignature":"c2lnbmVk"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGenerateContentPreservesGroundingMetadata(t *testing.T) {
	metadata := json.RawMessage(`{"webSearchQueries":["weather"],"searchEntryPoint":{"renderedContent":"<div>Search</div>"}}`)
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "id", Model: "m", Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "answer"}, FinishReason: "stop", GeminiGroundingMetadata: metadata}}, Usage: openai.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2, SearchRequests: 1}}}
	response := generateCall(Routes(NewHandler(modules.NewPipeline(nil), upstream)), "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"text":"question"}]}],"tools":[{"googleSearch":{}}]}`, "")
	if response.Code != http.StatusOK || upstream.request.Request.WebSearchOptions == nil || !strings.Contains(response.Body.String(), `"groundingMetadata":{"webSearchQueries":["weather"],"searchEntryPoint"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGenerateStreamEmitsFinalTextPartSignature(t *testing.T) {
	native, err := openai.AddGeminiPartSignature(nil, 0, "c2lnbmVk")
	if err != nil {
		t.Fatal(err)
	}
	destination := httptest.NewRecorder()
	w := &generateWriter{destination: destination, headers: make(http.Header)}
	w.Header().Set("Content-Type", "text/event-stream")
	if err := w.chunk(`{"id":"id","model":"m","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`); err != nil {
		t.Fatal(err)
	}
	w.chatStreamResult(openai.ChatCompletionResponse{ID: "id", Model: "m", Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "answer", NativeContent: native}, FinishReason: "stop"}}, Usage: openai.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}})
	if err := w.chunk("[DONE]"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(destination.Body.String(), `"text":"","thoughtSignature":"c2lnbmVk"`) {
		t.Fatalf("body=%s", destination.Body.String())
	}
}
func TestGenerateContentAuthQuotasAndUnsupportedParameters(t *testing.T) {
	for _, tc := range []struct {
		path, body, key string
		policy          accessPolicyModule
		code            int
	}{
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}]}`, code: 401},
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}]}`, key: "gateway-test-key", policy: accessPolicyModule{models: []string{"other"}}, code: 403},
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":10}}`, key: "gateway-test-key", policy: accessPolicyModule{models: []string{"*"}, tpm: 1}, code: 429},
		{path: "/v1beta/models/m:streamGenerateContent", body: `{}`, code: 400},
		{path: "/v1beta/models/m:generateContent?key=not-accepted", body: `{}`, code: 400},
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`, code: 400},
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}],"serviceTier":"unknown"}`, code: 400},
		{path: "/v1beta/models/m:generateContent", body: `{"contents":[{"parts":[{"text":"hi"}]}],"unknown":true}`, code: 400},
		{path: "/v1beta/models/m:missing", body: `{}`, code: 404},
	} {
		upstream := &fallbackChatProvider{}
		handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{tc.policy}}), upstream))
		response := generateCall(handler, tc.path, tc.body, tc.key)
		if response.Code != tc.code || upstream.calls != 0 || !strings.Contains(response.Body.String(), `"status":`) {
			t.Fatalf("policy: %d %s calls=%d", response.Code, response.Body.String(), upstream.calls)
		}
	}
}
func TestGenerateContentSSEConvertsToolsAndErrors(t *testing.T) {
	for _, fail := range []bool{false, true} {
		upstream := &nativeMessagesStreamProvider{fail: fail}
		response := generateCall(Routes(NewHandler(modules.NewPipeline(nil), upstream)), "/v1beta/models/model:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"hi"}]}]}`, "")
		body := response.Body.String()
		if response.Code != 200 || response.Header().Get("Content-Type") != "text/event-stream" || strings.Contains(body, "[DONE]") || strings.Contains(body, "sensitive upstream error") {
			t.Fatalf("native stream: %d %s", response.Code, body)
		}
		if fail {
			if !strings.Contains(body, `"error":`) || strings.Contains(body, `"finishReason"`) {
				t.Fatal(body)
			}
			continue
		}
		if !strings.Contains(body, `"args":{"city":"Paris"}`) || !strings.Contains(body, `"finishReason":"STOP"`) || !strings.Contains(body, `"promptTokenCount":12`) {
			t.Fatal(body)
		}
	}
}
func TestGenerateContentGeminiRoundTripUsageAndBilling(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" || r.Header.Get("x-goog-api-key") != "provider-key" {
			t.Error("native path/key lost")
		}
		var native map[string]any
		if err := json.NewDecoder(r.Body).Decode(&native); err != nil {
			t.Error(err)
		}
		toolsJSON, _ := json.Marshal(native["tools"])
		if !strings.Contains(string(toolsJSON), `"googleSearch":{}`) {
			t.Errorf("Google Search tool lost: %#v", native["tools"])
		}
		if native["serviceTier"] != "priority" || native["store"] != true {
			t.Errorf("native service controls lost: %#v", native)
		}
		_, _ = w.Write([]byte("data: {\"responseId\":\"g-test\",\"modelVersion\":\"gemini-resolved\",\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\",\"groundingMetadata\":{\"webSearchQueries\":[\"query\"],\"groundingChunks\":[],\"groundingSupports\":[],\"searchEntryPoint\":{\"renderedContent\":\"widget\"}}}],\"usageMetadata\":{\"promptTokenCount\":10,\"toolUsePromptTokenCount\":4,\"candidatesTokenCount\":2,\"thoughtsTokenCount\":3,\"totalTokenCount\":19,\"serviceTier\":\"priority\"}}\n\n"))
	}))
	defer upstream.Close()
	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "gemini", BaseURL: upstream.URL, APIKey: "provider-key", Stream: true, Models: []string{"m"}, ModelAliases: map[string]string{"m": "gemini-test"}, Capabilities: []string{"chat", "stream", "web_search"}}}, Modules: modules.NewPipeline([]modules.Module{billing})})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}}}}), router))
	response := generateCall(handler, "/v1beta/models/m:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"hi"}]}],"serviceTier":"priority","store":true,"tools":[{"googleSearch":{}}],"generationConfig":{"maxOutputTokens":10}}`, "gateway-test-key")
	if !strings.Contains(response.Body.String(), `"modelVersion":"gemini-resolved"`) || response.Code != 200 || billing.calls != 1 || billing.usage.PromptTokens != 14 || billing.usage.ProviderToolInputTokens != 4 || billing.usage.TotalTokens != 19 || billing.usage.CompletionTokens != 5 || billing.usage.SearchRequests != 1 || !strings.Contains(response.Body.String(), `"promptTokenCount":10`) || !strings.Contains(response.Body.String(), `"toolUsePromptTokenCount":4`) || !strings.Contains(response.Body.String(), `"candidatesTokenCount":2`) || !strings.Contains(response.Body.String(), `"thoughtsTokenCount":3`) || !strings.Contains(response.Body.String(), `"serviceTier":"priority"`) || !strings.Contains(response.Body.String(), `"searchEntryPoint":{"renderedContent":"widget"}`) {
		t.Fatalf("native usage: %d %+v %s", response.Code, billing.usage, response.Body.String())
	}
}
func TestGenerateContentStreamingBoundsAndWriteFailure(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &generateWriter{destination: recorder, headers: make(http.Header)}
	if err := writer.chunk(`{"id":"m","choices":[{"index":0,"delta":{"content":"hi"}}]}`); err != nil {
		t.Fatal(err)
	}
	if err := writer.chunk("[DONE]"); err == nil {
		t.Fatal("truncated stream accepted")
	}
	writer.finish()
	if !strings.Contains(recorder.Body.String(), `"error"`) {
		t.Fatal("stream failure not emitted")
	}
	bounded := &generateWriter{destination: httptest.NewRecorder(), headers: make(http.Header)}
	if _, err := bounded.Write(make([]byte, (32<<20)+1)); err == nil {
		t.Fatal("oversized frame accepted")
	}
	failure := errors.New("disconnected")
	failed := &generateWriter{destination: messagesFailWriter{httptest.NewRecorder(), failure}, headers: make(http.Header)}
	if err := failed.chunk(`{"id":"m","choices":[{"index":0,"delta":{"content":"hi"}}]}`); !errors.Is(err, failure) {
		t.Fatal("write error lost")
	}
}
func TestGenerateContentMetricPathsAreBounded(t *testing.T) {
	for _, action := range []string{"generateContent", "streamGenerateContent", "countTokens"} {
		if got := metricPath("/v1beta/models/private-model:" + action); got != "/v1beta/models/{model}:"+action {
			t.Fatalf("metric path %s", got)
		}
	}
}

func TestGenerateContentEnforcesToolACL(t *testing.T) {
	upstream := &fallbackChatProvider{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{"safe"}}}}), upstream))
	response := generateCall(handler, "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"denied"}]}]}`, "gateway-test-key")
	if response.Code != 403 || upstream.calls != 0 {
		t.Fatalf("tool ACL bypassed: %d %s", response.Code, response.Body.String())
	}
}

func TestGenerateContentGeminiCodeExecutionStreamAndACL(t *testing.T) {
	denied := &fallbackChatProvider{}
	deniedHandler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{"safe"}}}}), denied))
	response := generateCall(deniedHandler, "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"text":"calculate"}]}],"tools":[{"codeExecution":{}}]}`, "gateway-test-key")
	if response.Code != http.StatusForbidden || denied.calls != 0 || !strings.Contains(response.Body.String(), "code_execution") {
		t.Fatalf("code execution ACL bypassed: %d %s calls=%d", response.Code, response.Body.String(), denied.calls)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(body["tools"])
		if !strings.Contains(string(encoded), `"codeExecution":{}`) {
			t.Fatalf("native tool lost: %s", encoded)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"responseId\":\"code\",\"modelVersion\":\"gemini-code\",\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"executableCode\":{\"id\":\"exec-1\",\"language\":\"PYTHON\",\"code\":\"print(4)\"}},{\"codeExecutionResult\":{\"id\":\"exec-1\",\"outcome\":\"OUTCOME_OK\",\"output\":\"4\\n\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"responseId\":\"code\",\"modelVersion\":\"gemini-code\",\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"The answer is 4.\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":4,\"candidatesTokenCount\":8,\"totalTokenCount\":12}}\n\n"))
	}))
	defer upstream.Close()
	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "gemini", BaseURL: upstream.URL, APIKey: "provider-key", Stream: true, Models: []string{"m"}, ModelAliases: map[string]string{"m": "gemini-code"}, Capabilities: []string{"chat", "stream", "gemini_code_execution"}}}, Modules: modules.NewPipeline([]modules.Module{billing})})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}, tools: []string{"code_execution"}}}}), router))
	response = generateCall(handler, "/v1beta/models/m:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"calculate"}]}],"tools":[{"codeExecution":{}}]}`, "gateway-test-key")
	for _, want := range []string{`"executableCode":{"id":"exec-1","language":"PYTHON","code":"print(4)"}`, `"codeExecutionResult":{"id":"exec-1","outcome":"OUTCOME_OK","output":"4\n"}`, `"text":"The answer is 4."`, `"totalTokenCount":12`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("missing %s: %d %s", want, response.Code, response.Body.String())
		}
	}
	if response.Code != http.StatusOK || billing.calls != 1 || billing.usage.TotalTokens != 12 {
		t.Fatalf("code execution settlement: status=%d billing=%d usage=%+v", response.Code, billing.calls, billing.usage)
	}
}

func TestGenerateContentGeminiURLContextStreamACLAndBilling(t *testing.T) {
	denied := &fallbackChatProvider{}
	deniedHandler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{"safe"}}}}), denied))
	response := generateCall(deniedHandler, "/v1beta/models/m:generateContent", `{"contents":[{"parts":[{"text":"summarize https://example.com"}]}],"tools":[{"urlContext":{}}]}`, "gateway-test-key")
	if response.Code != http.StatusForbidden || denied.calls != 0 || !strings.Contains(response.Body.String(), "url_context") {
		t.Fatalf("URL context ACL bypassed: %d %s calls=%d", response.Code, response.Body.String(), denied.calls)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(body["tools"])
		if !strings.Contains(string(encoded), `"urlContext":{}`) {
			t.Fatalf("native URL context tool lost: %s", encoded)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"responseId\":\"urls\",\"modelVersion\":\"gemini-urls\",\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"summary\"}]},\"finishReason\":\"STOP\",\"urlContextMetadata\":{\"urlMetadata\":[{\"retrievedUrl\":\"https://example.com/report\",\"urlRetrievalStatus\":\"URL_RETRIEVAL_STATUS_SUCCESS\"}]}}],\"usageMetadata\":{\"promptTokenCount\":4,\"toolUsePromptTokenCount\":9,\"candidatesTokenCount\":2,\"totalTokenCount\":15}}\n\n"))
	}))
	defer upstream.Close()
	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "gemini", BaseURL: upstream.URL, APIKey: "provider-key", Stream: true, Models: []string{"m"}, ModelAliases: map[string]string{"m": "gemini-urls"}, Capabilities: []string{"chat", "stream", "url_context"}}}, Modules: modules.NewPipeline([]modules.Module{billing})})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}, tools: []string{"url_context"}}}}), router))
	response = generateCall(handler, "/v1beta/models/m:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"summarize https://example.com/report"}]}],"tools":[{"urlContext":{}}]}`, "gateway-test-key")
	for _, want := range []string{`"text":"summary"`, `"urlContextMetadata":{"urlMetadata":[{"retrievedUrl":"https://example.com/report","urlRetrievalStatus":"URL_RETRIEVAL_STATUS_SUCCESS"}]}`, `"toolUsePromptTokenCount":9`, `"totalTokenCount":15`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("missing %s: %d %s", want, response.Code, response.Body.String())
		}
	}
	if response.Code != http.StatusOK || billing.calls != 1 || billing.usage.PromptTokens != 13 || billing.usage.ProviderToolInputTokens != 9 || billing.usage.TotalTokens != 15 {
		t.Fatalf("URL context settlement: status=%d billing=%d usage=%+v", response.Code, billing.calls, billing.usage)
	}
}

func TestGenerateContentGeminiGoogleMapsStreamACLAndBilling(t *testing.T) {
	denied := &fallbackChatProvider{}
	deniedHandler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"*"}, tools: []string{"safe"}}}}), denied))
	requestBody := `{"contents":[{"parts":[{"text":"restaurants near here"}]}],"tools":[{"googleMaps":{}}],"toolConfig":{"retrievalConfig":{"latLng":{"latitude":40.758896,"longitude":-73.98513}}}}`
	response := generateCall(deniedHandler, "/v1beta/models/m:generateContent", requestBody, "gateway-test-key")
	if response.Code != http.StatusForbidden || denied.calls != 0 || !strings.Contains(response.Body.String(), "google_maps") {
		t.Fatalf("Google Maps ACL bypassed: %d %s calls=%d", response.Code, response.Body.String(), denied.calls)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(body)
		for _, want := range []string{`"googleMaps":{}`, `"retrievalConfig":{"latLng":{"latitude":40.758896,"longitude":-73.98513}}`} {
			if !strings.Contains(string(encoded), want) {
				t.Fatalf("native Maps request lost %s: %s", want, encoded)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"responseId\":\"maps\",\"modelVersion\":\"gemini-maps\",\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"Try Cafe One.\"}]},\"finishReason\":\"STOP\",\"groundingMetadata\":{\"googleMapsWidgetContextToken\":\"widget\",\"groundingChunks\":[{\"maps\":{\"uri\":\"https://maps.google.com/?cid=1\",\"title\":\"Cafe One\",\"text\":\"Coffee shop\",\"placeId\":\"places/one\"}}],\"groundingSupports\":[{\"segment\":{\"startIndex\":4,\"endIndex\":12,\"text\":\"Cafe One\"},\"groundingChunkIndices\":[0]}]}}],\"usageMetadata\":{\"promptTokenCount\":4,\"toolUsePromptTokenCount\":2,\"candidatesTokenCount\":3,\"totalTokenCount\":9}}\n\n"))
	}))
	defer upstream.Close()
	billing := &messagesUsageRecorder{}
	router := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "gemini", BaseURL: upstream.URL, APIKey: "provider-key", Stream: true, Models: []string{"m"}, ModelAliases: map[string]string{"m": "gemini-maps"}, Capabilities: []string{"chat", "stream", "google_maps"}}}, Modules: modules.NewPipeline([]modules.Module{billing})})
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}, tools: []string{"google_maps"}}}}), router))
	response = generateCall(handler, "/v1beta/models/m:streamGenerateContent?alt=sse", requestBody, "gateway-test-key")
	for _, want := range []string{`"text":"Try Cafe One."`, `"googleMapsWidgetContextToken":"widget"`, `"maps":{"uri":"https://maps.google.com/?cid=1"`, `"totalTokenCount":9`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("missing %s: %d %s", want, response.Code, response.Body.String())
		}
	}
	if response.Code != http.StatusOK || billing.calls != 1 || billing.usage.PromptTokens != 6 || billing.usage.ProviderToolInputTokens != 2 || billing.usage.TotalTokens != 9 || billing.usage.SearchRequests != 1 {
		t.Fatalf("Maps settlement: status=%d billing=%d usage=%+v", response.Code, billing.calls, billing.usage)
	}
}

func TestGenerateCountTokensEnforcesNativeManagedToolACL(t *testing.T) {
	for _, test := range []struct {
		name, tool, grant string
	}{
		{name: "code execution", tool: `"codeExecution":{}`, grant: "code_execution"},
		{name: "URL context", tool: `"urlContext":{}`, grant: "url_context"},
		{name: "Google Maps", tool: `"googleMaps":{}`, grant: "google_maps"},
	} {
		t.Run(test.name, func(t *testing.T) {
			counter := &countProviderSpy{}
			body := `{"generateContentRequest":{"contents":[{"parts":[{"text":"count"}]}],"tools":[{` + test.tool + `}]}}`
			denied := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}, tools: []string{"safe"}}}}), counter))
			response := generateCall(denied, "/v1beta/models/m:countTokens", body, "gateway-test-key")
			if response.Code != http.StatusForbidden || counter.calls != 0 {
				t.Fatalf("managed tool count ACL bypassed: status=%d calls=%d body=%s", response.Code, counter.calls, response.Body.String())
			}
			allowed := Routes(NewHandler(modules.NewPipeline([]modules.Module{messagesAuth{accessPolicyModule{models: []string{"m"}, tools: []string{test.grant}}}}), counter))
			response = generateCall(allowed, "/v1beta/models/m:countTokens", body, "gateway-test-key")
			if response.Code != http.StatusOK || counter.calls != 1 || counter.request.Request.NativeInputTokens == 0 {
				t.Fatalf("managed tool count rejected: status=%d calls=%d request=%+v body=%s", response.Code, counter.calls, counter.request.Request, response.Body.String())
			}
		})
	}
}
func TestGenerateContentFallbackSSE(t *testing.T) {
	upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "fallback", Model: "m", Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "hello"}, FinishReason: "stop"}}, Usage: openai.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}}}
	response := generateCall(Routes(NewHandler(modules.NewPipeline(nil), upstream)), "/v1beta/models/m:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"hi"}]}]}`, "")
	for _, want := range []string{`"text":"hello"`, `"finishReason":"STOP"`, `"totalTokenCount":3`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("missing %s: %s", want, response.Body.String())
		}
	}
}
func TestGenerateContentRejectsMalformedToolStreams(t *testing.T) {
	for _, payload := range []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":128}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":-1}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"custom"}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"new"}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"changed"}}}]}}]}`,
	} {
		writer := &generateWriter{destination: httptest.NewRecorder(), headers: make(http.Header)}
		first := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"original","function":{"name":"f"},"extra_content":{"google":{"thought_signature":"original"}}}]}}]}`
		if err := writer.chunk(first); err != nil {
			t.Fatal(err)
		}
		if err := writer.chunk(payload); err == nil {
			t.Fatalf("accepted malformed tool: %s", payload)
		}
	}
	for _, args := range []string{"null", "[]", "{"} {
		_, err := generateParts(openai.Message{ToolCalls: []openai.ToolCall{{Type: "function", Function: openai.FunctionCall{Name: "f", Arguments: args}}}})
		if err == nil {
			t.Fatalf("accepted non-object args: %s", args)
		}
	}
	writer := &generateWriter{destination: httptest.NewRecorder(), headers: make(http.Header), toolBytes: 32 << 20}
	if err := writer.chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"x"}}]}}]}`); err == nil {
		t.Fatal("tool budget exceeded")
	}
}
