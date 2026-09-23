package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func geminiTestChat() openai.ChatCompletionRequest {
	limit := 123
	topK, frequencyPenalty, presencePenalty := 40, 0.2, -0.1
	return openai.ChatCompletionRequest{Model: "gemini-test", MaxCompletionTokens: &limit, Messages: []openai.Message{{Role: "system", Content: "Be concise"}, {Role: "user", Content: "Weather?"}}, Tools: []openai.Tool{{Type: "function", Function: openai.FunctionDefinition{Name: "weather", Parameters: map[string]any{"type": "object"}}}}, ToolChoice: "required", ResponseFormat: &openai.ResponseFormat{Type: "json_schema", JSONSchema: &openai.JSONSchemaFormat{Name: "answer", Schema: map[string]any{"type": "object"}}}, ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &topK, FrequencyPenalty: &frequencyPenalty, PresencePenalty: &presencePenalty, ReasoningEffort: "medium"}}
}

func TestGeminiNativeChatAndToolSignatures(t *testing.T) {
	var request geminiRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-test:generateContent" || r.URL.RawQuery != "" || r.Header.Get("x-goog-api-key") != "fake-key" || r.Header.Get("Authorization") != "" {
			t.Errorf("invalid native transport: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		_, _ = fmt.Fprint(w, `{"responseId":"native-id","candidates":[{"index":0,"content":{"parts":[{"functionCall":{"id":"native-call","name":"weather","args":{"city":"Moscow"}},"thoughtSignature":"opaque-signature"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"toolUsePromptTokenCount":6,"cachedContentTokenCount":4,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":21}}`)
	}))
	defer server.Close()
	client := NewGemini(server.URL, "fake-key", true)
	response, err := client.ChatCompletions(context.Background(), geminiTestChat())
	if err != nil {
		t.Fatal(err)
	}
	if request.System == nil || request.System.Parts[0].Text != "Be concise" || request.Generation.MaxOutputTokens == nil || *request.Generation.MaxOutputTokens != 123 || request.Generation.TopK == nil || *request.Generation.TopK != 40 || request.Generation.FrequencyPenalty == nil || *request.Generation.FrequencyPenalty != 0.2 || request.Generation.PresencePenalty == nil || *request.Generation.PresencePenalty != -0.1 || request.Generation.ThinkingConfig == nil || request.Generation.ThinkingConfig.ThinkingLevel != "medium" || request.Generation.ResponseMIMEType != "application/json" || len(request.Tools) != 1 {
		t.Fatalf("native mapping incomplete: %+v", request)
	}
	if response.Usage.PromptTokens != 16 || response.Usage.ProviderToolInputTokens != 6 || response.Usage.TotalTokens != 21 || response.Usage.CompletionTokens != 5 || response.Usage.PromptTokensDetails.CachedTokens != 4 {
		t.Fatalf("usage mapped incorrectly: %+v", response.Usage)
	}
	if response.Choices[0].FinishReason != "tool_calls" {
		t.Fatal("tool finish reason lost")
	}
	call := response.Choices[0].Message.ToolCalls[0]
	if call.ID != "native-call" || call.ExtraContent.Google.ThoughtSignature != "opaque-signature" {
		t.Fatal("native tool identity/signature lost")
	}
	next := geminiTestChat()
	next.Messages = append(next.Messages, response.Choices[0].Message, openai.Message{Role: "tool", ToolCallID: call.ID, Content: map[string]any{"temperature": 20}})
	encoded, err := geminiChatRequest(next)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Contents[1].Parts[0].ThoughtSignature != "opaque-signature" || encoded.Contents[2].Parts[0].FunctionResponse.Name != "weather" {
		t.Fatalf("tool continuation was not preserved: %+v", encoded.Contents)
	}
}

func TestGeminiForwardsValidatedSafetySettings(t *testing.T) {
	request := geminiTestChat()
	request.GeminiSafetySettings = []openai.GeminiSafetySetting{{Category: "HARM_CATEGORY_HATE_SPEECH", Threshold: "BLOCK_MEDIUM_AND_ABOVE"}}
	native, err := geminiChatRequest(request)
	if err != nil || len(native.Safety) != 1 || native.Safety[0] != request.GeminiSafetySettings[0] {
		t.Fatalf("native=%+v err=%v", native, err)
	}
}

func TestGeminiForwardsValidatedMediaResolution(t *testing.T) {
	request := openai.ChatCompletionRequest{
		Model: "gemini-test",
		Messages: []openai.Message{{Role: "user", Content: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
		}}},
		GeminiMediaResolution: "MEDIA_RESOLUTION_HIGH",
	}
	native, err := geminiChatRequest(request)
	if err != nil || native.Generation.MediaResolution != "MEDIA_RESOLUTION_HIGH" {
		t.Fatalf("native=%+v err=%v", native, err)
	}
	request.Messages = []openai.Message{{Role: "user", Content: "text only"}}
	if _, err := geminiChatRequest(request); err == nil {
		t.Fatal("media resolution without media input accepted")
	}
	request.Messages = []openai.Message{{Role: "user", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}}}}}
	request.GeminiMediaResolution = "MEDIA_RESOLUTION_ULTRA_HIGH"
	if _, err := geminiChatRequest(request); err == nil {
		t.Fatal("global ultra-high media resolution accepted")
	}
}

func TestGeminiForwardsPerPartMediaResolution(t *testing.T) {
	request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: []any{
		map[string]any{
			"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="},
			"gemini_media_resolution": "MEDIA_RESOLUTION_ULTRA_HIGH",
		},
	}}}}
	native, err := geminiChatRequest(request)
	if err != nil || native.Contents[0].Parts[0].MediaResolution == nil || native.Contents[0].Parts[0].MediaResolution.Level != "MEDIA_RESOLUTION_ULTRA_HIGH" {
		t.Fatalf("native=%+v err=%v", native, err)
	}
	request.Messages[0].Content = []any{map[string]any{"type": "text", "text": "hello", "gemini_media_resolution": "MEDIA_RESOLUTION_HIGH"}}
	if _, err := geminiChatRequest(request); err == nil {
		t.Fatal("text part media resolution accepted")
	}
}

func TestGeminiForwardsPerVideoMediaProcessing(t *testing.T) {
	request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: []any{
		map[string]any{
			"type": "input_video", "input_video": map[string]any{"data": "AAAADGZ0eXBtcDQy", "format": "mp4"},
			"gemini_media_processing": "AGENTIC",
		},
	}}}}
	native, err := geminiChatRequest(request)
	if err != nil || native.Contents[0].Parts[0].MediaProcessing != "AGENTIC" {
		t.Fatalf("native=%+v err=%v", native, err)
	}
	request.Messages[0].Content = []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}, "gemini_media_processing": "STATIC"}}
	if _, err := geminiChatRequest(request); err == nil {
		t.Fatal("image media processing accepted")
	}
}

func TestGeminiGoogleSearchGroundingAndUsage(t *testing.T) {
	var upstream geminiRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprint(w, `{"responseId":"grounded","candidates":[{"index":0,"content":{"parts":[{"text":"Paris is sunny."}]},"finishReason":"STOP","groundingMetadata":{"webSearchQueries":["Paris weather"],"groundingChunks":[{"web":{"uri":"https://weather.example/paris","title":"Weather"}}],"groundingSupports":[{"segment":{"startIndex":9,"endIndex":14,"text":"sunny"},"groundingChunkIndices":[0]}],"searchEntryPoint":{"renderedContent":"<div>Search</div>"}}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":3,"totalTokenCount":7}}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "weather"}}, ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{}}}
	response, err := NewGemini(server.URL, "key", false).ChatCompletions(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	annotation := response.Choices[0].Message.Annotations
	if len(upstream.Tools) != 1 || upstream.Tools[0].GoogleSearch == nil || response.Usage.SearchRequests != 1 || len(annotation) != 1 || annotation[0].URLCitation.URL != "https://weather.example/paris" || !strings.Contains(string(response.Choices[0].GeminiGroundingMetadata), "searchEntryPoint") {
		t.Fatalf("upstream=%+v response=%+v", upstream, response)
	}
}

func TestGeminiForwardsGoogleSearchTimeRange(t *testing.T) {
	rangeFilter := &openai.GeminiSearchTimeRange{StartTime: "2026-01-01T00:00:00Z", EndTime: "2026-02-01T00:00:00Z"}
	native, err := geminiChatRequest(openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "news"}}, ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{GeminiTimeRange: rangeFilter}}})
	if err != nil || len(native.Tools) != 1 || native.Tools[0].GoogleSearch == nil || native.Tools[0].GoogleSearch.TimeRange == nil || native.Tools[0].GoogleSearch.TimeRange.StartTime != rangeFilter.StartTime {
		t.Fatalf("native=%+v err=%v", native, err)
	}
	rangeFilter.StartTime = "invalid"
	if _, err := geminiChatRequest(openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "news"}}, ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{GeminiTimeRange: rangeFilter}}}); err == nil {
		t.Fatal("invalid search time range accepted")
	}
}

