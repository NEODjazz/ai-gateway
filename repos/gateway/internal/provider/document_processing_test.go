package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/documentprocessing"
	"ai-gateway-gateway/internal/modelcatalog"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type documentConverterFake struct {
	calls  int
	text   string
	err    error
	owners []string
}

func (f *documentConverterFake) Convert(_ context.Context, _ openai.ResponseFileAttachment, owner string) (string, error) {
	f.calls++
	f.owners = append(f.owners, owner)
	return f.text, f.err
}

func documentInput() any {
	return []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_file", "filename": "test.pdf", "file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("%PDF-1.7 test"))}}}}
}

func documentRouter(converter documentprocessing.Converter) *Router {
	return New(Config{DocumentConverter: converter, Endpoints: []config.ProviderEndpointConfig{{Name: "local", Type: "ollama", Models: []string{"qwen"}, Capabilities: []string{"chat", "responses"}, DocumentProcessing: "docling"}}}).(*Router)
}

func TestDocumentConversionCopiesInputAndBindsOwner(t *testing.T) {
	converter := &documentConverterFake{text: "document text"}
	r := documentRouter(converter)
	input := documentInput()
	before, _ := json.Marshal(input)
	response := openai.ResponseRequest{Model: "qwen", Input: input}
	req := modules.RequestContext{CredentialID: "key", OrganizationID: "one", UserID: "user", Request: openai.ChatCompletionRequest{Model: "qwen"}, ResponseRequest: &response}
	prepared, err := r.PrepareDocumentInput(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(input)
	if string(before) != string(after) {
		t.Fatal("original file input mutated")
	}
	if openai.HasResponseFiles(*prepared.ResponseRequest) || !strings.Contains(openai.ContentText(prepared.ResponseRequest.Input), "document text") {
		t.Fatalf("input=%v", prepared.ResponseRequest.Input)
	}
	if len(r.routeCandidates(t.Context(), prepared, prepared.Request, "responses")) != 1 {
		t.Fatal("bound deployment unavailable")
	}
	req.OrganizationID = "two"
	if _, err := r.PrepareDocumentInput(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if converter.owners[0] == converter.owners[1] {
		t.Fatal("document tasks shared tenant scope")
	}
	deployment := r.ListModelDeployments(t.Context())[0]
	deployment.DocumentProcessing = "native"
	if _, err := r.UpdateModelDeployment(deployment.ID, deployment); err != nil {
		t.Fatal(err)
	}
	if len(r.routeCandidates(t.Context(), prepared, prepared.Request, "responses")) != 0 {
		t.Fatal("changed deployment reused converted request")
	}
}

func TestDocumentConversionFailuresStopInference(t *testing.T) {
	for _, tc := range []struct {
		name      string
		converter *documentConverterFake
		want      error
	}{
		{"unavailable", nil, documentprocessing.ErrUnavailable},
		{"empty", &documentConverterFake{}, documentprocessing.ErrFailed},
		{"queue", &documentConverterFake{err: documentprocessing.ErrBusy}, documentprocessing.ErrBusy},
		{"too large", &documentConverterFake{text: strings.Repeat("x", maxConvertedDocumentBytes+1)}, documentprocessing.ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var converter documentprocessing.Converter
			if tc.converter != nil {
				converter = tc.converter
			}
			r := documentRouter(converter)
			request := openai.ResponseRequest{Model: "qwen", Input: documentInput()}
			_, err := r.PrepareDocumentInput(t.Context(), modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "qwen"}, ResponseRequest: &request})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestConvertedDocumentContextLimit(t *testing.T) {
	converter := &documentConverterFake{text: strings.Repeat("word ", 100)}
	r := documentRouter(converter)
	catalog, err := modelcatalog.Parse(`{"version":"test","models":[{"provider":"ollama","model":"qwen","capabilities":["chat","responses"],"max_input_tokens":20}]}`)
	if err != nil {
		t.Fatal(err)
	}
	r.catalog.SetAuthoritative(catalog)
	request := openai.ResponseRequest{Model: "qwen", Input: documentInput()}
	_, err = r.PrepareDocumentInput(t.Context(), modules.RequestContext{CredentialID: "key", Request: openai.ChatCompletionRequest{Model: "qwen"}, ResponseRequest: &request})
	if !errors.Is(err, ErrDocumentContextLimit) {
		t.Fatalf("context limit=%v", err)
	}
}

func TestDocumentProcessingCacheAndBackgroundIsolation(t *testing.T) {
	req := modules.RequestContext{CredentialID: "key", Metadata: map[string]string{documentBindingMetadata: "one", "gateway.document.processing": "docling"}}
	before := cacheIsolationScope(req)
	req.Metadata[documentBindingMetadata] = "two"
	if before == cacheIsolationScope(req) {
		t.Fatal("cache reused another document processing policy")
	}
	saved := backgroundJobMetadata(req.Metadata)
	if saved[documentBindingMetadata] != "two" {
		t.Fatal("background job lost deployment binding")
	}
}

func TestDocumentPolicySurvivesOnboardingUpdate(t *testing.T) {
	r := documentRouter(nil)
	catalog, err := modelcatalog.Parse(`{"version":"updated","models":[{"provider":"ollama","model":"qwen","capabilities":["chat","responses"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.PlanModelOnboarding(t.Context(), ModelOnboardingInput{Catalog: catalog, UpdateExistingDeployments: true, Deployments: []ModelDeployment{{ID: "local", ProviderID: "local", Models: []string{"qwen"}, Capabilities: []string{"chat", "responses"}, Weight: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Deployments) != 1 || plan.Deployments[0].DocumentProcessing != "docling" {
		t.Fatalf("onboarding reset policy: %+v", plan.Deployments)
	}
}
