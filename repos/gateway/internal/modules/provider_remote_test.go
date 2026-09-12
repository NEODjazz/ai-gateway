package modules

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/openai"
)

func TestProviderRemoteModuleSkipsDisabledProvider(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(ScanResponse{Allowed: true})
	}))
	defer server.Close()

	module := NewProviderRemoteModule("dlp", true, server.URL+"/scan")
	err := module.Handle(context.Background(), &RequestContext{
		Metadata: map[string]string{"provider.modules.dlp.enabled": "false"},
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{{Role: "user", Content: "secret"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("remote module should not be called for disabled provider")
	}
}

func TestScanPayloadIncludesEmbeddingText(t *testing.T) {
	req := RequestContext{EmbeddingRequest: &openai.EmbeddingRequest{Model: "embed", Input: []any{"first secret", "second secret"}}}
	payload := scanPayload(&req)
	if !strings.Contains(payload, "embedding_input: first secret\nsecond secret") {
		t.Fatalf("embedding text missing from scan projection: %q", payload)
	}
}

func TestBedrockDocumentsReachDLPAndAVProjections(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("account user@example.com"))
	prompt := "inspect"
	chat, err := (openai.BedrockConverseRequest{Messages: []openai.BedrockMessage{{Role: "user", Content: []openai.BedrockContentBlock{
		{Text: &prompt},
		{Document: &openai.BedrockDocument{Format: "txt", Name: "Customer Export", Source: openai.BedrockDocumentSource{Bytes: data}}},
	}}}}).ChatRequest("model", "")
	if err != nil {
		t.Fatal(err)
	}
	request := RequestContext{Request: chat}
	attachments, err := requestImageAttachments(&request)
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "text/plain" || attachments[0].Data != data {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
	if payload := scanPayload(&request); !strings.Contains(payload, "document: account user@example.com") {
		t.Fatalf("document missing from DLP projection: %q", payload)
	}
}

func TestBedrockRequestMetadataReachesDLPProjection(t *testing.T) {
	req := RequestContext{Request: openai.ChatCompletionRequest{BedrockRequestMetadata: map[string]string{
		"z-key": "private-z", "a-key": "user@example.com",
	}}}
	if payload := scanPayload(&req); payload != "request_metadata: a-key=user@example.com\nrequest_metadata: z-key=private-z" {
		t.Fatalf("request metadata missing or unstable: %q", payload)
	}
}

func TestBedrockAdditionalModelRequestFieldsReachDLPProjection(t *testing.T) {
	req := RequestContext{Request: openai.ChatCompletionRequest{BedrockAdditionalModelRequestFields: json.RawMessage(`{"user_context":"user@example.com"}`)}}
	if payload := scanPayload(&req); payload != `additional_model_request_fields: {"user_context":"user@example.com"}` {
		t.Fatalf("additional model request fields missing: %q", payload)
	}
}

func TestScanPayloadIncludesImageGenerationPrompt(t *testing.T) {
	req := RequestContext{ImageGenerationRequest: &openai.ImageGenerationRequest{Model: "image", Prompt: "private image prompt"}}
	if payload := scanPayload(&req); payload != "image_prompt: private image prompt" {
		t.Fatalf("image prompt missing from scan projection: %q", payload)
	}
}

func TestImageEditProjectsPromptAndAttachmentsToScanners(t *testing.T) {
	attachment := openai.ImageAttachment{MediaType: "image/png", Data: "iVBORw0KGgpmaXh0dXJl"}
	req := RequestContext{ImageEditRequest: &openai.ImageEditRequest{Model: "image", Prompt: "private edit prompt", Images: []openai.ImageAttachment{attachment}, Mask: &attachment}}
	if payload := scanPayload(&req); payload != "image_edit_prompt: private edit prompt" {
		t.Fatalf("payload=%q", payload)
	}
	attachments, err := requestImageAttachments(&req)
	if err != nil || len(attachments) != 2 || attachments[0] != attachment || attachments[1] != attachment {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
}

func TestImageVariationProjectsAttachmentToAV(t *testing.T) {
	attachment := openai.ImageAttachment{MediaType: "image/png", Data: "iVBORw0KGgpmaXh0dXJl"}
	req := RequestContext{ImageVariationRequest: &openai.ImageVariationRequest{Model: "image", Image: attachment}}
	attachments, err := requestImageAttachments(&req)
	if err != nil || len(attachments) != 1 || attachments[0] != attachment || scanPayload(&req) != "" {
		t.Fatalf("attachments=%+v payload=%q err=%v", attachments, scanPayload(&req), err)
	}
}

func TestResponseAudioProjectsAttachmentToAV(t *testing.T) {
	request := RequestContext{ResponseRequest: &openai.ResponseRequest{Input: []any{map[string]any{
		"type": "input_audio", "input_audio": map[string]any{"data": "UklGRgAAAABXQVZF", "format": "wav"},
	}}}}
	attachments, err := requestImageAttachments(&request)
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "audio/wav" || attachments[0].Data != "UklGRgAAAABXQVZF" {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
}

func TestResponsePDFProjectsAttachmentToAV(t *testing.T) {
	request := RequestContext{ResponseRequest: &openai.ResponseRequest{Input: []any{map[string]any{
		"type": "input_file", "file_data": "data:application/pdf;base64,JVBERi0xLjcKY29udGVudA==", "filename": "report.pdf",
	}}}}
	attachments, err := requestImageAttachments(&request)
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "application/pdf" || attachments[0].Data != "JVBERi0xLjcKY29udGVudA==" {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
}

func TestChatPDFProjectsAttachmentToAV(t *testing.T) {
	request := RequestContext{Request: openai.ChatCompletionRequest{
		Messages: []openai.Message{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64,JVBERi0xLjcKY29udGVudA==", "filename": "report.pdf"},
				},
			},
		},
	}}
	attachments, err := requestImageAttachments(&request)
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "application/pdf" || attachments[0].Data != "JVBERi0xLjcKY29udGVudA==" {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
}

func TestAudioTranscriptionProjectsFilesToAVAndHintsToDLP(t *testing.T) {
	file := openai.AudioAttachment{Filename: "sample.wav", MediaType: "audio/wav", Data: "UklGRi4uLi5XQVZFZGF0YQ=="}
	request := openai.AudioTranscriptionRequest{Model: "audio", File: file, Prompt: "private speaker", Keywords: []string{"private company", "private person"}, KnownSpeakerNames: []string{"Jane"}, KnownSpeakerReferences: []openai.AudioAttachment{{Filename: "reference.wav", MediaType: file.MediaType, Data: file.Data}}}
	req := RequestContext{AudioTranscriptionRequest: &request}
	attachments, err := requestImageAttachments(&req)
	if err != nil || len(attachments) != 2 || attachments[0].MediaType != "audio/wav" || attachments[0].Data != request.File.Data || attachments[1].Data != request.KnownSpeakerReferences[0].Data {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
	if payload := scanPayload(&req); payload != "transcription_prompt: private speaker\ntranscription_keyword: private company\ntranscription_keyword: private person\nknown_speaker_name: Jane" {
		t.Fatalf("payload=%q", payload)
	}
}

func TestOCRProjectsInlineDocumentToAVAndPromptToDLP(t *testing.T) {
	document := "data:application/pdf;base64,JVBERi0xLjcK"
	request := openai.OCRRequest{Model: "ocr", Document: openai.OCRDocument{Type: "document_url", DocumentURL: document}, DocumentAnnotationPrompt: "private extraction rules"}
	req := RequestContext{OCRRequest: &request}
	attachments, err := requestImageAttachments(&req)
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "application/pdf" || attachments[0].Data != "JVBERi0xLjcK" {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
	if payload := scanPayload(&req); payload != "ocr_annotation_prompt: private extraction rules" {
		t.Fatalf("payload=%q", payload)
	}
}

func TestScanPayloadIncludesRerankTextOnly(t *testing.T) {
	req := RequestContext{RerankRequest: &openai.RerankRequest{Model: "rerank", Query: "private query", Documents: []any{"private document", map[string]any{"text": "object document", "binary": []byte{1, 2}}}}}
	payload := scanPayload(&req)
	if !strings.Contains(payload, "private query") || !strings.Contains(payload, "object document") || strings.Contains(payload, "AQI") {
		t.Fatalf("unexpected scan projection: %q", payload)
	}
}

func TestModerationPayloadAndImageScanning(t *testing.T) {
	req := RequestContext{ModerationRequest: &openai.ModerationRequest{Input: []any{map[string]any{"type": "text", "text": "private moderation text"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}}}}}
	if payload := scanPayload(&req); !strings.Contains(payload, "private moderation text") {
		t.Fatalf("moderation text missing: %q", payload)
	}
	if _, err := requestImageAttachments(&req); err == nil {
		t.Fatal("expected remote moderation image to fail closed for AV scanning")
	}
}

func TestScanPayloadIncludesToolArgumentsAndResponseFunctionOutput(t *testing.T) {
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{
		Role: "assistant", ToolCalls: []openai.ToolCall{{Function: openai.FunctionCall{Arguments: `{"email":"user@example.com"}`}}},
	}}}, ResponseRequest: &openai.ResponseRequest{Input: []any{map[string]any{"type": "function_call_output", "output": "secret-result"}}}}
	payload := scanPayload(&req)
	if !strings.Contains(payload, "user@example.com") || !strings.Contains(payload, "secret-result") {
		t.Fatalf("tool content missing from scan projection: %s", payload)
	}
}

func TestScanPayloadIncludesReasoningTextWithoutOpaqueData(t *testing.T) {
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", Reasoning: []openai.ReasoningBlock{
		{Type: "thinking", Thinking: "private plan", Signature: "secret-signature"},
		{Type: "redacted_thinking", Data: "b3BhcXVl"},
	}}}}}
	payload := scanPayload(&req)
	if payload != "reasoning: private plan" || strings.Contains(payload, "secret-signature") || strings.Contains(payload, "b3BhcXVl") {
		t.Fatalf("reasoning DLP projection=%q", payload)
	}
}

