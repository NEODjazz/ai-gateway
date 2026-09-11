package openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func stringPointer(value string) *string { return &value }

func TestBedrockConverseMapsToolHistoryAndConfiguration(t *testing.T) {
	maxTokens := 32
	request := BedrockConverseRequest{
		System: []BedrockContentBlock{{Text: stringPointer("be concise")}},
		Messages: []BedrockMessage{
			{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("weather")}}},
			{Role: "assistant", Content: []BedrockContentBlock{{ToolUse: &BedrockToolUse{ID: "call_1", Name: "weather", Input: map[string]any{"city": "Paris"}}}}},
			{Role: "user", Content: []BedrockContentBlock{{ToolResult: &BedrockToolResult{ID: "call_1", Content: []BedrockContentBlock{{Text: stringPointer("sunny")}}}}}},
		},
		InferenceConfig: BedrockInferenceConfig{MaxTokens: &maxTokens},
		ToolConfig:      &BedrockToolConfig{Tools: []BedrockTool{{Spec: BedrockToolSpec{Name: "weather", InputSchema: BedrockToolInputSchema{JSON: map[string]any{"type": "object"}}}}}},
		ServiceTier:     &BedrockServiceTier{Type: "priority"},
	}
	chat, err := request.ChatRequest("public", "deployment")
	if err != nil || chat.Model != "public" || chat.Provider != "deployment" || chat.ServiceTier != "priority" || chat.MaxCompletionTokens == nil || *chat.MaxCompletionTokens != 32 || len(chat.Messages) != 4 || chat.Messages[2].ToolCalls[0].Function.Arguments != `{"city":"Paris"}` || chat.Messages[3].Role != "tool" || len(chat.Tools) != 1 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
}

func TestBedrockConverseMapsToolChoice(t *testing.T) {
	tool := BedrockTool{Spec: BedrockToolSpec{Name: "weather", InputSchema: BedrockToolInputSchema{JSON: map[string]any{"type": "object"}}}}
	for name, choice := range map[string]*BedrockToolChoice{
		"auto":  {Auto: &struct{}{}},
		"any":   {Any: &struct{}{}},
		"named": {Tool: &BedrockSpecificToolChoice{Name: "weather"}},
	} {
		t.Run(name, func(t *testing.T) {
			request := BedrockConverseRequest{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("weather")}}}}, ToolConfig: &BedrockToolConfig{Tools: []BedrockTool{tool}, ToolChoice: choice}}
			chat, err := request.ChatRequest("model", "")
			if err != nil || chat.ToolChoice == nil {
				t.Fatalf("chat=%+v err=%v", chat, err)
			}
		})
	}
	invalid := []*BedrockToolChoice{{}, {Auto: &struct{}{}, Any: &struct{}{}}, {Tool: &BedrockSpecificToolChoice{Name: "missing"}}}
	for _, choice := range invalid {
		request := BedrockConverseRequest{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("weather")}}}}, ToolConfig: &BedrockToolConfig{Tools: []BedrockTool{tool}, ToolChoice: choice}}
		if _, err := request.ChatRequest("model", ""); err == nil {
			t.Fatalf("invalid tool choice accepted: %+v", choice)
		}
	}
	request := BedrockConverseRequest{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("weather")}}}}, ToolConfig: &BedrockToolConfig{Tools: []BedrockTool{{Spec: BedrockToolSpec{Name: "bad name", InputSchema: BedrockToolInputSchema{JSON: map[string]any{}}}}}}}
	if _, err := request.ChatRequest("model", ""); err == nil {
		t.Fatal("invalid Bedrock tool name accepted")
	}
}

func TestBedrockConverseMapsReservedTierAndPerformance(t *testing.T) {
	request := BedrockConverseRequest{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("hello")}}}}, ServiceTier: &BedrockServiceTier{Type: "reserved"}, PerformanceConfig: &BedrockPerformanceConfig{Latency: "optimized"}}
	chat, err := request.ChatRequest("model", "")
	if err != nil || chat.BedrockServiceTier != "reserved" || chat.BedrockPerformanceLatency != "optimized" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	request.ServiceTier.Type = "burst"
	if _, err := request.ChatRequest("model", ""); err == nil {
		t.Fatal("unknown service tier accepted")
	}
	request.ServiceTier.Type = "reserved"
	request.PerformanceConfig.Latency = "fastest"
	if _, err := request.ChatRequest("model", ""); err == nil {
		t.Fatal("unknown performance latency accepted")
	}
}

