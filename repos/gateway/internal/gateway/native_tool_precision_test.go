package gateway

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestMessagesPreservesNativeToolArgumentNumbers(t *testing.T) {
	const arguments = `{"id":9007199254740993,"amount":0.1234567890123456789012345,"negative":-9007199254740993}`
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			message := openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "next", Type: "function", Function: openai.FunctionCall{Name: "query", Arguments: arguments}}}}
			if native {
				message.NativeContent = []json.RawMessage{json.RawMessage(`{"type":"tool_use","id":"next","name":"query","input":` + arguments + `}`)}
			}
			upstream := &fallbackChatProvider{response: openai.ChatCompletionResponse{ID: "msg", Model: "model", Choices: []openai.Choice{{Message: message, FinishReason: "tool_calls"}}, Usage: openai.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}}}
			handler := Routes(NewHandler(modules.NewPipeline(nil), upstream))
			body := `{"model":"model","max_tokens":32,"tools":[{"name":"query","input_schema":{"type":"object"}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"previous","name":"query","input":` + arguments + `}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"previous","content":"Reviewed"}]}]}`
			response := nativeMessageCall(handler, body, "")
			if response.Code != 200 {
				t.Fatalf("Messages response: %d %s", response.Code, response.Body.String())
			}
			for _, literal := range []string{"9007199254740993", "0.1234567890123456789012345", "-9007199254740993"} {
				if !strings.Contains(response.Body.String(), literal) || !strings.Contains(upstream.request.Request.Messages[0].ToolCalls[0].Function.Arguments, literal) {
					t.Fatalf("native argument rounded: response=%s request=%s", response.Body.String(), upstream.request.Request.Messages[0].ToolCalls[0].Function.Arguments)
				}
			}
			stream := httptest.NewRecorder()
			writer := &messagesWriter{destination: stream, headers: stream.Header(), status: 200, tools: map[int]int{}}
			if err := writer.streamResult(upstream.response); err != nil {
				t.Fatal(err)
			}
			for _, literal := range []string{"9007199254740993", "0.1234567890123456789012345", "-9007199254740993"} {
				if !strings.Contains(stream.Body.String(), literal) {
					t.Fatalf("stream argument rounded: %s", stream.Body.String())
				}
			}
		})
	}
	for _, invalid := range []string{"null", "[]", "{} {}", "{broken"} {
		if _, err := messagesContent(openai.Message{ToolCalls: []openai.ToolCall{{ID: "call", Type: "function", Function: openai.FunctionCall{Name: "query", Arguments: invalid}}}}); err == nil {
			t.Fatalf("invalid argument object accepted: %s", invalid)
		}
	}
}
