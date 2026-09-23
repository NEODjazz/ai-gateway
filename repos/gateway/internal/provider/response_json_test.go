package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponseOutputItemsHaveBoundedCardinality(t *testing.T) {
	if err := validateResponseOutputItems(make([]openai.ResponseOutputItem, maxResponseStreamOutputItems)); err != nil {
		t.Fatalf("boundary rejected: %v", err)
	}
	if err := validateResponseOutputItems(make([]openai.ResponseOutputItem, maxResponseStreamOutputItems+1)); err == nil {
		t.Fatal("oversized response output accepted")
	}
}

func TestResponsesRejectsInvalidJSONDocuments(t *testing.T) {
	for _, body := range []string{"null", `{"id":"r"} {"id":"second"}`, `{"id":"r"} trailing`, `{"id":`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			client := NewOpenAICompatible(server.URL, "", false)
			if _, err := client.Responses(context.Background(), openai.ResponseRequest{Model: "m", Input: "hello"}); err == nil {
				t.Fatal("invalid JSON document accepted")
			}
		})
	}
}

func TestResponsesRejectsInvalidImageGenerationResults(t *testing.T) {
	for _, output := range []string{
		`{"type":"image_generation_call","status":"completed"}`,
		`{"type":"image_generation_call","status":"completed","result":{}}`,
		`{"type":"image_generation_call","status":"completed","result":"%%%"}`,
	} {
		document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid image generation output accepted: %s", output)
		}
	}
	for _, result := range []string{`null`, `"aW1hZ2U="`} {
		document := `{"id":"r","object":"response","model":"m","status":"completed","output":[{"type":"image_generation_call","status":"completed","result":` + result + `}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err != nil {
			t.Fatalf("valid image generation output rejected: result=%s err=%v", result, err)
		}
	}
}

func TestResponsesValidateComputerCalls(t *testing.T) {
	valid := []string{
		`{"type":"computer_call","call_id":"call_1","status":"completed","actions":[{"type":"click","button":"left","x":10,"y":20},{"type":"keypress","keys":["CTRL","L"]},{"type":"type","text":"example.test"},{"type":"wait"}],"pending_safety_checks":[{"id":"check_1","code":"domain"}]}`,
		`{"type":"computer_call","call_id":"call_2","action":{"type":"drag","path":[{"x":1,"y":2},{"x":3,"y":4}]}}`,
	}
	for _, output := range valid {
		document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		response, err := decodeResponseJSON(strings.NewReader(document))
		if err != nil || len(response.Output) != 1 || response.Output[0].CallID == "" {
			t.Fatalf("valid computer output rejected: output=%s response=%+v err=%v", output, response, err)
		}
	}
	for _, output := range []string{
		`{"type":"computer_call","actions":[{"type":"wait"}]}`,
		`{"type":"computer_call","call_id":"call","actions":[]}`,
		`{"type":"computer_call","call_id":"call","action":{"type":"wait"},"actions":[{"type":"wait"}]}`,
		`{"type":"computer_call","call_id":"call","actions":[{"type":"click","x":1.5,"y":2}]}`,
		`{"type":"computer_call","call_id":"call","actions":[{"type":"unknown"}]}`,
		`{"type":"computer_call","call_id":"call","actions":[{"type":"wait"}],"pending_safety_checks":[{"id":"same"},{"id":"same"}]}`,
	} {
		document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid computer output accepted: %s", output)
		}
	}
}

func TestResponsesValidateShellCallsAndOutputs(t *testing.T) {
	valid := []string{
		`{"type":"shell_call","id":"sh_1","call_id":"call_1","status":"completed","action":{"commands":["pwd","ls -la"],"max_output_length":4096,"timeout_ms":30000},"environment":{"type":"local"},"caller":{"type":"direct"}}`,
		`{"type":"shell_call","call_id":"call_2","status":"in_progress","action":{"commands":["go test ./..."]},"environment":{"type":"container_reference","container_id":"cntr_1"},"caller":{"type":"direct"}}`,
		`{"type":"shell_call_output","id":"sho_1","call_id":"call_1","status":"completed","output":[{"stdout":"ok\n","stderr":"","outcome":{"type":"exit","exit_code":0}}]}`,
	}
	for _, output := range valid {
		document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		response, err := decodeResponseJSON(strings.NewReader(document))
		if err != nil || len(response.Output) != 1 || response.Output[0].CallID == "" {
			t.Fatalf("valid shell output rejected: output=%s response=%+v err=%v", output, response, err)
		}
	}
	invalid := []string{
		`{"type":"shell_call","status":"completed","action":{"commands":["pwd"]}}`,
		`{"type":"shell_call","call_id":"call","status":"completed","action":{"commands":[]}}`,
		`{"type":"shell_call","call_id":"call","status":"completed","action":{"commands":["pwd"],"timeout_ms":600001}}`,
		`{"type":"shell_call","call_id":"call","status":"completed","action":{"commands":["pwd"]},"environment":{"type":"container_auto"}}`,
		`{"type":"shell_call","call_id":"call","status":"completed","action":{"commands":["pwd"]},"caller":{"type":"program"}}`,
		`{"type":"shell_call_output","call_id":"call","status":"completed","output":[{"stdout":"ok","stderr":"","outcome":{"type":"exit"}}]}`,
	}
	for _, output := range invalid {
		document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid shell output accepted: %s", output)
		}
	}
}

func TestResponsesValidateApplyPatchCallsAndOutputs(t *testing.T) {
	valid := []string{
		`{"type":"apply_patch_call","id":"patch_1","call_id":"call_1","status":"completed","operation":{"type":"update_file","path":"docs/readme.md","diff":"@@ -1 +1 @@\n-old\n+new"},"caller":{"type":"direct"}}`,
		`{"type":"apply_patch_call","id":"patch_2","call_id":"call_2","status":"in_progress","operation":{"type":"delete_file","path":"tmp/old.txt"}}`,
		`{"type":"apply_patch_call_output","id":"out_1","call_id":"call_1","status":"completed","output":"updated"}`,
	}
	for _, output := range valid {
		document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		response, err := decodeResponseJSON(strings.NewReader(document))
		if err != nil || len(response.Output) != 1 || response.Output[0].CallID == "" {
			t.Fatalf("valid apply patch output rejected: output=%s response=%+v err=%v", output, response, err)
		}
	}
	invalid := []string{
		`{"type":"apply_patch_call","id":"patch","status":"completed","operation":{"type":"delete_file","path":"file"}}`,
		`{"type":"apply_patch_call","id":"patch","call_id":"call","status":"completed","operation":{"type":"delete_file","path":"../secret"}}`,
		`{"type":"apply_patch_call","id":"patch","call_id":"call","status":"completed","operation":{"type":"create_file","path":"file"}}`,
		`{"type":"apply_patch_call","id":"patch","call_id":"call","status":"incomplete","operation":{"type":"delete_file","path":"file"}}`,
		`{"type":"apply_patch_call_output","id":"out","call_id":"call","status":"in_progress"}`,
	}
	for _, output := range invalid {
		document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid apply patch output accepted: %s", output)
		}
	}
}

func TestResponsesJSONReadLimitAndErrors(t *testing.T) {
	reader := &embeddingLimitReader{}
	if _, err := decodeResponseJSON(reader); err == nil || reader.read != maxResponseJSONBytes+1 {
		t.Fatalf("read=%d err=%v", reader.read, err)
	}
	failure := errors.New("read interrupted")
	if _, err := decodeResponseJSON(embeddingErrorReader{failure}); !errors.Is(err, failure) {
		t.Fatalf("I/O error lost: %v", err)
	}
	document := `{"id":"r","object":"response","model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`
	padded := document + strings.Repeat(" ", maxResponseJSONBytes-len(document))
	response, err := decodeResponseJSON(strings.NewReader(padded))
	if err != nil || response.ID != "r" || response.OutputText != "hello" || response.Usage.TotalTokens != 3 {
		t.Fatalf("valid boundary: response=%+v err=%v", response, err)
	}
}

func TestResponsesRejectsOversizedHTTPBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"id":"r"}`); err != nil {
			return
		}
		_, _ = io.CopyN(w, &embeddingLimitReader{}, maxResponseJSONBytes)
	}))
	defer server.Close()
	client := NewOpenAICompatible(server.URL, "", false)
	if _, err := client.Responses(context.Background(), openai.ResponseRequest{Model: "m", Input: "hello"}); err == nil {
		t.Fatal("oversized HTTP response accepted")
	}
}