func TestBedrockConverseValidatesAdditionalResponseFieldPaths(t *testing.T) {
	request := BedrockConverseRequest{
		Messages:                          []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("hello")}}}},
		AdditionalModelResponseFieldPaths: []string{"/stop_sequence", "/nested/a~1b/~0value"},
	}
	chat, err := request.ChatRequest("model", "")
	if err != nil || len(chat.BedrockAdditionalModelResponseFieldPaths) != 2 || chat.BedrockAdditionalModelResponseFieldPaths[1] != "/nested/a~1b/~0value" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	invalid := [][]string{{""}, {"stop_sequence"}, {"/bad~2escape"}, {"/same", "/same"}, {"/" + strings.Repeat("x", 256)}, make([]string, 11)}
	for _, paths := range invalid {
		request.AdditionalModelResponseFieldPaths = paths
		if _, err := request.ChatRequest("model", ""); err == nil {
			t.Fatalf("invalid response paths accepted: %#v", paths)
		}
	}
}

func TestBedrockConverseValidatesAndCopiesRequestMetadata(t *testing.T) {
	request := BedrockConverseRequest{
		Messages:        []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("hello")}}}},
		RequestMetadata: map[string]string{"tenant:id": "customer-42", "empty": ""},
	}
	chat, err := request.ChatRequest("model", "")
	if err != nil || chat.BedrockRequestMetadata["tenant:id"] != "customer-42" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	request.RequestMetadata["tenant:id"] = "changed"
	if chat.BedrockRequestMetadata["tenant:id"] != "customer-42" {
		t.Fatal("request metadata was not copied")
	}

	tooMany := make(map[string]string, 17)
	for i := 0; i < 17; i++ {
		tooMany[string(rune('a'+i))] = "value"
	}
	for _, metadata := range []map[string]string{
		tooMany,
		{"": "value"},
		{strings.Repeat("a", 257): "value"},
		{"key": strings.Repeat("a", 257)},
		{"bad!key": "value"},
		{"key": "snowman ☃"},
	} {
		request.RequestMetadata = metadata
		if _, err := request.ChatRequest("model", ""); err == nil {
			t.Fatalf("invalid request metadata accepted: %#v", metadata)
		}
	}
}

func TestBedrockConverseValidatesAndCopiesAdditionalModelRequestFields(t *testing.T) {
	request := BedrockConverseRequest{
		Messages:                     []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("hello")}}}},
		AdditionalModelRequestFields: json.RawMessage(`{"top_k":42,"thinking":{"budget_tokens":128}}`),
	}
	chat, err := request.ChatRequest("model", "")
	if err != nil || string(chat.BedrockAdditionalModelRequestFields) != string(request.AdditionalModelRequestFields) || chat.NativeInputTokens <= 0 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	request.AdditionalModelRequestFields[2] = 'X'
	if string(chat.BedrockAdditionalModelRequestFields) != `{"top_k":42,"thinking":{"budget_tokens":128}}` {
		t.Fatal("additional model request fields were not copied")
	}
	for _, fields := range []json.RawMessage{
		json.RawMessage(`null`),
		json.RawMessage(" \nnull "),
		json.RawMessage(`{"broken":`),
		json.RawMessage(`"` + strings.Repeat("x", MaxBedrockAdditionalModelRequestFieldsBytes) + `"`),
	} {
		request.AdditionalModelRequestFields = fields
		if _, err := request.ChatRequest("model", ""); err == nil {
			t.Fatalf("invalid additional model request fields accepted: %d bytes", len(fields))
		}
	}
}

