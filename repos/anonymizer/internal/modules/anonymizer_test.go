package modules

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"ai-gateway-anonymizer/internal/openai"
)

func TestAnonymizerLeavesImagePayloadUntouched(t *testing.T) {
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
		t.Fatalf("image payload changed: %q", got)
	}
}

func TestAnonymizerUsesConfiguredRule(t *testing.T) {
	module, err := NewAnonymizerModuleFromConfig(true, []RuleConfig{
		{Name: "ticket", Placeholder: "{{TICKET}}", Pattern: `TKT-\d{4}`},
	}, "all")
	if err != nil {
		t.Fatal(err)
	}
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{
		{Role: "user", Content: "check TKT-1234"},
	}}}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if content := openai.ContentText(req.Request.Messages[0].Content); content != "check {{TICKET_1}}" {
		t.Fatalf("unexpected configured rule result: %s", content)
	}
}

func TestAnonymizerRejectsInvalidConfiguredRule(t *testing.T) {
	_, err := NewAnonymizerModuleFromConfig(true, []RuleConfig{
		{Name: "broken", Placeholder: "{{BROKEN}}", Pattern: `(`},
	}, "all")
	if err == nil {
		t.Fatal("expected invalid pattern to be rejected")
	}
}

func TestAnonymizerRejectsMissingCaptureGroup(t *testing.T) {
	_, err := NewAnonymizerModuleFromConfig(true, []RuleConfig{
		{Name: "broken", Placeholder: "{{BROKEN}}", Pattern: `(value)`, CaptureGroup: 2},
	}, "all")
	if err == nil {
		t.Fatal("expected missing capture group to be rejected")
	}
}

func TestConfiguredRuleCanMaskCaptureGroupOnly(t *testing.T) {
	module, err := NewAnonymizerModuleFromConfig(true, []RuleConfig{
		{Name: "otp", Placeholder: "{{OTP}}", Pattern: `(?i)(?:otp|код)[: ]+(\d{6})`, CaptureGroup: 1},
	}, "all")
	if err != nil {
		t.Fatal(err)
	}
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{
		{Role: "user", Content: "OTP: 123456"},
	}}}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if content := openai.ContentText(req.Request.Messages[0].Content); content != "OTP: {{OTP_1}}" {
		t.Fatalf("expected the label to be preserved, got: %s", content)
	}
}

func TestConfiguredRuleCanExcludeValues(t *testing.T) {
	module, err := NewAnonymizerModuleFromConfig(true, []RuleConfig{
		{Name: "code_word", Placeholder: "{{CODE_WORD}}", Pattern: `(?i)кодовое\s+слово\s+(\S+)`, CaptureGroup: 1, ExcludeValues: []string{"подошло"}},
	}, "all")
	if err != nil {
		t.Fatal(err)
	}
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: "кодовое слово Ромашка; кодовое слово ПОДОШЛО"}}}}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if got := openai.ContentText(req.Request.Messages[0].Content); got != "кодовое слово {{CODE_WORD_1}} кодовое слово ПОДОШЛО" {
		t.Fatalf("unexpected anonymized content: %q", got)
	}
}

func TestConfiguredRuleRejectsEmptyExcludedValue(t *testing.T) {
	_, err := NewAnonymizerModuleFromConfig(true, []RuleConfig{
		{Name: "code_word", Placeholder: "{{CODE_WORD}}", Pattern: `(\S+)`, CaptureGroup: 1, ExcludeValues: []string{" "}},
	}, "all")
	if err == nil {
		t.Fatal("expected empty excluded value to be rejected")
	}
}

