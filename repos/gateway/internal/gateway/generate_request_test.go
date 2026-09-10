package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/provider"
)

func TestGenerateRequestConvertsNativeContextAndConfig(t *testing.T) {
	var native generateRequest
	raw := `{"systemInstruction":{"parts":[{"text":"Be concise"}]},"contents":[{"role":"user","parts":[{"text":"Describe"},{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]}],"tools":[{"functionDeclarations":[{"name":"weather","parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"}},"required":["city"]}}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["weather"]}},"generationConfig":{"maxOutputTokens":50,"temperature":0.2,"topP":0.9,"seed":7,"stopSequences":[" END "],"candidateCount":1,"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"answer":{"type":"STRING"}}}}}`
	if err := decodeMessagesValue(json.RawMessage(raw), &native); err != nil {
		t.Fatal(err)
	}
	chat, err := native.chat("public-model", true)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Model != "public-model" || !chat.Stream || *chat.MaxCompletionTokens != 50 || *chat.Seed != 7 || len(chat.Messages) != 2 || chat.Messages[0].Role != "system" {
		t.Fatalf("context/config: %+v", chat)
	}
	schema := chat.Tools[0].Function.Parameters.(map[string]any)
	if schema["type"] != "object" || schema["properties"].(map[string]any)["city"].(map[string]any)["type"] != "string" || chat.ResponseFormat.JSONSchema.Schema.(map[string]any)["type"] != "object" {
		t.Fatal("native schema types not normalized")
	}
	parts := chat.Messages[1].Content.([]any)
	if len(parts) != 2 || parts[1].(map[string]any)["type"] != "image_url" || chat.Stop.([]string)[0] != " END " {
		t.Fatal("parts or stop delimiter lost")
	}
}
func TestGenerateFunctionHistoryRoundTrip(t *testing.T) {
	var native generateRequest
	raw := `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"weather","args":{"city":"Paris"}},"thoughtSignature":"opaque"}]},{"role":"user","parts":[{"functionResponse":{"name":"weather","response":{"temperature":18}}}]}]}`
	if err := decodeMessagesValue(json.RawMessage(raw), &native); err != nil {
		t.Fatal(err)
	}
	chat, err := native.chat("gemini-test", false)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Messages[0].ToolCalls[0].ID != chat.Messages[1].ToolCallID || chat.Messages[0].ToolCalls[0].ExtraContent.Google.ThoughtSignature != "opaque" {
		t.Fatal("function reference or signature lost")
	}
	repeated, err := native.chat("gemini-test", false)
	if err != nil || repeated.Messages[0].ToolCalls[0].ID != chat.Messages[0].ToolCalls[0].ID {
		t.Fatal("implicit IDs are not deterministic")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Contents []struct {
				Parts []struct {
					Signature string `json:"thoughtSignature"`
					Result    *struct {
						Response map[string]any `json:"response"`
					} `json:"functionResponse"`
				} `json:"parts"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Contents[0].Parts[0].Signature != "opaque" || body.Contents[1].Parts[0].Result.Response["temperature"] != float64(18) || body.Contents[1].Parts[0].Result.Response["result"] != nil {
			t.Errorf("native function response changed: %+v", body)
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()
	if _, err := provider.NewGemini(server.URL, "", false).ChatCompletions(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateThoughtHistoryRoundTrip(t *testing.T) {
	var native generateRequest
	raw := `{"contents":[{"role":"model","parts":[{"text":"private plan","thought":true,"thoughtSignature":"c2lnbmVk"},{"text":"answer"}]}]}`
	if err := decodeMessagesValue(json.RawMessage(raw), &native); err != nil {
		t.Fatal(err)
	}
	chat, err := native.chat("model", false)
	if err != nil || len(chat.Messages) != 1 || len(chat.Messages[0].Reasoning) != 1 || chat.Messages[0].Reasoning[0].Thinking != "private plan" || chat.Messages[0].Reasoning[0].Signature != "c2lnbmVk" {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	parts, err := generateParts(chat.Messages[0])
	if err != nil || len(parts) != 2 || parts[0].(map[string]any)["thought"] != true || parts[1].(map[string]any)["text"] != "answer" {
		t.Fatalf("parts=%+v err=%v", parts, err)
	}
}
func TestGenerateRequestRejectsUnsupportedFieldsAndUnions(t *testing.T) {
	for _, raw := range []string{
		`{"contents":[{"parts":[{"text":"hi"}]}],"safetySettings":[]}`,
		`{"contents":[{"parts":[{"text":"hi","inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]}]}`,
		`{"contents":[{"parts":[{"text":"hi","thoughtSignature":"signature"}]}]}`,
		`{"contents":[{"parts":[{"text":"private","thought":true}]}]}`,
		`{"contents":[{"role":"model","parts":[{"text":"private","thought":true,"thoughtSignature":"%%%"}]}]}`,
		`{"contents":[{"parts":[{"functionResponse":{"name":"missing","response":{}}}]}]}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"f","parameters":{"type":"OBJECT","propertyOrdering":["a"]}}]}]}`,
		`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}},{"functionCall":{"name":"f","args":{}}}]},{"parts":[{"functionResponse":{"name":"f","response":{}}}]}]}`,
	} {
		var request generateRequest
		err := decodeMessagesValue(json.RawMessage(raw), &request)
		if err == nil {
			_, err = request.chat("model", false)
		}
		if err == nil {
			t.Fatalf("unsupported input accepted: %s", raw)
		}
	}
	var request generateRequest
	if err := decodeMessagesValue(json.RawMessage(`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"topK":12}}`), &request); err == nil || !strings.Contains(err.Error(), "topK") {
		t.Fatal("unknown generation parameter ignored")
	}
}

func TestGenerateHistoryWithoutFunctionArguments(t *testing.T) {
	for _, args := range []string{"", `,"args":null`, `,"args":{}`} {
		var native generateRequest
		raw := `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"clock"` + args + `},"thoughtSignature":"opaque"}]},{"parts":[{"functionResponse":{"name":"clock","response":{"time":"12:00"}}}]}]}`
		if err := decodeMessagesValue(json.RawMessage(raw), &native); err != nil {
			t.Fatal(err)
		}
		request, err := native.chat("m", false)
		if err != nil {
			t.Fatal(err)
		}
		call := request.Messages[0].ToolCalls[0]
		if call.Function.Arguments != "{}" || call.ID != request.Messages[1].ToolCallID || call.ExtraContent.Google.ThoughtSignature != "opaque" {
			t.Fatalf("history changed: %+v", request)
		}
	}
	for _, args := range []string{`[]`, `1`, `"text"`} {
		var native generateRequest
		err := decodeMessagesValue(json.RawMessage(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"clock","args":`+args+`}}]}]}`), &native)
		if err == nil {
			_, err = native.chat("m", false)
		}
		if err == nil {
			t.Fatalf("non-object args accepted: %s", args)
		}
	}
}