func TestGeminiFileSearchRoundTrip(t *testing.T) {
	var upstream geminiRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprint(w, `{"responseId":"retrieved","candidates":[{"content":{"parts":[{"text":"Use policy A."}]},"finishReason":"STOP","groundingMetadata":{"groundingChunks":[{"retrievedContext":{"uri":"https://docs.example/policy-a","title":"Policy A","text":"Policy body","fileSearchStore":"fileSearchStores/policies","pageNumber":2,"mediaId":"fileSearchStores/policies/media/policy-a","customMetadata":[{"key":"status","stringValue":"active"}]}}],"groundingSupports":[{"segment":{"startIndex":4,"endIndex":12,"text":"policy A"},"groundingChunkIndices":[0]}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":3,"totalTokenCount":7}}`)
	}))
	defer server.Close()
	topK := 8
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "find policy"}}, GeminiFileSearch: &openai.GeminiFileSearchConfig{StoreNames: []string{"fileSearchStores/policies"}, MetadataFilter: "status=active", TopK: &topK}}
	response, err := NewGemini(server.URL, "key", false).ChatCompletions(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	annotations := response.Choices[0].Message.Annotations
	if len(upstream.Tools) != 1 || upstream.Tools[0].FileSearch == nil || upstream.Tools[0].FileSearch.StoreNames[0] != "fileSearchStores/policies" || response.Usage.SearchRequests != 0 || len(annotations) != 1 || annotations[0].URLCitation.URL != "https://docs.example/policy-a" || !strings.Contains(string(response.Choices[0].GeminiGroundingMetadata), "retrievedContext") {
		t.Fatalf("upstream=%+v response=%+v", upstream, response)
	}
	request.GeminiCodeExecution = true
	if _, err := geminiChatRequest(request); err == nil {
		t.Fatal("file search combined with another tool")
	}
}

func TestGeminiForwardsComputerUse(t *testing.T) {
	config := &openai.GeminiComputerUseConfig{Environment: "ENVIRONMENT_BROWSER", ExcludedPredefinedFunctions: []string{"drag_and_drop"}, EnablePromptInjectionDetection: true, DisabledSafetyPolicies: []string{"FINANCIAL_TRANSACTIONS"}}
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "browse"}}, GeminiComputerUse: config}
	native, err := geminiChatRequest(request)
	if err != nil || len(native.Tools) != 1 || native.Tools[0].ComputerUse == nil || native.Tools[0].ComputerUse.Environment != "ENVIRONMENT_BROWSER" || !native.Tools[0].ComputerUse.EnablePromptInjectionDetection {
		t.Fatalf("native=%+v err=%v", native, err)
	}
	request.GeminiComputerUse.Environment = "browser"
	if _, err := geminiChatRequest(request); err == nil {
		t.Fatal("invalid environment accepted")
	}
}

func TestGeminiRejectsInvalidFileSearchGrounding(t *testing.T) {
	for _, raw := range []string{
		`{"groundingChunks":[{"retrievedContext":{"fileSearchStore":"wrong"}}]}`,
		`{"groundingChunks":[{"retrievedContext":{"fileSearchStore":"fileSearchStores/a","mediaId":"fileSearchStores/b/media/one"}}]}`,
		`{"groundingChunks":[{"retrievedContext":{"fileSearchStore":"fileSearchStores/a","pageNumber":0}}]}`,
		`{"groundingChunks":[{"retrievedContext":{"fileSearchStore":"fileSearchStores/a","customMetadata":[{"key":"x","stringValue":"a","numericValue":1}]}}]}`,
		`{"groundingChunks":[{"web":{"uri":"https://example.com","title":"Web"},"retrievedContext":{"fileSearchStore":"fileSearchStores/a"}}]}`,
	} {
		if _, _, err := geminiGrounding(json.RawMessage(raw), "text"); err == nil {
			t.Fatalf("invalid file search grounding accepted: %s", raw)
		}
	}
}

