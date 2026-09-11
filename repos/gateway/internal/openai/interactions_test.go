package openai

import (
	"encoding/json"
	"testing"
)

func TestInteractionRequestMapsSupportedResponseSemantics(t *testing.T) {
	maxTokens := 42
	temperature := 0.5
	topP := 0.8
	store := false
	request := InteractionRequest{
		Provider: "deployment", Model: "model", Input: "hello", SystemInstruction: "be concise",
		Tools:                 []ResponseTool{{Type: "function", Name: "weather", Parameters: map[string]any{"type": "object"}}},
		ResponseFormat:        map[string]any{"type": "json_schema", "name": "answer", "schema": map[string]any{"type": "object"}},
		PreviousInteractionID: "resp_previous", Store: &store, Stream: true,
		GenerationConfig: InteractionGenerationConfig{MaxOutputTokens: &maxTokens, Temperature: &temperature, TopP: &topP},
	}
	response, message := request.ResponseRequest()
	if message != "" || response.Provider != "deployment" || response.Model != "model" || response.Input != "hello" || response.Instructions != "be concise" || response.PreviousResponse != "resp_previous" || response.Store == nil || *response.Store || !response.Stream || response.MaxOutputTokens == nil || *response.MaxOutputTokens != 42 || response.Temperature == nil || *response.Temperature != 0.5 || response.TopP == nil || *response.TopP != 0.8 || len(response.Tools) != 1 {
		t.Fatalf("response=%+v message=%q", response, message)
	}
	text, ok := response.Text.(map[string]any)
	if !ok || text["format"] == nil {
		t.Fatalf("response format lost: %#v", response.Text)
	}
}

func TestInteractionRequestMapsDurableBackgroundSemantics(t *testing.T) {
	response, message := (InteractionRequest{Model: "model", Input: "hello", Background: true}).ResponseRequest()
	if message != "" || !response.Background || response.Store == nil || !*response.Store {
		t.Fatalf("response=%+v message=%q", response, message)
	}
	store := false
	if _, message := (InteractionRequest{Model: "model", Input: "hello", Background: true, Store: &store}).ResponseRequest(); message != "background requires store=true" {
		t.Fatalf("explicit store=false message=%q", message)
	}
}

func TestInteractionRequestRejectsUnsupportedOrInvalidSemantics(t *testing.T) {
	seed := int64(1)
	zero := 0
	tests := []InteractionRequest{
		{Agent: "research", Input: "hello"},
		{Model: "model"},
		{Model: "model", Input: "hello", Stream: true, Background: true},
		{Model: "model", Input: "hello", GenerationConfig: InteractionGenerationConfig{Seed: &seed}},
		{Model: "model", Input: "hello", GenerationConfig: InteractionGenerationConfig{StopSequences: []string{"stop"}}},
		{Model: "model", Input: "hello", GenerationConfig: InteractionGenerationConfig{ThinkingLevel: "high"}},
		{Model: "model", Input: "hello", Tools: []ResponseTool{{Type: "mcp", ServerLabel: "server", ServerURL: "https://example.test"}}},
		{Model: "model", Input: "hello", GenerationConfig: InteractionGenerationConfig{MaxOutputTokens: &zero}},
	}
	for _, request := range tests {
		if _, message := request.ResponseRequest(); message == "" {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
}

func TestInteractionResponsePreservesStepsAndUsage(t *testing.T) {
	response := InteractionFromResponse(ResponseResponse{
		ID: "resp_1", Model: "model", Status: "completed", CreatedAt: 1,
		Output: []ResponseOutputItem{
			{ID: "reason", Type: "reasoning", Summary: []ResponseOutputContent{{Type: "summary_text", Text: "summary"}}},
			{ID: "call", CallID: "call_1", Type: "function_call", Name: "weather", Arguments: `{"city":"Paris"}`},
			{ID: "message", Type: "message", Content: []ResponseOutputContent{{Type: "output_text", Text: "sunny"}}},
		},
		Usage:             ResponseUsage{InputTokens: 7, OutputTokens: 5, TotalTokens: 12, InputTokensDetails: &InputTokenDetails{CachedTokens: 2}, OutputTokensDetails: &CompletionTokenDetails{ReasoningTokens: 3}},
		IncompleteDetails: &ResponseIncompleteDetails{Reason: "max_output_tokens"},
	})
	if response.Object != "interaction" || response.Created != "1970-01-01T00:00:01Z" || len(response.Steps) != 3 || response.Steps[1].ID != "call_1" || response.Steps[2].Content[0].Text != "sunny" || response.Usage.TotalCachedTokens != 2 || response.Usage.TotalThoughtTokens != 3 || response.Usage.TotalTokens != 12 || response.IncompleteDetails == nil || response.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("interaction=%+v", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil || string(encoded) == "" {
		t.Fatal(err)
	}
}