func TestProviderRemoteModuleRequiresURLWhenEnabled(t *testing.T) {
	module := NewProviderRemoteModule("av", true, "")
	err := module.Handle(context.Background(), &RequestContext{
		Metadata: map[string]string{"provider.modules.av.enabled": "true"},
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{{Role: "user", Content: "secret"}},
		},
	})
	if err == nil {
		t.Fatal("expected missing url error")
	}
}

func TestRemoteModuleMapsContentRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnavailableForLegalReasons)
	}))
	defer server.Close()

	_, err := callRemote[ScanRequest, ScanResponse](context.Background(), newRemoteHTTPClient(), server.URL, ScanRequest{})
	if !errors.Is(err, ErrContentRejected) {
		t.Fatalf("expected content rejected error, got %v", err)
	}
}

func TestRemoteModuleRejectsAllowedFalse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ScanResponse{Allowed: false})
	}))
	defer server.Close()
	module := NewProviderRemoteModule("dlp", false, server.URL)
	err := module.Handle(context.Background(), &RequestContext{Metadata: map[string]string{"provider.modules.dlp.enabled": "true"}})
	if !errors.Is(err, ErrContentRejected) {
		t.Fatalf("expected content rejection, got %v", err)
	}
}

func TestAttachedPolicyFailsClosedWhenOptionalScannerIsUnavailable(t *testing.T) {
	module := NewProviderRemoteModule("dlp", false, "")
	req := RequestContext{Metadata: map[string]string{
		"provider.modules.dlp.enabled": "true",
		"policy.guardrail.required":    "true",
	}}
	err := NewPipeline([]Module{module}).Run(context.Background(), &req)
	if !errors.Is(err, ErrGuardrailUnavailable) {
		t.Fatalf("expected attached policy to fail closed, got %v", err)
	}
}

