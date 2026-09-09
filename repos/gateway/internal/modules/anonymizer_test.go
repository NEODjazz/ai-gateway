package modules

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestAnonymizerDoesNotTransformImagePayload(t *testing.T) {
	image := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("api_key=sk-test-1234567890abcdef"))
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "user@example.com"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": image}},
	}}}}}
	if err := NewAnonymizerModule(true, RuleEmail, RuleAPIKey).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	content := req.Request.Messages[0].Content.([]any)
	if content[0].(map[string]any)["text"] == "user@example.com" {
		t.Fatal("text was not anonymized")
	}
	if got := content[1].(map[string]any)["image_url"].(map[string]any)["url"]; got != image {
		t.Fatalf("image payload was changed: %q", got)
	}
}

func TestAnonymizerMasksSensitiveData(t *testing.T) {
	module := NewAnonymizerModule(true, "all")
	req := RequestContext{
		Request: openai.ChatCompletionRequest{
			Model: "demo",
			Messages: []openai.Message{
				{
					Role: "user",
					Content: strings.Join([]string{
						"Иванов Иван Иванович",
						"user@example.com",
						"+7 999 123-45-67",
						"г. Москва ул. Ленина д. 1 кв. 2",
						"4510 123456",
						"7707083893",
						"4111 1111 1111 1111",
						"192.168.1.10",
						"api_key=sk-test-1234567890abcdef",
						"password=qwerty123",
						"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.signature123",
					}, "\n"),
				},
			},
		},
	}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	content := openai.ContentText(req.Request.Messages[0].Content)
	expected := []string{
		"{{PERSON_RU_1}}",
		"{{EMAIL_1}}",
		"{{PHONE_1}}",
		"{{ADDRESS_RU_1}}",
		"{{PASSPORT_RU_1}}",
		"{{INN_1}}",
		"{{BANK_CARD_1}}",
		"{{IP_1}}",
		"{{API_KEY_1}}",
		"{{SECRET_1}}",
		"{{JWT_1}}",
	}

	for _, placeholder := range expected {
		if !strings.Contains(content, placeholder) {
			t.Fatalf("expected %s in anonymized content:\n%s", placeholder, content)
		}
	}
}

func TestAnonymizerMasksEmbeddingInput(t *testing.T) {
	request := openai.EmbeddingRequest{Model: "embed", Input: []any{"send to user@example.com", "call +1 202-555-0123"}}
	req := RequestContext{EmbeddingRequest: &request}
	module := NewAnonymizerModule(true, RuleEmail, RulePhone)
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	text := openai.EmbeddingInputText(req.EmbeddingRequest.Input)
	if strings.Contains(text, "user@example.com") || strings.Contains(text, "202-555") {
		t.Fatalf("embedding input was not masked: %s", text)
	}
	if len(req.AnonymizationValues) != 2 {
		t.Fatalf("unexpected replacements: %+v", req.AnonymizationValues)
	}
}

func TestAnonymizerMasksImageGenerationPrompt(t *testing.T) {
	request := openai.ImageGenerationRequest{Model: "image", Prompt: "draw user@example.com"}
	req := RequestContext{ImageGenerationRequest: &request}
	if err := NewAnonymizerModule(true, RuleEmail).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if request.Prompt != "draw {{EMAIL_1}}" {
		t.Fatalf("prompt=%q", request.Prompt)
	}
}

func TestAnonymizerModerationInputPreservesImages(t *testing.T) {
	request := openai.ModerationRequest{Input: []any{map[string]any{"type": "text", "text": "send to user@example.com"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}}}}
	req := RequestContext{ModerationRequest: &request}
	if err := NewAnonymizerModule(true, RuleEmail).Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if got := openai.ModerationInputText(req.ModerationRequest.Input); got != "send to {{EMAIL_1}}" {
		t.Fatalf("unexpected text: %q", got)
	}
	parts := req.ModerationRequest.Input.([]any)
	if parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "https://example.test/image.png" {
		t.Fatal("image URL changed")
	}
}

func TestAnonymizerRulesCanBeLimited(t *testing.T) {
	module := NewAnonymizerModule(true, RuleEmail)
	req := RequestContext{
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{
				{Role: "user", Content: "user@example.com +7 999 123-45-67"},
			},
		},
	}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	content := openai.ContentText(req.Request.Messages[0].Content)
	if !strings.Contains(content, "{{EMAIL_1}}") {
		t.Fatalf("expected email to be masked: %s", content)
	}
	if strings.Contains(content, "{{PHONE_1}}") {
		t.Fatalf("expected phone to stay visible when phone rule is disabled: %s", content)
	}
}