func TestGeminiGoogleMapsGroundingAndUsage(t *testing.T) {
	var upstream geminiRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstream); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprint(w, `{"responseId":"maps","candidates":[{"index":0,"content":{"parts":[{"text":"Try Cafe One."}]},"finishReason":"STOP","groundingMetadata":{"googleMapsWidgetContextToken":"widget-token","groundingChunks":[{"maps":{"uri":"https://maps.google.com/?cid=1","title":"Cafe One","text":"Coffee shop","placeId":"places/one","placeAnswerSources":{"reviewSnippets":[{"reviewId":"review-1","googleMapsUri":"https://maps.google.com/review/1","title":"Review"}]}}}],"groundingSupports":[{"segment":{"startIndex":4,"endIndex":12,"text":"Cafe One"},"groundingChunkIndices":[0]}]}}],"usageMetadata":{"promptTokenCount":4,"toolUsePromptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":9}}`)
	}))
	defer server.Close()
	location := &openai.GeminiLatLng{Latitude: 40.758896, Longitude: -73.98513}
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "restaurants"}}, GeminiGoogleMaps: true, GeminiRetrievalLocation: location}
	response, err := NewGemini(server.URL, "key", false).ChatCompletions(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := upstream.ToolConfig["retrievalConfig"].(map[string]any)
	encodedConfig, _ := json.Marshal(config)
	annotations := response.Choices[0].Message.Annotations
	if len(upstream.Tools) != 1 || upstream.Tools[0].GoogleMaps == nil || !strings.Contains(string(encodedConfig), `"latitude":40.758896`) || response.Usage.SearchRequests != 1 || response.Usage.ProviderToolInputTokens != 2 || len(annotations) != 1 || annotations[0].URLCitation.URL != "https://maps.google.com/?cid=1" || !strings.Contains(string(response.Choices[0].GeminiGroundingMetadata), "googleMapsWidgetContextToken") {
		t.Fatalf("upstream=%+v response=%+v", upstream, response)
	}
}

func TestGeminiRejectsInvalidGoogleMapsGrounding(t *testing.T) {
	for _, raw := range []string{
		`{"groundingChunks":[{"maps":{"uri":"javascript:alert(1)","title":"Place","placeId":"places/one"}}]}`,
		`{"groundingChunks":[{"maps":{"uri":"https://maps.google.com/place/1","title":"","placeId":"places/one"}}]}`,
		`{"groundingChunks":[{"maps":{"uri":"https://maps.google.com/place/1","title":"Place","placeId":"wrong"}}]}`,
		`{"groundingChunks":[{"web":{"uri":"https://example.com","title":"Web"},"maps":{"uri":"https://maps.google.com/place/1","title":"Place","placeId":"places/one"}}]}`,
		`{"googleMapsWidgetContextToken":"` + strings.Repeat("x", 65537) + `"}`,
	} {
		if _, _, err := geminiGrounding(json.RawMessage(raw), "Place"); err == nil {
			t.Fatalf("invalid Maps grounding accepted: %s", raw[:min(len(raw), 200)])
		}
	}
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "places"}}, GeminiRetrievalLocation: &openai.GeminiLatLng{}}
	if _, err := geminiChatRequest(request); err == nil {
		t.Fatal("retrieval location without Google Maps accepted")
	}
	request.GeminiGoogleMaps = true
	request.GeminiRetrievalLocation.Latitude = 91
	if _, err := geminiChatRequest(request); err == nil {
		t.Fatal("out-of-range Maps location accepted")
	}
}

func TestGeminiCodeExecutionRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Tools) != 1 || body.Tools[0].CodeExecution == nil {
			t.Fatalf("code execution tool lost: %+v", body.Tools)
		}
		_, _ = fmt.Fprint(w, `{"responseId":"code","candidates":[{"index":0,"content":{"parts":[{"executableCode":{"id":"exec-1","language":"PYTHON","code":"print(4)"}},{"codeExecutionResult":{"id":"exec-1","outcome":"OUTCOME_OK","output":"4\n"}},{"text":"The answer is 4."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":8,"totalTokenCount":12}}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "calculate"}}, GeminiCodeExecution: true}
	response, err := NewGemini(server.URL, "key", false).ChatCompletions(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	parts := response.Choices[0].Message.GeminiCodeExecutionParts
	if len(parts) != 2 || parts[0].Code == nil || parts[1].Result == nil || parts[1].Result.Output != "4\n" || openai.ContentText(response.Choices[0].Message.Content) != "The answer is 4." {
		t.Fatalf("execution response lost: %+v", response)
	}
}

func TestGeminiURLContextRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Tools) != 1 || body.Tools[0].URLContext == nil {
			t.Fatalf("URL context tool lost: %+v", body.Tools)
		}
		_, _ = fmt.Fprint(w, `{"responseId":"urls","candidates":[{"index":0,"content":{"parts":[{"text":"summary"}]},"finishReason":"STOP","urlContextMetadata":{"urlMetadata":[{"retrievedUrl":"https://example.com/report","urlRetrievalStatus":"URL_RETRIEVAL_STATUS_SUCCESS"}]}}],"usageMetadata":{"promptTokenCount":4,"toolUsePromptTokenCount":9,"candidatesTokenCount":2,"totalTokenCount":15}}`)
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "summarize https://example.com/report"}}, GeminiURLContext: true}
	response, err := NewGemini(server.URL, "key", false).ChatCompletions(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage.PromptTokens != 13 || response.Usage.ProviderToolInputTokens != 9 || !strings.Contains(string(response.Choices[0].GeminiURLContextMetadata), `"retrievedUrl":"https://example.com/report"`) {
		t.Fatalf("URL context response lost: %+v", response)
	}
}

func TestGeminiRejectsInvalidURLContextMetadata(t *testing.T) {
	entries := strings.Repeat(`{"retrievedUrl":"https://example.com","urlRetrievalStatus":"URL_RETRIEVAL_STATUS_SUCCESS"},`, openai.MaxGeminiURLContextEntries+1)
	entries = strings.TrimSuffix(entries, ",")
	for _, raw := range []string{
		`{"urlMetadata":[{"retrievedUrl":"javascript:alert(1)","urlRetrievalStatus":"URL_RETRIEVAL_STATUS_SUCCESS"}]}`,
		`{"urlMetadata":[{"retrievedUrl":"https://example.com","urlRetrievalStatus":"FUTURE_STATUS"}]}`,
		`{"urlMetadata":[{"retrievedUrl":"https://example.com","urlRetrievalStatus":"URL_RETRIEVAL_STATUS_SUCCESS","extra":true}]}`,
		`{"urlMetadata":[` + entries + `]}`,
		`{"urlMetadata":[]} {}`,
	} {
		body := geminiResponse{Candidates: []geminiResponseCandidate{{Index: 0, Content: geminiContent{Parts: []geminiPart{{Text: "answer"}}}, FinishReason: "STOP", URLContext: json.RawMessage(raw)}}}
		if _, err := geminiToChat(body, "model"); err == nil {
			t.Fatalf("invalid URL context metadata accepted: %s", raw)
		}
	}
}

func TestGeminiRejectsInvalidGroundingAndUnrepresentableSearchOptions(t *testing.T) {
	request := openai.ChatCompletionRequest{ChatGenerationOptions: openai.ChatGenerationOptions{WebSearchOptions: &openai.ChatWebSearchOptions{SearchContextSize: "high"}}}
	if err := (Gemini{}).ValidateChatParameters(request); err == nil {
		t.Fatal("unrepresentable search option accepted")
	}
	for _, raw := range []string{
		`{"webSearchQueries":["q"],"groundingChunks":[{"web":{"uri":"javascript:alert(1)","title":"bad"}}],"groundingSupports":[{"segment":{"startIndex":0,"endIndex":2,"text":"ok"},"groundingChunkIndices":[0]}]}`,
		`{"webSearchQueries":["q","q","q","q","q","q"]}`,
		`{"groundingChunks":[],"groundingSupports":[{"segment":{"startIndex":0,"endIndex":3,"text":"wrong"},"groundingChunkIndices":[0]}]}`,
	} {
		if _, _, err := geminiGrounding(json.RawMessage(raw), "ok"); err == nil {
			t.Fatalf("invalid grounding accepted: %s", raw)
		}
	}
}

func TestGeminiPreservesThoughtPartsAndHistory(t *testing.T) {
	index := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		thought := body.Contents[1].Parts[0]
		if !thought.Thought || thought.Text != "prior plan" || thought.ThoughtSignature != "c2lnbmVk" {
			t.Fatalf("thought history=%+v", thought)
		}
		_, _ = w.Write([]byte(`{"candidates":[{"index":0,"content":{"parts":[{"text":"new plan","thought":true,"thoughtSignature":"bmV3LXNpZw=="},{"text":"answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"thoughtsTokenCount":1,"totalTokenCount":4}}`))
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "prior answer", Reasoning: []openai.ReasoningBlock{{Index: &index, Type: "thinking", Thinking: "prior plan", Signature: "c2lnbmVk"}}},
	}}
	response, err := NewGemini(server.URL, "", false).ChatCompletions(t.Context(), request)
	if err != nil || len(response.Choices[0].Message.Reasoning) != 1 || response.Choices[0].Message.Reasoning[0].Thinking != "new plan" || response.Choices[0].Message.Reasoning[0].Signature != "bmV3LXNpZw==" || openai.ContentText(response.Choices[0].Message.Content) != "answer" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestGeminiPreservesTextPartSignatures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Contents[1].Parts[0].ThoughtSignature != "cHJpb3I=" || body.Contents[1].Parts[0].Thought {
			t.Fatalf("history=%+v", body.Contents[1].Parts)
		}
		_, _ = w.Write([]byte(`{"candidates":[{"index":0,"content":{"parts":[{"text":"answer","thoughtSignature":"bmV3"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer server.Close()
	native, err := openai.AddGeminiPartSignature(nil, 0, "cHJpb3I=")
	if err != nil {
		t.Fatal(err)
	}
	request := openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "question"}, {Role: "assistant", Content: "prior", NativeContent: native}}}
	response, err := NewGemini(server.URL, "", false).ChatCompletions(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	signatures, err := openai.GeminiPartSignatures(response.Choices[0].Message.NativeContent)
	if err != nil || len(signatures) != 1 || signatures[0].Signature != "bmV3" {
		t.Fatalf("signatures=%+v err=%v", signatures, err)
	}
}

func TestGeminiRejectsUnknownNativeContent(t *testing.T) {
	request := geminiTestChat()
	request.Messages = append(request.Messages, openai.Message{
		Role:          "assistant",
		Content:       "answer",
		NativeContent: []json.RawMessage{json.RawMessage(`{"type":"unknown"}`)},
	})
	if _, err := geminiChatRequest(request); err == nil {
		t.Fatal("unknown native content accepted")
	}
}

func TestGeminiStreamPreservesTextPartSignatureInFinalResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"answer\",\"thoughtSignature\":\"c2lnbmVk\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n"))
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Stream: true, Messages: []openai.Message{{Role: "user", Content: "question"}}}
	response, err := NewGemini(server.URL, "", true).StreamChatCompletions(t.Context(), request, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	signatures, err := openai.GeminiPartSignatures(response.Choices[0].Message.NativeContent)
	if err != nil || len(signatures) != 1 || signatures[0].Signature != "c2lnbmVk" {
		t.Fatalf("signatures=%+v err=%v", signatures, err)
	}
}

func TestGeminiRejectsThoughtsOutsideAssistantHistory(t *testing.T) {
	request := geminiTestChat()
	request.Messages[0].Reasoning = []openai.ReasoningBlock{{Type: "thinking", Thinking: "private"}}
	if _, err := geminiChatRequest(request); err == nil {
		t.Fatal("user reasoning was silently discarded")
	}
}

func TestGeminiStreamReassemblesThoughtSignature(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"plan \",\"thought\":true}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"more\",\"thought\":true}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"\",\"thought\":true,\"thoughtSignature\":\"c2lnbmVk\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"thoughtsTokenCount\":2,\"totalTokenCount\":4}}\n\n"))
	}))
	defer server.Close()
	request := openai.ChatCompletionRequest{Model: "model", Stream: true, Messages: []openai.Message{{Role: "user", Content: "question"}}}
	var chunks []string
	response, err := NewGemini(server.URL, "", true).StreamChatCompletions(t.Context(), request, func(value string) error { chunks = append(chunks, value); return nil })
	if err != nil || len(response.Choices[0].Message.Reasoning) != 1 || response.Choices[0].Message.Reasoning[0].Thinking != "plan more" || response.Choices[0].Message.Reasoning[0].Signature != "c2lnbmVk" || !strings.Contains(strings.Join(chunks, "\n"), `"reasoning"`) {
		t.Fatalf("response=%+v chunks=%v err=%v", response, chunks, err)
	}
}

func TestGeminiNativeChatUsesGCPWorkloadToken(t *testing.T) {
	var tokenCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metadata/token":
			tokenCalls++
			if r.Header.Get("Metadata-Flavor") != "Google" {
				t.Errorf("missing metadata header: %v", r.Header)
			}
			w.Header().Set("Metadata-Flavor", "Google")
			_, _ = fmt.Fprint(w, `{"access_token":"workload-token","expires_in":3600,"token_type":"Bearer"}`)
		case "/v1beta/models/gemini-test:generateContent":
			if r.Header.Get("Authorization") != "Bearer workload-token" || r.Header.Get("x-goog-api-key") != "" {
				t.Errorf("invalid workload auth: %v", r.Header)
			}
			_, _ = fmt.Fprint(w, `{"responseId":"native-id","candidates":[{"index":0,"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewGeminiWithAuth(server.URL, "", false, "gcp_adc")
	client.tokenSource.metadataURL = server.URL + "/metadata/token"
	if _, err := client.ChatCompletions(t.Context(), geminiTestChat()); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 1 {
		t.Fatalf("metadata token calls=%d", tokenCalls)
	}
}

func TestGeminiRejectsUnknownAuthenticationType(t *testing.T) {
	client := NewGeminiWithAuth("https://example.invalid", "secret", false, "unknown")
	if _, err := client.ChatCompletions(t.Context(), geminiTestChat()); err == nil || !strings.Contains(err.Error(), "unsupported Gemini authentication type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGeminiNativeLogprobsRoundTrip(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var received geminiRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
					return
				}
				payload := `{"responseId":"logprobs-id","candidates":[{"index":0,"content":{"parts":[{"text":"é"}]},"finishReason":"STOP","logprobsResult":{"topCandidates":[{"candidates":[{"token":"é","tokenId":1,"logProbability":-0.1},{"token":"e","tokenId":2,"logProbability":-1.2}]}],"chosenCandidates":[{"token":"é","tokenId":1,"logProbability":-0.1}],"logProbabilitySum":-0.1}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
					return
				}
				_, _ = fmt.Fprint(w, payload)
			}))
			defer server.Close()
			enabled, count := true, 2
			request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &enabled, TopLogprobs: &count}}
			client := NewGemini(server.URL, "", streaming)
			var response openai.ChatCompletionResponse
			var payloads []string
			var err error
			if streaming {
				response, err = client.StreamChatCompletions(t.Context(), request, func(payload string) error { payloads = append(payloads, payload); return nil })
			} else {
				response, err = client.ChatCompletions(t.Context(), request)
			}
			if err != nil || received.Generation.ResponseLogprobs == nil || !*received.Generation.ResponseLogprobs || received.Generation.Logprobs == nil || *received.Generation.Logprobs != 2 {
				t.Fatalf("request=%+v response=%+v err=%v", received.Generation, response, err)
			}
			logprobs := response.Choices[0].Logprobs
			if logprobs == nil || len(logprobs.Content) != 1 || logprobs.Content[0].Token != "é" || logprobs.Content[0].Logprob != -0.1 || len(logprobs.Content[0].TopLogprobs) != 2 || fmt.Sprint(logprobs.Content[0].Bytes) != "[195 169]" {
				t.Fatalf("logprobs were not normalized: %+v", logprobs)
			}
			if streaming && (len(payloads) != 1 || !strings.Contains(payloads[0], `"logprobs":{"content"`)) {
				t.Fatalf("stream logprobs lost: %v", payloads)
			}
		})
	}
}

func TestGeminiNativeMultipleCandidates(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var received geminiRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
					return
				}
				payload := `{"responseId":"multi-id","candidates":[{"index":0,"content":{"parts":[{"text":"one"}]},"finishReason":"STOP"},{"index":1,"content":{"parts":[{"text":"two"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
					return
				}
				_, _ = fmt.Fprint(w, payload)
			}))
			defer server.Close()
			count := 2
			request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{N: &count}}
			client := NewGemini(server.URL, "", streaming)
			var response openai.ChatCompletionResponse
			var err error
			if streaming {
				response, err = client.StreamChatCompletions(t.Context(), request, func(string) error { return nil })
			} else {
				response, err = client.ChatCompletions(t.Context(), request)
			}
			if err != nil || received.Generation.CandidateCount == nil || *received.Generation.CandidateCount != 2 || len(response.Choices) != 2 || response.Choices[1].Message.Content != "two" {
				t.Fatalf("request=%+v response=%+v err=%v", received.Generation, response, err)
			}
		})
	}
}

func TestGeminiNativeServiceTierRoundTrip(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var received geminiRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
					return
				}
				payload := `{"responseId":"tier-id","candidates":[{"index":0,"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2,"serviceTier":"priority"}}`
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
					return
				}
				_, _ = fmt.Fprint(w, payload)
			}))
			defer server.Close()
			request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "priority"}}
			client := NewGemini(server.URL, "", streaming)
			var response openai.ChatCompletionResponse
			var payloads []string
			var err error
			if streaming {
				response, err = client.StreamChatCompletions(t.Context(), request, func(payload string) error { payloads = append(payloads, payload); return nil })
			} else {
				response, err = client.ChatCompletions(t.Context(), request)
			}
			if err != nil || received.ServiceTier != "priority" || response.ServiceTier != "priority" {
				t.Fatalf("request=%+v response=%+v err=%v", received, response, err)
			}
			if streaming && (len(payloads) != 1 || !strings.Contains(payloads[0], `"service_tier":"priority"`)) {
				t.Fatalf("stream service tier lost: %v", payloads)
			}
		})
	}
}