func TestBedrockConverseValidatesAndCopiesGuardrailConfig(t *testing.T) {
	request := BedrockConverseRequest{
		Messages:        []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("hello")}}}},
		GuardrailConfig: &BedrockGuardrailConfig{GuardrailIdentifier: "guardrail123", GuardrailVersion: "DRAFT", Trace: "enabled_full"},
	}
	chat, err := request.ChatRequest("model", "")
	if err != nil || chat.BedrockGuardrailConfig == nil || chat.BedrockGuardrailConfig.Trace != "enabled_full" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	request.GuardrailConfig.Trace = "disabled"
	if chat.BedrockGuardrailConfig.Trace != "enabled_full" {
		t.Fatal("guardrail config was not copied")
	}
	for _, config := range []*BedrockGuardrailConfig{
		{GuardrailVersion: "1"},
		{GuardrailIdentifier: "UPPER", GuardrailVersion: "1"},
		{GuardrailIdentifier: "guardrail123", GuardrailVersion: "0"},
		{GuardrailIdentifier: "guardrail123", GuardrailVersion: "1", Trace: "full"},
	} {
		request.GuardrailConfig = config
		if _, err := request.ChatRequest("model", ""); err == nil {
			t.Fatalf("invalid guardrail config accepted: %+v", config)
		}
	}
}

func TestBedrockConverseMapsStructuredOutput(t *testing.T) {
	request := BedrockConverseRequest{
		Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("extract")}}}},
		OutputConfig: &BedrockOutputConfig{TextFormat: BedrockOutputFormat{Type: "json_schema", Structure: BedrockOutputFormatStructure{JSONSchema: &BedrockJSONSchemaDefinition{
			Name: "answer", Description: "structured answer", Schema: `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`,
		}}}},
	}
	chat, err := request.ChatRequest("model", "")
	if err != nil || chat.ResponseFormat == nil || chat.ResponseFormat.JSONSchema == nil || chat.ResponseFormat.JSONSchema.Name != "answer" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	schema, ok := chat.ResponseFormat.JSONSchema.Schema.(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("schema=%#v", chat.ResponseFormat.JSONSchema.Schema)
	}

	invalid := []*BedrockOutputConfig{
		{TextFormat: BedrockOutputFormat{Type: "json_object", Structure: BedrockOutputFormatStructure{JSONSchema: request.OutputConfig.TextFormat.Structure.JSONSchema}}},
		{TextFormat: BedrockOutputFormat{Type: "json_schema"}},
		{TextFormat: BedrockOutputFormat{Type: "json_schema", Structure: BedrockOutputFormatStructure{JSONSchema: &BedrockJSONSchemaDefinition{Schema: `[]`}}}},
		{TextFormat: BedrockOutputFormat{Type: "json_schema", Structure: BedrockOutputFormatStructure{JSONSchema: &BedrockJSONSchemaDefinition{Schema: `{`}}}},
	}
	for _, output := range invalid {
		request.OutputConfig = output
		if _, err := request.ChatRequest("model", ""); err == nil {
			t.Fatalf("invalid output config accepted: %+v", output)
		}
	}
}

func TestBedrockConverseRejectsAmbiguousContent(t *testing.T) {
	request := BedrockConverseRequest{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("hello"), ToolUse: &BedrockToolUse{ID: "call", Name: "tool", Input: map[string]any{}}}}}}}
	if _, err := request.ChatRequest("model", ""); err == nil {
		t.Fatal("ambiguous content accepted")
	}
}

func TestBedrockConverseMapsBoundedUserImages(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nimage"))
	request := BedrockConverseRequest{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{
		{Text: stringPointer("describe")},
		{Image: &BedrockImage{Format: "png", Source: BedrockImageSource{Bytes: data}}},
	}}}}
	chat, err := request.ChatRequest("model", "bedrock")
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := ChatImageAttachments(chat.Messages)
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "image/png" || len(chat.Messages[0].Content.([]any)) != 2 {
		t.Fatalf("chat=%+v attachments=%+v err=%v", chat, attachments, err)
	}
	request.Messages[0].Role = "assistant"
	if _, err := request.ChatRequest("model", "bedrock"); err == nil {
		t.Fatal("assistant image accepted")
	}
	request.Messages[0].Role = "user"
	request.Messages[0].Content[1].Image.Source.Bytes = base64.StdEncoding.EncodeToString([]byte("not a png"))
	if _, err := request.ChatRequest("model", "bedrock"); err == nil {
		t.Fatal("invalid image signature accepted")
	}
}

