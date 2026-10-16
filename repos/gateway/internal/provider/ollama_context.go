package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"ai-gateway-gateway/internal/openai"
)

// Zero preserves the server default and the existing adapter contracts.
func validOllamaNumCtx(provider string, n int) bool {
	return n == 0 || provider == "ollama" && n >= 4096 && n <= 1048576
}

func (p Ollama) contextChatRequest(request openai.ChatCompletionRequest, messages []ollamaRequestMessage, tools []openai.Tool, stream bool) (ollamaChatRequest, error) {
	native := ollamaChatRequest{Model: request.Model, Messages: messages, Tools: tools, Format: ollamaResponseFormat(request.ResponseFormat), Options: ollamaRequestOptions(request), Stream: stream, Logprobs: request.Logprobs, TopLogprobs: request.TopLogprobs, Think: ollamaThink(request.ReasoningEffort)}
	if p.numCtx == 0 {
		return native, nil
	}
	native.Options.NumCtx = p.numCtx
	output := openai.ChatOutputReserve(request)
	if native.Options.NumPredict == nil || *native.Options.NumPredict <= 0 {
		native.Options.NumPredict = &output
	}
	disabled := false
	native.Truncate, native.Shift = &disabled, &disabled
	for _, message := range messages {
		if len(message.Images) > 0 {
			return native, &Error{Class: FailureClientRequest, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "unsupported_parameter", Param: "messages", Err: errors.New("num_ctx context budgeting currently supports text and converted documents only")}
		}
	}
	// A byte budget is deliberately conservative for byte-fallback tokenizers.
	// Unlike chars/4 it also bounds Russian, code and arbitrary tool arguments.
	// Reserve template/system overhead separately; native truncate=false and
	// shift=false remain authoritative for the server's actual model template.
	encoded, err := json.Marshal(struct {
		Messages []ollamaRequestMessage
		Tools    []openai.Tool
		Format   any
	}{messages, tools, native.Format})
	if err != nil {
		return native, err
	}
	overhead := 2048 + 128*len(messages) + 256*len(tools)
	if request.NativeInputTokens > 0 {
		if request.NativeInputTokens > p.numCtx {
			return native, &Error{Class: FailureContextLength, Provider: "ollama", StatusCode: 400, UpstreamCode: "context_length_exceeded", Param: "messages", Err: errors.New("native input exceeds deployment context")}
		}
		overhead += request.NativeInputTokens
	}
	if output > p.numCtx || overhead > p.numCtx-output || len(encoded) > p.numCtx-output-overhead {
		return native, &Error{Class: FailureContextLength, Provider: "ollama", StatusCode: http.StatusBadRequest, UpstreamCode: "context_length_exceeded", Param: "messages", Err: fmt.Errorf("input byte budget, template allowance and output reserve exceed deployment num_ctx=%d", p.numCtx)}
	}
	return native, nil
}

func (p Ollama) validateContextChat(request openai.ChatCompletionRequest) error {
	if p.numCtx == 0 {
		return nil
	}
	messages, err := ollamaMessages(request.Messages)
	if err != nil {
		return err
	}
	_, err = p.contextChatRequest(request, messages, ollamaChatTools(request), false)
	return err
}