func TestGeminiServiceTierMapping(t *testing.T) {
	for input, want := range map[string]string{"auto": "unspecified", "default": "standard", "standard_only": "standard", "flex": "flex", "priority": "priority"} {
		t.Run(input, func(t *testing.T) {
			request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: input}}
			native, err := geminiChatRequest(request)
			if err != nil || native.ServiceTier != want {
				t.Fatalf("service tier=%q err=%v", native.ServiceTier, err)
			}
		})
	}
}

func TestGeminiForwardsProviderStorageControl(t *testing.T) {
	for _, store := range []bool{false, true} {
		t.Run(fmt.Sprint(store), func(t *testing.T) {
			var received geminiRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
					return
				}
				_, _ = fmt.Fprint(w, `{"candidates":[{"index":0,"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
			}))
			defer server.Close()
			request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Store: &store}}
			if _, err := NewGemini(server.URL, "", false).ChatCompletions(t.Context(), request); err != nil || received.Store == nil || *received.Store != store {
				t.Fatalf("store=%t was not forwarded: request=%+v err=%v", store, received, err)
			}
		})
	}
}

func TestGeminiMapsTextResponseModality(t *testing.T) {
	request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Modalities: []string{"text"}}}
	native, err := geminiChatRequest(request)
	if err != nil || len(native.Generation.ResponseModalities) != 1 || native.Generation.ResponseModalities[0] != "TEXT" {
		t.Fatalf("response modalities=%v err=%v", native.Generation.ResponseModalities, err)
	}
}

func TestGeminiRejectsAudioResponseModality(t *testing.T) {
	request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{Modalities: []string{"text", "audio"}, Audio: &openai.ChatAudioOptions{Voice: openai.ChatAudioVoice{Name: "alloy"}, Format: "wav"}}}
	var failure *Error
	if _, err := geminiChatRequest(request); !errors.As(err, &failure) || failure.Param != "modalities" || failure.UpstreamCode != "unsupported_parameter" {
		t.Fatalf("audio modality was not rejected explicitly: %v", err)
	}
}

func TestGeminiReasoningEffortMapping(t *testing.T) {
	for _, level := range []string{"minimal", "low", "medium", "high"} {
		t.Run(level, func(t *testing.T) {
			request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: level}}
			native, err := geminiChatRequest(request)
			if err != nil || native.Generation.ThinkingConfig == nil || native.Generation.ThinkingConfig.ThinkingLevel != level {
				t.Fatalf("thinking config=%+v err=%v", native.Generation.ThinkingConfig, err)
			}
		})
	}
}

func TestGeminiRequiresEveryRequestedCandidate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"candidates":[{"index":0,"content":{"parts":[{"text":"one"}]},"finishReason":"STOP"}]}`)
	}))
	defer server.Close()
	count := 2
	request := openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}, ChatGenerationOptions: openai.ChatGenerationOptions{N: &count}}
	if _, err := NewGemini(server.URL, "", false).ChatCompletions(t.Context(), request); err == nil || !strings.Contains(err.Error(), "choice count") {
		t.Fatalf("incomplete candidate set accepted: %v", err)
	}
}

