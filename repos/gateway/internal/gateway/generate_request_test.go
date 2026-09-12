package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func TestGenerateRequestConvertsNativeContextAndConfig(t *testing.T) {
	var native generateRequest
	raw := `{"systemInstruction":{"parts":[{"text":"Be concise"}]},"contents":[{"role":"user","parts":[{"text":"Describe"},{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]}],"safetySettings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_ONLY_HIGH"}],"tools":[{"functionDeclarations":[{"name":"weather","parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"}},"required":["city"]}}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["weather"]}},"generationConfig":{"maxOutputTokens":50,"temperature":0.2,"topP":0.9,"topK":12,"seed":7,"presencePenalty":0.3,"frequencyPenalty":-0.2,"responseLogprobs":true,"logprobs":5,"responseModalities":["TEXT"],"stopSequences":[" END "],"candidateCount":1,"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"answer":{"type":"STRING"}}}}}`
	if err := decodeMessagesValue(json.RawMessage(raw), &native); err != nil {
		t.Fatal(err)
	}
	chat, err := native.chat("public-model", true)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Model != "public-model" || !chat.Stream || *chat.MaxCompletionTokens != 50 || *chat.Seed != 7 || chat.TopK == nil || *chat.TopK != 12 || chat.PresencePenalty == nil || *chat.PresencePenalty != 0.3 || chat.FrequencyPenalty == nil || *chat.FrequencyPenalty != -0.2 || chat.Logprobs == nil || !*chat.Logprobs || chat.TopLogprobs == nil || *chat.TopLogprobs != 5 || chat.N != nil || chat.Modalities != nil || len(chat.Messages) != 2 || chat.Messages[0].Role != "system" || len(chat.GeminiSafetySettings) != 1 || chat.GeminiSafetySettings[0].Threshold != "BLOCK_ONLY_HIGH" || chat.NativeInputTokens == 0 {
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

func TestGenerateRequestMapsGoogleSearchTool(t *testing.T) {
	var native generateRequest
	if err := decodeMessagesValue(json.RawMessage(`{"contents":[{"parts":[{"text":"latest news"}]}],"tools":[{"googleSearch":{}}]}`), &native); err != nil {
		t.Fatal(err)
	}
	chat, err := native.chat("model", false)
	if err != nil || chat.WebSearchOptions == nil || len(chat.Tools) != 0 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	for _, raw := range []string{
		`{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"googleSearch":{}},{"googleSearch":{}}]}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"googleSearch":{},"functionDeclarations":[{"name":"f"}]}]}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"googleSearch":{}}],"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}}}`,
	} {
		var request generateRequest
		if err := decodeMessagesValue(json.RawMessage(raw), &request); err != nil {
			continue
		}
		if _, err := request.chat("model", false); err == nil {
			t.Fatalf("invalid Google Search tool accepted: %s", raw)
		}
	}
}

func TestGenerateRequestMapsCodeExecutionToolAndHistory(t *testing.T) {
	var native generateRequest
	raw := `{"contents":[{"role":"model","parts":[{"executableCode":{"id":"exec-1","language":"PYTHON","code":"print(4)"}},{"codeExecutionResult":{"id":"exec-1","outcome":"OUTCOME_OK","output":"4\n"}},{"text":"done"}]},{"role":"user","parts":[{"text":"continue"}]}],"tools":[{"codeExecution":{}}]}`
	if err := decodeMessagesValue(json.RawMessage(raw), &native); err != nil {
		t.Fatal(err)
	}
	chat, err := native.chat("model", false)
	if err != nil {
		t.Fatal(err)
	}
	if !chat.GeminiCodeExecution || len(chat.Messages) != 2 || len(chat.Messages[0].GeminiCodeExecutionParts) != 2 || chat.NativeInputTokens == 0 {
		t.Fatalf("code execution context lost: %+v", chat)
	}
	for _, raw := range []string{
		`{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"codeExecution":{},"googleSearch":{}}]}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"codeExecution":{}},{"codeExecution":{}}]}`,
		`{"contents":[{"role":"user","parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}}]}]}`,
		`{"contents":[{"role":"model","parts":[{"codeExecutionResult":{"outcome":"OUTCOME_UNSPECIFIED"}}]}]}`,
	} {
		var request generateRequest
		if err := decodeMessagesValue(json.RawMessage(raw), &request); err != nil {
			continue
		}
		if _, err := request.chat("model", false); err == nil {
			t.Fatalf("invalid code execution request accepted: %s", raw)
		}
	}
}

func TestGenerateRequestMapsInlineAudio(t *testing.T) {
	var native generateRequest
	if err := decodeMessagesValue(json.RawMessage(`{"contents":[{"role":"user","parts":[{"text":"transcribe"},{"inlineData":{"mimeType":"audio/wav","data":"UklGRgAAAABXQVZF"}}]}]}`), &native); err != nil {
		t.Fatal(err)
	}
	chat, err := native.chat("model", false)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := openai.ChatAudioAttachments(chat.Messages)
	if err != nil || len(attachments) != 1 || attachments[0].MediaType != "audio/wav" {
		t.Fatalf("chat=%+v attachments=%+v err=%v", chat, attachments, err)
	}
	native.Contents[0].Role = "model"
	if _, err := native.chat("model", false); err == nil {
		t.Fatal("model audio input accepted")
	}
}

func TestGenerateRequestMapsAdditionalInlineAudioFormats(t *testing.T) {
	tests := []struct {
		mediaType string
		data      []byte
	}{
		{"audio/flac", []byte("fLaCpayload")},
		{"audio/ogg", []byte("OggSpayload")},
		{"audio/opus", []byte("OggSpayload")},
		{"audio/aiff", []byte("FORM\x00\x00\x00\x00AIFFpayload")},
		{"audio/aac", []byte("\xff\xf1\x50\x80\x00\x1f\xfc")},
		{"audio/webm", []byte("\x1a\x45\xdf\xa3payload")},
		{"audio/m4a", []byte("\x00\x00\x00\x18ftypisom")},
	}
	for _, test := range tests {
		t.Run(test.mediaType, func(t *testing.T) {
			var native generateRequest
			raw := `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"` + test.mediaType + `","data":"` + base64.StdEncoding.EncodeToString(test.data) + `"}}]}]}`
			if err := decodeMessagesValue(json.RawMessage(raw), &native); err != nil {
				t.Fatal(err)
			}
			chat, err := native.chat("model", false)
			if err != nil {
				t.Fatal(err)
			}
			attachments, err := openai.ChatAudioAttachments(chat.Messages)
			if err != nil || len(attachments) != 1 || attachments[0].MediaType != test.mediaType {
				t.Fatalf("attachments=%+v err=%v", attachments, err)
			}
			if chat.NativeInputTokens == 0 && openai.ChatInputTokens(chat) == 0 {
				t.Fatal("audio omitted from token estimate")
			}
		})
	}
}

func TestGenerateRequestMapsInlinePDF(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\ncontent"))
	var native generateRequest
	raw := `{"contents":[{"role":"user","parts":[{"text":"summarize"},{"inlineData":{"mimeType":"application/pdf","data":"` + data + `"}}]}]}`
	if err := decodeMessagesValue(json.RawMessage(raw), &native); err != nil {
		t.Fatal(err)
	}
	chat, err := native.chat("model", false)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := openai.ChatFileAttachments(chat.Messages)
	if err != nil || len(attachments) != 1 || attachments[0].Data != data || attachments[0].Filename != "input.pdf" {
		t.Fatalf("chat=%+v attachments=%+v err=%v", chat, attachments, err)
	}
	if openai.ChatInputTokens(chat) <= openai.ChatInputTokens(openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: "summarize"}}}) {
		t.Fatal("inline PDF omitted from token reserve")
	}
	native.Contents[0].Role = "model"
	if _, err := native.chat("model", false); err == nil {
		t.Fatal("model PDF input accepted")
	}
}