func TestOptionalAVFailsClosedForImageWhenScannerUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	module := NewProviderRemoteModule("av", false, server.URL)
	req := RequestContext{
		Metadata: map[string]string{"provider.modules.av.enabled": "true"},
		Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
		}}}},
	}
	err := NewPipeline([]Module{module}).Run(context.Background(), &req)
	if !errors.Is(err, ErrGuardrailUnavailable) {
		t.Fatalf("optional AV failed open: %v", err)
	}
}

func TestProviderRemoteModuleDoesNotSendBearerToken(t *testing.T) {
	const bearer = "super-secret-bearer-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, found := body["api_key"]; found {
			t.Fatal("provider module request must not contain api_key")
		}
		if _, found := body["user_id"]; found {
			t.Fatal("scan module request must not contain identity")
		}
		_ = json.NewEncoder(w).Encode(ScanResponse{Allowed: true})
	}))
	defer server.Close()

	module := NewProviderRemoteModule("dlp", true, server.URL)
	err := module.Handle(context.Background(), &RequestContext{
		APIKey: bearer,
		UserID: "user-1",
		Metadata: map[string]string{
			"provider.modules.dlp.enabled": "true",
		},
		Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: "hello"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDLPScansProviderOutputAndRejectsBeforeDelivery(t *testing.T) {
	var received ScanRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(ScanResponse{Allowed: false})
	}))
	defer server.Close()
	module := NewProviderRemoteModule("dlp", false, server.URL)
	refusal := "private refusal"
	documentIndex := 0
	req := RequestContext{
		RequestID: "execution-1",
		Metadata:  map[string]string{"provider.modules.dlp.output_enabled": "true"},
		Request:   openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: "request secret"}}},
		Response: &openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{
			Role: "assistant", Content: "response secret", Refusal: &refusal,
			ToolCalls: []openai.ToolCall{{Function: openai.FunctionCall{Arguments: `{"email":"user@example.com"}`}}},
			Annotations: []openai.ChatAnnotation{{Type: "source_citation", SourceCitation: &openai.ChatSourceCitation{
				Title: "report", Source: "citation source secret", SourceContent: []string{"citation excerpt secret"},
				LocationType: "document_page", DocumentIndex: &documentIndex,
			}}},
			NativeContent: []json.RawMessage{
				json.RawMessage(`{"type":"web_fetch_tool_result","content":{"type":"document","source":{"type":"text","data":"native document secret"}}}`),
				json.RawMessage(`{"type":"bedrock_guardrail_trace","trace":{"guardrail":{"actionReason":"trace secret"}}}`),
			},
		}}}},
	}
	err := NewPipeline([]Module{module}).RunPostResponse(context.Background(), &req)
	if !errors.Is(err, ErrContentRejected) {
		t.Fatalf("expected output rejection, got %v", err)
	}
	if received.RequestID != "execution-1" || !strings.Contains(received.Content, "response secret") || !strings.Contains(received.Content, "user@example.com") || !strings.Contains(received.Content, "citation source secret") || !strings.Contains(received.Content, "citation excerpt secret") || !strings.Contains(received.Content, "native document secret") || !strings.Contains(received.Content, "trace secret") || strings.Contains(received.Content, "request secret") {
		t.Fatalf("unexpected output projection: %+v", received)
	}
}