func TestGeminiRejectsInvalidLogprobs(t *testing.T) {
	enabled, count := true, 1
	for _, request := range []openai.ChatCompletionRequest{
		{ChatGenerationOptions: openai.ChatGenerationOptions{TopLogprobs: &count}},
		{ChatGenerationOptions: openai.ChatGenerationOptions{Logprobs: &enabled, TopLogprobs: pointerInt(21)}},
	} {
		if _, err := geminiChatRequest(request); err == nil {
			t.Fatal("invalid logprobs request accepted")
		}
	}
	for _, body := range []geminiResponse{
		{Candidates: []geminiResponseCandidate{{LogprobsResult: &geminiLogprobsResult{ChosenCandidates: []geminiLogprobCandidate{{Token: "a", LogProbability: -0.1}}}}}},
	} {
		if _, err := geminiToChat(body, "model"); err == nil {
			t.Fatal("inconsistent native logprobs accepted")
		}
	}
}

func pointerInt(value int) *int { return &value }

func TestGeminiRejectsInvalidNativeSamplingControls(t *testing.T) {
	zero, tooLarge, tooManyCandidates := 0, 1000001, 21
	below, above := -2.1, 2.1
	for name, request := range map[string]openai.ChatCompletionRequest{
		"top_k_zero":        {ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &zero}},
		"top_k_too_large":   {ChatGenerationOptions: openai.ChatGenerationOptions{TopK: &tooLarge}},
		"frequency_penalty": {ChatGenerationOptions: openai.ChatGenerationOptions{FrequencyPenalty: &below}},
		"presence_penalty":  {ChatGenerationOptions: openai.ChatGenerationOptions{PresencePenalty: &above}},
		"n":                 {ChatGenerationOptions: openai.ChatGenerationOptions{N: &tooManyCandidates}},
		"reasoning_effort":  {ChatGenerationOptions: openai.ChatGenerationOptions{ReasoningEffort: "xhigh"}},
		"service_tier":      {ChatGenerationOptions: openai.ChatGenerationOptions{ServiceTier: "scale"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := geminiChatRequest(request); err == nil {
				t.Fatal("invalid native sampling control accepted")
			}
		})
	}
}

func TestGeminiNativeStreamUsageAndBilling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" || r.URL.Query().Has("key") {
			t.Error("invalid SSE URL")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"responseId\":\"stream-id\",\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"private\",\"thought\":true},{\"text\":\"hello\"}]}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\" world\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":2,\"thoughtsTokenCount\":3,\"totalTokenCount\":15}}\n\n")
	}))
	defer server.Close()
	recorder := &streamUsageRecorder{}
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "native", Type: "gemini", BaseURL: server.URL, APIKey: "fake-key", Stream: true, Models: []string{"gemini-test"}, Capabilities: []string{"chat", "stream"}}}, Modules: modules.NewPipeline([]modules.Module{recorder})}).(*Router)
	var payloads []string
	response, streamed, err := router.StreamChatCompletions(context.Background(), modules.RequestContext{Request: openai.ChatCompletionRequest{Model: "gemini-test", Messages: []openai.Message{{Role: "user", Content: "hello"}}}}, func(payload string) error { payloads = append(payloads, payload); return nil })
	if err != nil || !streamed {
		t.Fatalf("stream failed: %v", err)
	}
	if response.Choices[0].Message.Content != "hello world" || len(response.Choices[0].Message.Reasoning) != 1 || response.Choices[0].Message.Reasoning[0].Thinking != "private" || recorder.calls != 1 || recorder.usage.CompletionTokens != 5 || recorder.usage.TotalTokens != 15 {
		t.Fatalf("stream/billing result: %+v %+v", response, recorder)
	}
	if len(payloads) != 2 || !strings.Contains(payloads[0], `"thinking":"private"`) || strings.Contains(payloads[0], `"content":"private"`) || !strings.Contains(payloads[1], `"total_tokens":15`) {
		t.Fatalf("invalid SSE conversion: %v", payloads)
	}
}