func TestConfiguredRuleUsesUnicodeWordBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		config   RuleConfig
		input    string
		expected string
	}{
		{
			name: "dialog code word",
			config: RuleConfig{Name: "dialog_code_word", Placeholder: "{{DIALOG_KW_MASK}}", CaptureGroup: 1,
				Pattern: `(?is)(?:назовите|скажите|ваше)?\s*(?:кодовое|секретное|ключевое)\s+слово[.?:\s]{0,4}(?:клиент:\s*)?(.{1,100}?)(?:\s+(?:\d{2}:\d{2}:\d{2}\s+оператор:\s+)?(?:кодовое\s+слово\s+)?(?:подошло|верно|подходит|принято)(?:$|[^\p{L}\p{N}_]))`},
			input:    "Назовите кодовое слово. Клиент: ромашка 12:34:56 Оператор: кодовое слово подошло.",
			expected: "Назовите кодовое слово. Клиент: {{DIALOG_KW_MASK_1}} 12:34:56 Оператор: кодовое слово подошло.",
		},
		{
			name: "password",
			config: RuleConfig{Name: "password", Placeholder: "{{PASSWORD}}", CaptureGroup: 1,
				Pattern: `(?i)(?:^|[^\p{L}\p{N}_])(?:password|passwd|pwd|пароль)\s*[:=]\s*["']?([^"'\s,;]{6,})["']?`},
			input:    "Пароль: secret123;",
			expected: "Пароль: {{PASSWORD_1}};",
		},
		{
			name: "single Cyrillic person",
			config: RuleConfig{Name: "person_context", Placeholder: "{{PERSON_NAME}}", CaptureGroup: 1,
				Pattern: `(?i)(?:^|[^\p{L}\p{N}_])(?:клиент|сотрудник|пользователь)\s+([А-ЯЁ][а-яё]+(?:\s+[А-ЯЁ][а-яё]+){0,2})(?:$|[^\p{L}\p{N}_])`},
			input:    "Клиент Иван.",
			expected: "Клиент {{PERSON_NAME_1}}.",
		},
		{
			name: "Cyrillic month date",
			config: RuleConfig{Name: "date", Placeholder: "{{DATE}}", CaptureGroup: 1,
				Pattern: `(?i)(?:^|[^\p{L}\p{N}_])((?:0[1-9]|[12][0-9]|3[01])[- ./](?:0[1-9]|1[0-2])[- ./]\d{2,4}|\d{4}[- ./](?:0[1-9]|1[0-2])[- ./](?:0[1-9]|[12][0-9]|3[01])|(?:0?[1-9]|[12][0-9]|3[01])\s+(?:янв(?:аря)?|февр(?:аля)?|мар(?:та)?|апр(?:еля)?|мая|июн(?:я)?|июл(?:я)?|авг(?:уста)?|сент(?:ября)?|окт(?:ября)?|нояб(?:ря)?|дек(?:абря)?)(?:\s+\d{2,4}(?:г\.?|\s+года?)?)?)(?:$|[^\p{L}\p{N}_])`},
			input:    "Дата 15 сентября.",
			expected: "Дата {{DATE_1}}.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module, err := NewAnonymizerModuleFromConfig(true, []RuleConfig{test.config}, "all")
			if err != nil {
				t.Fatal(err)
			}
			req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: test.input}}}}
			if err := module.Handle(context.Background(), &req); err != nil {
				t.Fatal(err)
			}
			if got := openai.ContentText(req.Request.Messages[0].Content); got != test.expected {
				t.Fatalf("content=%q want=%q", got, test.expected)
			}
		})
	}
}

func TestAnonymizerMasksRussianPasswordLabel(t *testing.T) {
	module := NewAnonymizerModule(true, RuleSecret)
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: "Пароль: secret123;"}}}}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if got := openai.ContentText(req.Request.Messages[0].Content); got != "Пароль: {{SECRET_1}};" {
		t.Fatalf("unexpected anonymized content: %q", got)
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

func TestAnonymizerMasksMultipartMessageContent(t *testing.T) {
	module := NewAnonymizerModule(true, RuleEmail)
	req := RequestContext{
		Request: openai.ChatCompletionRequest{
			Messages: []openai.Message{
				{
					Role: "user",
					Content: []any{
						map[string]any{"type": "text", "text": "send to user@example.com"},
						map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,abc"}},
					},
				},
			},
		},
	}

	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}

	content := openai.ContentText(req.Request.Messages[0].Content)
	if !strings.Contains(content, "{{EMAIL_1}}") {
		t.Fatalf("expected multipart email to be masked: %s", content)
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
}

func TestAnonymizerMasksToolCallArguments(t *testing.T) {
	module := NewAnonymizerModule(true, RuleEmail)
	req := RequestContext{Request: openai.ChatCompletionRequest{Messages: []openai.Message{{
		Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call-1", Type: "function", Function: openai.FunctionCall{Name: "send", Arguments: `{"email":"user@example.com"}`}}},
	}}}}
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	arguments := req.Request.Messages[0].ToolCalls[0].Function.Arguments
	if !strings.Contains(arguments, "{{EMAIL_1}}") {
		t.Fatalf("tool arguments were not anonymized: %s", arguments)
	}
}