func TestAnonymizerRulesCanBeDisabled(t *testing.T) {
	module := NewAnonymizerModule(true, parseAnonymizerRules("none")...)
	req := RequestContext{
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{
				{Role: "user", Content: "user@example.com"},
			},
		},
	}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	content := openai.ContentText(req.Request.Messages[0].Content)
	if strings.Contains(content, "{{EMAIL_1}}") {
		t.Fatalf("expected anonymizer rules to be disabled: %s", content)
	}
}

func TestDeanonymizeResponseRestoresOriginalValues(t *testing.T) {
	module := NewAnonymizerModule(true, RuleEmail, RulePhone)
	req := RequestContext{
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{
				{Role: "user", Content: "write to user@example.com or call +7 999 123-45-67"},
			},
		},
	}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	response := openai.ChatCompletionResponse{
		Choices: []openai.Choice{
			{
				Message: openai.Message{
					Role:    "assistant",
					Content: "I will use {{EMAIL_1}} and {{PHONE_1}}.",
				},
			},
		},
	}

	DeanonymizeResponse(&req, &response)

	content := openai.ContentText(response.Choices[0].Message.Content)
	if !strings.Contains(content, "user@example.com") {
		t.Fatalf("expected email to be restored: %s", content)
	}
	if !strings.Contains(content, "+7 999 123-45-67") {
		t.Fatalf("expected phone to be restored: %s", content)
	}
}

func TestDeanonymizeResponsesResponseRestoresOriginalValues(t *testing.T) {
	module := NewAnonymizerModule(true, RuleEmail)
	req := RequestContext{
		ResponseRequest: &openai.ResponseRequest{
			Model: "demo",
			Input: "send to user@example.com",
		},
	}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	response := openai.ResponseResponse{
		OutputText: "Email: {{EMAIL_1}}",
		Output: []openai.ResponseOutputItem{
			{
				Type: "message",
				Content: []openai.ResponseOutputContent{
					{Type: "output_text", Text: "Email: {{EMAIL_1}}"},
					{Type: "refusal", Refusal: "Cannot send to {{EMAIL_1}}"},
				},
			},
		},
	}

	DeanonymizeResponsesResponse(&req, &response)
	if !strings.Contains(response.OutputText, "user@example.com") {
		t.Fatalf("expected output_text to be restored: %s", response.OutputText)
	}
	if !strings.Contains(response.Output[0].Content[0].Text, "user@example.com") {
		t.Fatalf("expected output content to be restored: %s", response.Output[0].Content[0].Text)
	}
	if response.Output[0].Content[1].Refusal != "Cannot send to user@example.com" {
		t.Fatalf("refusal was not restored: %s", response.Output[0].Content[1].Refusal)
	}

}

func TestAnonymizerProtectsChatRefusalHistoryAndResponse(t *testing.T) {
	refusal := "Cannot send to user@example.com"
	module := NewAnonymizerModule(true, RuleEmail)
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "assistant", Refusal: &refusal}}}}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if req.Request.Messages[0].Refusal == nil || !strings.Contains(*req.Request.Messages[0].Refusal, "{{EMAIL_1}}") {
		t.Fatalf("refusal history was not anonymized: %+v", req.Request.Messages[0].Refusal)
	}
	masked := "Cannot send to {{EMAIL_1}}"
	response := openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{Refusal: &masked, Audio: &openai.ChatAudio{Transcript: &masked}}}}}
	DeanonymizeResponse(&req, &response)
	if response.Choices[0].Message.Refusal == nil || *response.Choices[0].Message.Refusal != refusal || response.Choices[0].Message.Audio.Transcript == nil || *response.Choices[0].Message.Audio.Transcript != refusal {
		t.Fatalf("refusal response was not restored: %+v", response.Choices[0].Message.Refusal)
	}
}

func TestAnonymizerProtectsToolArgumentsAndRestoresToolResponse(t *testing.T) {
	module := NewAnonymizerModule(true, RuleEmail)
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{
		Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call-1", Type: "function", Function: openai.FunctionCall{Name: "send", Arguments: `{"email":"user@example.com"}`}}},
	}}}}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	masked := req.Request.Messages[0].ToolCalls[0].Function.Arguments
	if !strings.Contains(masked, "{{EMAIL_1}}") {
		t.Fatalf("tool arguments were not anonymized: %s", masked)
	}
	response := openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{ToolCalls: []openai.ToolCall{{Function: openai.FunctionCall{Arguments: `{"email":"{{EMAIL_1}}"}`}}}}}}}
	DeanonymizeResponse(&req, &response)
	if !strings.Contains(response.Choices[0].Message.ToolCalls[0].Function.Arguments, "user@example.com") {
		t.Fatalf("tool arguments were not restored: %+v", response)
	}
}