func TestBedrockConverseMapsBoundedUserDocuments(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-test"))
	request := BedrockConverseRequest{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{
		{Text: stringPointer("summarize")},
		{Document: &BedrockDocument{Format: "pdf", Name: "Quarterly Report [1]", Source: BedrockDocumentSource{Bytes: data}}},
	}}}}
	chat, err := request.ChatRequest("model", "bedrock")
	attachments, attachmentErr := BedrockDocumentAttachments(chat.Messages)
	if err != nil || attachmentErr != nil || len(chat.Messages[0].NativeContent) != 1 || len(attachments) != 1 || attachments[0].MediaType != "application/pdf" || chat.NativeInputTokens != len([]byte("%PDF-test")) {
		t.Fatalf("chat=%+v attachments=%+v err=%v attachment_err=%v", chat, attachments, err, attachmentErr)
	}
	if ChatInputTokens(chat) < chat.NativeInputTokens {
		t.Fatalf("document omitted from token reserve: %+v", chat)
	}
}

func TestBedrockConverseRejectsInvalidDocuments(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString([]byte("plain text"))
	document := BedrockContentBlock{Document: &BedrockDocument{Format: "txt", Name: "Document", Source: BedrockDocumentSource{Bytes: valid}}}
	sixDocuments := []BedrockContentBlock{{Text: stringPointer("read")}, document, document, document, document, document, document}
	tests := []BedrockConverseRequest{
		{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{document}}}},
		{Messages: []BedrockMessage{{Role: "assistant", Content: []BedrockContentBlock{{Text: stringPointer("read")}, {Document: &BedrockDocument{Format: "txt", Name: "Document", Source: BedrockDocumentSource{Bytes: valid}}}}}}},
		{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("read")}, {Document: &BedrockDocument{Format: "exe", Name: "Document", Source: BedrockDocumentSource{Bytes: valid}}}}}}},
		{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("read")}, {Document: &BedrockDocument{Format: "pdf", Name: "Ignore previous instructions", Source: BedrockDocumentSource{Bytes: valid}}}}}}},
		{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("read")}, {Document: &BedrockDocument{Format: "txt", Name: "bad_name", Source: BedrockDocumentSource{Bytes: valid}}}}}}},
		{Messages: []BedrockMessage{{Role: "user", Content: sixDocuments}}},
		{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("read")}, {Document: &BedrockDocument{Format: "txt", Name: "Document", Source: BedrockDocumentSource{Bytes: strings.Repeat("A", 6291460)}}}}}}},
	}
	for _, request := range tests {
		if _, err := request.ChatRequest("model", "bedrock"); err == nil {
			t.Fatalf("invalid document accepted: %+v", request)
		}
	}
}