func TestOutputDLPFailsClosedWhenProjectionOrScannerIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint string
		content  string
	}{
		{name: "missing scanner", content: "response"},
		{name: "oversized projection", endpoint: "http://unused.invalid", content: strings.Repeat("x", maxResponseScanBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			module := NewProviderRemoteModule("dlp", false, tc.endpoint)
			req := RequestContext{Metadata: map[string]string{"provider.modules.dlp.output_enabled": "true"}, Response: &openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{Content: tc.content}}}}}
			err := NewPipeline([]Module{module}).RunPostResponse(context.Background(), &req)
			if !errors.Is(err, ErrGuardrailUnavailable) {
				t.Fatalf("output DLP failed open: %v", err)
			}
		})
	}
}

func TestOutputDLPSkipsResponsesWithoutExplicitPolicy(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(ScanResponse{Allowed: true})
	}))
	defer server.Close()
	module := NewProviderRemoteModule("dlp", true, server.URL)
	req := RequestContext{Response: &openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{Content: "response"}}}}}
	if err := NewPipeline([]Module{module}).RunPostResponse(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("output scanner called without output policy")
	}
}

func TestAVReceivesBinaryAttachmentsWhileDLPReceivesTextOnly(t *testing.T) {
	for _, moduleName := range []string{"dlp", "av"} {
		t.Run(moduleName, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request ScanRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.Content != "user: describe" {
					t.Fatalf("unexpected text projection: %q", request.Content)
				}
				if moduleName == "av" && (len(request.Attachments) != 1 || request.Attachments[0].Data != "iVBORw0KGgo=") {
					t.Fatalf("AV did not receive image attachment: %+v", request.Attachments)
				}
				if moduleName == "dlp" && len(request.Attachments) != 0 {
					t.Fatalf("DLP received binary attachment: %+v", request.Attachments)
				}
				_ = json.NewEncoder(w).Encode(ScanResponse{Allowed: true})
			}))
			defer server.Close()
			module := NewProviderRemoteModule(moduleName, true, server.URL)
			req := RequestContext{
				Metadata: map[string]string{"provider.modules." + moduleName + ".enabled": "true"},
				Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: []any{
					map[string]any{"type": "text", "text": "describe"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,iVBORw0KGgo="}},
				}}}},
			}
			if err := module.Handle(context.Background(), &req); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAVReceivesChatAudioInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ScanRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Attachments) != 1 || request.Attachments[0].MediaType != "audio/wav" || request.Attachments[0].Data != "UklGRgAAAABXQVZF" {
			t.Fatalf("audio attachment missing: %+v", request.Attachments)
		}
		_ = json.NewEncoder(w).Encode(ScanResponse{Allowed: true})
	}))
	defer server.Close()
	module := NewProviderRemoteModule("av", true, server.URL)
	req := RequestContext{
		Metadata: map[string]string{"provider.modules.av.enabled": "true"},
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{
				{Role: "user", Content: []any{map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": "UklGRgAAAABXQVZF", "format": "wav"}}}},
			},
		},
	}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineStopsOnContentRejectedEvenWhenOptional(t *testing.T) {
	pipeline := NewPipeline([]Module{
		rejectingModule{},
	})

	err := pipeline.Run(context.Background(), &RequestContext{})
	if !errors.Is(err, ErrContentRejected) {
		t.Fatalf("expected content rejected error, got %v", err)
	}
}

type rejectingModule struct{}

type postLifecycleModule struct {
	name   string
	err    error
	called *bool
}

func (m postLifecycleModule) Name() string                                { return m.name }
func (postLifecycleModule) Required() bool                                { return false }
func (postLifecycleModule) Handle(context.Context, *RequestContext) error { return nil }
func (postLifecycleModule) PostResponseEnabled() bool                     { return true }
func (m postLifecycleModule) HandlePostResponse(context.Context, *RequestContext) error {
	*m.called = true
	return m.err
}

func TestPostResponseLifecycleContinuesAfterOutputRejection(t *testing.T) {
	rejected, billed := false, false
	pipeline := NewPipeline([]Module{
		postLifecycleModule{name: "dlp", err: ErrContentRejected, called: &rejected},
		postLifecycleModule{name: "billing", called: &billed},
	})
	err := pipeline.RunPostResponse(context.Background(), &RequestContext{})
	if !errors.Is(err, ErrContentRejected) || !rejected || !billed {
		t.Fatalf("post-response lifecycle was truncated: rejected=%v billed=%v err=%v", rejected, billed, err)
	}
}

func TestNamedModuleLifecyclePreservesOptionalAndTerminalErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		terminal bool
	}{
		{name: "optional error", err: errors.New("scanner timeout")},
		{name: "content rejection", err: ErrContentRejected, terminal: true},
		{name: "guardrail unavailable", err: ErrGuardrailUnavailable, terminal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			preCalled, postCalled := false, false
			pre := namedLifecycleModule{name: "dlp", err: test.err, called: &preCalled}
			post := postLifecycleModule{name: "dlp", err: test.err, called: &postCalled}
			preErr := NewPipeline([]Module{pre}).RunNamed(t.Context(), &RequestContext{}, "dlp")
			postErr := NewPipeline([]Module{post}).RunNamedPostResponse(t.Context(), &RequestContext{}, "dlp")
			if preCalled != true || postCalled != true || (preErr != nil) != test.terminal || (postErr != nil) != test.terminal {
				t.Fatalf("pre_called=%v post_called=%v pre_err=%v post_err=%v", preCalled, postCalled, preErr, postErr)
			}
		})
	}
	if err := NewPipeline(nil).RunNamed(t.Context(), &RequestContext{}, "dlp"); !errors.Is(err, ErrGuardrailUnavailable) {
		t.Fatalf("missing named module error=%v", err)
	}
}