func TestGenerateRequestRejectsMalformedInlinePDF(t *testing.T) {
	var native generateRequest
	if err := decodeMessagesValue(json.RawMessage(`{"contents":[{"parts":[{"inlineData":{"mimeType":"application/pdf","data":"bm90IGEgcGRm"}}]}]}`), &native); err != nil {
		t.Fatal(err)
	}
	if _, err := native.chat("model", false); err == nil {
		t.Fatal("malformed PDF accepted")
	}
}

func TestGenerateRequestMapsInlineVideo(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("\x00\x00\x00\x18ftypisom"))
	var native generateRequest
	raw := `{"contents":[{"role":"user","parts":[{"text":"describe"},{"inlineData":{"mimeType":"video/mp4","data":"` + data + `"}}]}]}`
	if err := decodeMessagesValue(json.RawMessage(raw), &native); err != nil {
		t.Fatal(err)
	}
	chat, err := native.chat("model", false)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := openai.ChatVideoAttachments(chat.Messages)
	if err != nil || len(attachments) != 1 || attachments[0].Data != data || attachments[0].MediaType != "video/mp4" {
		t.Fatalf("chat=%+v attachments=%+v err=%v", chat, attachments, err)
	}
	if openai.ChatInputTokens(chat) <= openai.ChatInputTokens(openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: "describe"}}}) {
		t.Fatal("inline video omitted from token reserve")
	}
	native.Contents[0].Role = "model"
	if _, err := native.chat("model", false); err == nil {
		t.Fatal("model video input accepted")
	}
}