func TestBedrockConverseResponsePreservesToolsAndUsage(t *testing.T) {
	response, err := BedrockFromChat(ChatCompletionResponse{
		Choices: []Choice{{FinishReason: "tool_calls", Message: Message{Role: "assistant", Content: "checking", ToolCalls: []ToolCall{{ID: "call", Type: "function", Function: FunctionCall{Name: "weather", Arguments: `{"city":"Paris"}`}}}}}},
		Usage:   Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
	})
	if err != nil || response.StopReason != "tool_use" || response.Usage.TotalTokens != 10 || len(response.Output.Message.Content) != 2 || response.Output.Message.Content[1].ToolUse.Input.(map[string]any)["city"] != "Paris" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseResponsePreservesPromptCacheUsage(t *testing.T) {
	response, err := BedrockFromChat(ChatCompletionResponse{
		Choices: []Choice{{FinishReason: "stop", Message: Message{Role: "assistant", Content: "cached"}}},
		Usage: Usage{
			PromptTokens: 20, CompletionTokens: 2, TotalTokens: 22,
			PromptTokensDetails: &PromptTokenDetails{CachedTokens: 6, CacheWriteTokens: 10},
		},
	})
	if err != nil || response.Usage.InputTokens != 4 || response.Usage.CacheReadInputTokens != 6 || response.Usage.CacheWriteInputTokens != 10 || response.Usage.OutputTokens != 2 || response.Usage.TotalTokens != 22 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseResponseRejectsCacheUsageAbovePromptTotal(t *testing.T) {
	_, err := BedrockFromChat(ChatCompletionResponse{
		Choices: []Choice{{FinishReason: "stop", Message: Message{Role: "assistant", Content: "cached"}}},
		Usage: Usage{
			PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7,
			PromptTokensDetails: &PromptTokenDetails{CachedTokens: 4, CacheWriteTokens: 2},
		},
	})
	if err == nil {
		t.Fatal("cache usage above prompt total accepted")
	}
}

func TestBedrockConverseResponseRejectsUnknownFinishReason(t *testing.T) {
	_, err := BedrockFromChat(ChatCompletionResponse{Choices: []Choice{{FinishReason: "unknown", Message: Message{Role: "assistant", Content: "hello"}}}})
	if err == nil {
		t.Fatal("unknown finish reason accepted")
	}
}

func TestBedrockConversePreservesSignedAndRedactedReasoning(t *testing.T) {
	text := "answer"
	request := BedrockConverseRequest{Messages: []BedrockMessage{
		{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("question")}}},
		{Role: "assistant", Content: []BedrockContentBlock{
			{ReasoningContent: &BedrockReasoningContent{ReasoningText: &BedrockReasoningText{Text: "private plan", Signature: "signed"}}},
			{Text: &text},
			{ReasoningContent: &BedrockReasoningContent{RedactedContent: "b3BhcXVl"}},
		}},
	}}
	chat, err := request.ChatRequest("model", "bedrock")
	if err != nil || len(chat.Messages) != 2 || len(chat.Messages[1].Reasoning) != 2 || *chat.Messages[1].Reasoning[0].Index != 0 || *chat.Messages[1].Reasoning[1].Index != 2 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	response, err := BedrockFromChat(ChatCompletionResponse{
		Choices: []Choice{{Message: chat.Messages[1], FinishReason: "stop"}},
		Usage:   Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	})
	if err != nil || len(response.Output.Message.Content) != 3 || response.Output.Message.Content[0].ReasoningContent.ReasoningText.Signature != "signed" || response.Output.Message.Content[1].Text == nil || response.Output.Message.Content[2].ReasoningContent.RedactedContent != "b3BhcXVl" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestBedrockConverseRejectsInvalidReasoning(t *testing.T) {
	validText := &BedrockReasoningText{Text: "plan", Signature: "signed"}
	for _, message := range []BedrockMessage{
		{Role: "user", Content: []BedrockContentBlock{{ReasoningContent: &BedrockReasoningContent{ReasoningText: validText}}}},
		{Role: "assistant", Content: []BedrockContentBlock{{ReasoningContent: &BedrockReasoningContent{}}}},
		{Role: "assistant", Content: []BedrockContentBlock{{ReasoningContent: &BedrockReasoningContent{ReasoningText: validText, RedactedContent: "b3BhcXVl"}}}},
		{Role: "assistant", Content: []BedrockContentBlock{{ReasoningContent: &BedrockReasoningContent{RedactedContent: "%%%"}}}},
	} {
		request := BedrockConverseRequest{Messages: []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("question")}}}, message}}
		if _, err := request.ChatRequest("model", "bedrock"); err == nil {
			t.Fatalf("invalid reasoning accepted: %+v", message)
		}
	}
}

func TestBedrockConverseAcceptsUnsignedReasoningText(t *testing.T) {
	request := BedrockConverseRequest{Messages: []BedrockMessage{
		{Role: "user", Content: []BedrockContentBlock{{Text: stringPointer("question")}}},
		{Role: "assistant", Content: []BedrockContentBlock{{ReasoningContent: &BedrockReasoningContent{ReasoningText: &BedrockReasoningText{Text: "plan"}}}}},
	}}
	chat, err := request.ChatRequest("model", "bedrock")
	if err != nil || len(chat.Messages[1].Reasoning) != 1 || chat.Messages[1].Reasoning[0].Signature != "" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
}