type namedLifecycleModule struct {
	name   string
	err    error
	called *bool
}

func (m namedLifecycleModule) Name() string { return m.name }
func (namedLifecycleModule) Required() bool { return false }
func (m namedLifecycleModule) Handle(context.Context, *RequestContext) error {
	*m.called = true
	return m.err
}

type recordingModuleObserver struct {
	module string
	phase  string
	result string
}

func (o *recordingModuleObserver) ObserveModule(module, phase, result string, _ time.Duration) {
	o.module, o.phase, o.result = module, phase, result
}

func (rejectingModule) Name() string {
	return "optional-rejecting"
}

func (rejectingModule) Required() bool {
	return false
}

func (rejectingModule) Handle(context.Context, *RequestContext) error {
	return ErrContentRejected
}

func TestPipelineObservesBoundedModuleOutcome(t *testing.T) {
	observer := &recordingModuleObserver{}
	pipeline := NewPipelineWithObserver([]Module{rejectingModule{}}, observer)
	if err := pipeline.Run(context.Background(), &RequestContext{}); !errors.Is(err, ErrContentRejected) {
		t.Fatalf("expected content rejection, got %v", err)
	}
	if observer.module != "optional-rejecting" || observer.phase != "pre" || observer.result != "content_rejected" {
		t.Fatalf("unexpected module observation: %+v", observer)
	}
}