func TestGeminiRejectsInvalidAndUnsupportedRequests(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	enabled := true
	for _, change := range []func(*openai.ChatCompletionRequest){
		func(r *openai.ChatCompletionRequest) { r.Model = "../other" },
		func(r *openai.ChatCompletionRequest) { r.ParallelToolCalls = &enabled },
		func(r *openai.ChatCompletionRequest) { r.ResponseFormat.JSONSchema.Schema = nil },
		func(r *openai.ChatCompletionRequest) { r.Messages[0].Role = "invalid" },
		func(r *openai.ChatCompletionRequest) {
			r.Messages = append(r.Messages, openai.Message{Role: "tool", ToolCallID: "missing", Content: "result"})
		},
		func(r *openai.ChatCompletionRequest) { r.Tools[0].Function.Strict = &enabled },
	} {
		request := geminiTestChat()
		change(&request)
		_, err := NewGemini(server.URL, "fake-key", true).ChatCompletions(context.Background(), request)
		var failure *Error
		if !errors.As(err, &failure) || failure.Class != FailureClientRequest {
			t.Fatalf("invalid request not rejected: %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached upstream")
	}
}

func TestGeminiDoesNotForwardCredentialsOnRedirect(t *testing.T) {
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := NewGemini(source.URL, "fake-key", false).ChatCompletions(context.Background(), geminiTestChat())
	if err == nil || leaked.Load() {
		t.Fatal("redirect followed with provider credential")
	}
}

func TestGeminiRejectsTruncatedStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n")
	}))
	defer server.Close()
	_, err := NewGemini(server.URL, "", true).StreamChatCompletions(context.Background(), geminiTestChat(), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "before completion") {
		t.Fatalf("truncated stream accepted: %v", err)
	}
}

func TestGeminiInlineVisionPreservesPartOrder(t *testing.T) {
	parts, err := geminiMessageParts([]any{map[string]any{"type": "text", "text": "before"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}}, map[string]any{"type": "text", "text": "after"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0].Text != "before" || parts[1].InlineData.MIMEType != "image/png" || parts[2].Text != "after" {
		t.Fatalf("vision content order lost: %+v", parts)
	}
}

func TestGeminiInlineAudioPreservesPartOrder(t *testing.T) {
	parts, err := geminiMessageParts([]any{map[string]any{"type": "text", "text": "before"}, map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "UklGRgAAAABXQVZF", "format": "wav"}}, map[string]any{"type": "text", "text": "after"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0].Text != "before" || parts[1].InlineData == nil || parts[1].InlineData.MIMEType != "audio/wav" || parts[2].Text != "after" {
		t.Fatalf("audio content order lost: %+v", parts)
	}
}

func TestGeminiMapsAdditionalInlineAudioFormats(t *testing.T) {
	tests := []struct {
		format, mediaType string
		data              []byte
	}{
		{"flac", "audio/flac", []byte("fLaCpayload")},
		{"ogg", "audio/ogg", []byte("OggSpayload")},
		{"opus", "audio/opus", []byte("OggSpayload")},
		{"aiff", "audio/aiff", []byte("FORM\x00\x00\x00\x00AIFFpayload")},
		{"aac", "audio/aac", []byte("\xff\xf1\x50\x80\x00\x1f\xfc")},
		{"webm", "audio/webm", []byte("\x1a\x45\xdf\xa3payload")},
		{"m4a", "audio/m4a", []byte("\x00\x00\x00\x18ftypisom")},
	}
	for _, test := range tests {
		encoded := base64.StdEncoding.EncodeToString(test.data)
		parts, err := geminiMessageParts([]any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": encoded, "format": test.format}}})
		if err != nil || len(parts) != 1 || parts[0].InlineData == nil || parts[0].InlineData.MIMEType != test.mediaType || parts[0].InlineData.Data != encoded {
			t.Fatalf("format=%s parts=%+v err=%v", test.format, parts, err)
		}
	}
}

func TestGeminiInlinePDFPreservesPartOrder(t *testing.T) {
	parts, err := geminiMessageParts([]any{
		map[string]any{"type": "text", "text": "before"},
		map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,JVBERi0xLjcKY29udGVudA==", "filename": "report.pdf"},
		map[string]any{"type": "text", "text": "after"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0].Text != "before" || parts[1].InlineData == nil || parts[1].InlineData.MIMEType != "application/pdf" || parts[2].Text != "after" {
		t.Fatalf("file content order lost: %+v", parts)
	}
}

func TestGeminiInlineVideoPreservesPartOrder(t *testing.T) {
	parts, err := geminiMessageParts([]any{
		map[string]any{"type": "text", "text": "before"},
		map[string]any{"type": "input_video", "input_video": map[string]any{"data": "AAAAE2Z0eXBpc29t", "format": "mp4"}},
		map[string]any{"type": "text", "text": "after"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 3 || parts[0].Text != "before" || parts[1].InlineData == nil || parts[1].InlineData.MIMEType != "video/mp4" || parts[2].Text != "after" {
		t.Fatalf("video content order lost: %+v", parts)
	}
}

func TestGeminiAdditionalInlineVideoFormats(t *testing.T) {
	formats := []struct{ format, mediaType, data string }{
		{"mpeg", "video/mpeg", "AAABunBheWxvYWQ="},
		{"mpg", "video/mpg", "AAABs3BheWxvYWQ="},
		{"mov", "video/mov", "AAAAGGZ0eXBxdCAg"},
		{"avi", "video/avi", "UklGRgAAAABBVkkgcGF5bG9hZA=="},
		{"flv", "video/x-flv", "RkxWAQVwYXlsb2Fk"},
		{"wmv", "video/wmv", "MCaydY5mzxGm2QCqAGLObHBheWxvYWQ="},
		{"3gpp", "video/3gpp", "AAAAGGZ0eXAzZ3A1"},
	}
	for _, test := range formats {
		t.Run(test.format, func(t *testing.T) {
			parts, err := geminiMessageParts([]any{map[string]any{"type": "input_video", "input_video": map[string]any{"data": test.data, "format": test.format}}})
			if err != nil || len(parts) != 1 || parts[0].InlineData == nil || parts[0].InlineData.MIMEType != test.mediaType {
				t.Fatalf("parts=%+v err=%v", parts, err)
			}
		})
	}
}

func TestGeminiUsageValidation(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, usage := range []geminiUsage{{Prompt: -1}, {ToolUsePrompt: -1}, {Prompt: 1, Cached: 2}, {Prompt: 10, ToolUsePrompt: 4, Candidates: 2, Thoughts: 3, Total: 18}, {Prompt: maxInt, ToolUsePrompt: 1, Total: maxInt}} {
		if _, err := geminiToChat(geminiResponse{Usage: &usage}, "test"); err == nil {
			t.Fatalf("invalid usage accepted: %+v", usage)
		}
	}
}

func TestGeminiStreamStopsOnClientWriteError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}]}\n\n")
	}))
	defer server.Close()
	sentinel := errors.New("client disconnected")
	_, err := NewGemini(server.URL, "", true).StreamChatCompletions(context.Background(), geminiTestChat(), func(string) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("write failure lost: %v", err)
	}
}