func TestGenerateRequestRejectsMalformedInlineVideo(t *testing.T) {
	var native generateRequest
	if err := decodeMessagesValue(json.RawMessage(`{"contents":[{"parts":[{"inlineData":{"mimeType":"video/webm","data":"bm90IHdlYm0="}}]}]}`), &native); err != nil {
		t.Fatal(err)
	}
	if _, err := native.chat("model", false); err == nil {
		t.Fatal("malformed video accepted")
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

func TestGenerateTextPartSignatureRoundTrip(t *testing.T) {
	var native generateRequest
	raw := `{"contents":[{"role":"model","parts":[{"text":"answer","thoughtSignature":"c2lnbmVk"}]}]}`
	if err := decodeMessagesValue(json.RawMessage(raw), &native); err != nil {
		t.Fatal(err)
	}
	chat, err := native.chat("model", false)
	if err != nil || len(chat.Messages) != 1 || len(chat.Messages[0].NativeContent) != 1 || chat.NativeInputTokens == 0 {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	parts, err := generateParts(chat.Messages[0])
	if err != nil || len(parts) != 1 || parts[0].(map[string]any)["thoughtSignature"] != "c2lnbmVk" {
		t.Fatalf("parts=%+v err=%v", parts, err)
	}
}
func TestGenerateRequestRejectsUnsupportedFieldsAndUnions(t *testing.T) {
	for _, raw := range []string{
		`{"contents":[{"parts":[{"text":"hi"}]}],"safetySettings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"INVALID"}]}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"safetySettings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"OFF"},{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_ONLY_HIGH"}]}`,
		`{"contents":[{"parts":[{"text":"hi","inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}}]}]}`,
		`{"contents":[{"parts":[{"text":"hi","thoughtSignature":"signature"}]}]}`,
		`{"contents":[{"parts":[{"text":"private","thought":true}]}]}`,
		`{"contents":[{"role":"model","parts":[{"text":"private","thought":true,"thoughtSignature":"%%%"}]}]}`,
		`{"contents":[{"parts":[{"functionResponse":{"name":"missing","response":{}}}]}]}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":2}}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"topK":0}}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"presencePenalty":3}}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"frequencyPenalty":-3}}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"logprobs":1}}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"responseLogprobs":true,"logprobs":21}}`,
		`{"contents":[{"parts":[{"text":"hi"}]}],"generationConfig":{"responseModalities":["AUDIO"]}}`,
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