func TestGeminiUpstreamErrorIsClassifiedAndRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = fmt.Fprint(w, `{"error":{"code":429,"message":"private echoed input"}}`)
	}))
	defer server.Close()
	_, err := NewGemini(server.URL, "", false).ChatCompletions(context.Background(), geminiTestChat())
	var failure *Error
	if !errors.As(err, &failure) || failure.Class != FailureRateLimit || strings.Contains(err.Error(), "private") {
		t.Fatalf("incorrect upstream error: %v", err)
	}
}

func TestGeminiParallelToolResultsShareNativeContent(t *testing.T) {
	request := geminiTestChat()
	request.Messages = append(request.Messages, openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{
		{ID: "one", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{}`}},
		{ID: "two", Type: "function", Function: openai.FunctionCall{Name: "weather", Arguments: `{}`}},
	}}, openai.Message{Role: "tool", ToolCallID: "one", Content: "first"}, openai.Message{Role: "tool", ToolCallID: "two", Content: "second"})
	native, err := geminiChatRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(native.Contents) != 3 || len(native.Contents[2].Parts) != 2 || native.Contents[2].Parts[1].FunctionResponse.ID != "two" {
		t.Fatalf("parallel results not grouped: %+v", native.Contents)
	}
}

func TestGeminiStreamFinishesAccumulatedToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"index\":0,\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"weather\",\"args\":{}},\"thoughtSignature\":\"opaque\"}]}}]}\n\ndata: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"}]}\n\n")
	}))
	defer server.Close()
	var payloads []string
	response, err := NewGemini(server.URL, "", true).StreamChatCompletions(context.Background(), geminiTestChat(), func(s string) error { payloads = append(payloads, s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if response.Choices[0].FinishReason != "tool_calls" || response.Choices[0].Message.ToolCalls[0].ExtraContent.Google.ThoughtSignature != "opaque" || !strings.Contains(payloads[1], `"finish_reason":"tool_calls"`) {
		t.Fatalf("tool stream not completed correctly: %+v %v", response, payloads)
	}
}

func TestGeminiToolResponsePreservesJSONObjects(t *testing.T) {
	for _, value := range []any{map[string]any{"answer": "yes"}, `{"answer":"yes"}`} {
		result := geminiToolResponse(value)
		if result["answer"] != "yes" || result["result"] != nil {
			t.Fatalf("object wrapped as text: %v", result)
		}
	}
	for _, value := range []any{"plain text", `[1,2]`, `null`} {
		if result := geminiToolResponse(value); result["result"] != value {
			t.Fatalf("plain tool result changed: %v", result)
		}
	}
}

func TestGeminiFunctionCallsWithoutArguments(t *testing.T) {
	for _, args := range []string{"", `,"args":null`, `,"args":{}`} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("args=%s/stream=%t", args, stream), func(t *testing.T) {
				payload := `{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"clock"` + args + `},"thoughtSignature":"opaque"}]},"finishReason":"STOP"}]}`
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stream {
						_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
					} else {
						_, _ = fmt.Fprint(w, payload)
					}
				}))
				defer server.Close()
				client := NewGemini(server.URL, "", true)
				request := openai.ChatCompletionRequest{Model: "m", Messages: []openai.Message{{Role: "user", Content: "time?"}}}
				var response openai.ChatCompletionResponse
				var err error
				var chunks strings.Builder
				if stream {
					response, err = client.StreamChatCompletions(context.Background(), request, func(chunk string) error { chunks.WriteString(chunk); return nil })
				} else {
					response, err = client.ChatCompletions(context.Background(), request)
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(response.Choices) != 1 || len(response.Choices[0].Message.ToolCalls) != 1 {
					t.Fatalf("missing call: %+v", response)
				}
				call := response.Choices[0].Message.ToolCalls[0]
				if call.Function.Arguments != "{}" || call.ExtraContent.Google.ThoughtSignature != "opaque" || response.Choices[0].FinishReason != "tool_calls" {
					t.Fatalf("invalid normalized call: %+v", call)
				}
				if stream && !strings.Contains(chunks.String(), `"arguments":"{}"`) {
					t.Fatal("stream arguments lost")
				}
			})
		}
	}
	for _, args := range []string{`[]`, `1`, `"text"`} {
		var body geminiResponse
		err := json.Unmarshal([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"clock","args":`+args+`}}]},"finishReason":"STOP"}]}`), &body)
		if err == nil {
			_, err = geminiToChat(body, "m")
		}
		if err == nil {
			t.Fatalf("non-object args accepted: %s", args)
		}
	}
}
