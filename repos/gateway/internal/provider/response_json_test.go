package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponseOutputItemsHaveBoundedCardinality(t *testing.T) {
	items := make([]openai.ResponseOutputItem, maxResponseStreamOutputItems)
	for index := range items {
		items[index].Type = "message"
	}
	if err := validateResponseOutputItems(items); err != nil {
		t.Fatalf("boundary rejected: %v", err)
	}
	items = append(items, openai.ResponseOutputItem{Type: "message"})
	if err := validateResponseOutputItems(items); err == nil {
		t.Fatal("oversized response output accepted")
	}
}

func TestResponsesRejectsMissingOutputTypeAndOversizedPartsBeforeDelivery(t *testing.T) {
	parts := make([]string, maxResponseStreamContentParts+1)
	for index := range parts {
		parts[index] = `{"type":"output_text","text":"x"}`
	}
	for name, output := range map[string]string{
		"missing type":      `{}`,
		"oversized content": `{"type":"message","content":[` + strings.Join(parts, ",") + `]}`,
		"oversized summary": `{"type":"reasoning","summary":[` + strings.Join(parts, ",") + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `]}`
			if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
				t.Fatalf("invalid JSON output accepted: %s", output[:min(len(output), 512)])
			}
			callbacks := 0
			wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
			response, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil })
			if err == nil || callbacks != 0 {
				t.Fatalf("invalid SSE output delivered: response=%+v err=%v callbacks=%d", response, err, callbacks)
			}
		})
	}
}

func TestResponsesRejectsMissingOutputContentTypeBeforeDelivery(t *testing.T) {
	for name, output := range map[string]string{
		"message content":   `{"type":"message","content":[{}]}`,
		"reasoning summary": `{"type":"reasoning","summary":[{}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `]}`
			if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
				t.Fatalf("invalid JSON output content accepted: %s", output)
			}
			callbacks := 0
			wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
			if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
				t.Fatalf("invalid SSE output content delivered: err=%v callbacks=%d", err, callbacks)
			}
		})
	}
}

func TestResponsesRejectsUnsupportedOutputContentTypeBeforeDelivery(t *testing.T) {
	for name, output := range map[string]string{
		"message content":   `{"type":"message","content":[{"type":"input_text","text":"unexpected"}]}`,
		"reasoning summary": `{"type":"reasoning","summary":[{"type":"output_text","text":"unexpected"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `]}`
			if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
				t.Fatalf("unsupported JSON output content accepted: %s", output)
			}
			callbacks := 0
			wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
			if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
				t.Fatalf("unsupported SSE output content delivered: err=%v callbacks=%d", err, callbacks)
			}
		})
	}

	valid := `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"ok"},{"type":"refusal","refusal":"no"}]},{"type":"reasoning","summary":[{"type":"summary_text","text":"summary"}]}]}`
	if _, err := decodeResponseJSON(strings.NewReader(valid)); err != nil {
		t.Fatalf("supported output content rejected: %v", err)
	}
}

func TestResponsesRejectsConflictingOutputContentFieldsBeforeDelivery(t *testing.T) {
	invalid := []string{
		`{"type":"message","content":[{"type":"output_text","text":"answer","refusal":"no"}]}`,
		`{"type":"message","content":[{"type":"refusal","text":"answer","refusal":"no"}]}`,
		`{"type":"message","content":[{"type":"refusal","refusal":"no","annotations":[null]}]}`,
		`{"type":"message","content":[{"type":"refusal","refusal":"no","logprobs":[{"token":"n","logprob":-1}]}]}`,
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"summary","refusal":"no"}]}`,
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"summary","annotations":[null]}]}`,
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"summary","logprobs":[{"token":"s","logprob":-1}]}]}`,
	}
	for _, output := range invalid {
		document := `{"id":"r","object":"response","model":"m","status":"completed","output":[` + output + `]}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("conflicting JSON output content accepted: %s", output)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("conflicting SSE output content delivered: err=%v callbacks=%d output=%s", err, callbacks, output)
		}
	}

	valid := `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"ok","annotations":[null],"logprobs":[{"token":"o","logprob":-1}]},{"type":"refusal","refusal":"no"}]},{"type":"reasoning","summary":[{"type":"summary_text","text":"summary"}]}]}`
	if _, err := decodeResponseJSON(strings.NewReader(valid)); err != nil {
		t.Fatalf("valid output content rejected: %v", err)
	}
}

func TestResponseContentPartRejectsConflictingFieldsBeforeDelivery(t *testing.T) {
	parts := []string{
		`{"type":"output_text","text":"answer","refusal":"no"}`,
		`{"type":"refusal","text":"answer","refusal":"no"}`,
		`{"type":"refusal","refusal":"no","annotations":[null]}`,
		`{"type":"refusal","refusal":"no","logprobs":[{"token":"n","logprob":-1}]}`,
	}
	for _, part := range parts {
		callbacks := 0
		wire := `data: {"type":"response.content_part.done","part":` + part + "}\n\n" + responseTestTerminal
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("conflicting content part delivered: err=%v callbacks=%d part=%s", err, callbacks, part)
		}
	}
}

func TestResponsesValidateFunctionAndCustomToolCalls(t *testing.T) {
	valid := []string{
		`{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"city\":\"Paris\"}"}`,
		`{"type":"custom_tool_call","call_id":"call_2","name":"query","input":"status:open"}`,
	}
	for _, output := range valid {
		document := `{"id":"r","output":[` + output + `]}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err != nil {
			t.Fatalf("valid tool call rejected: output=%s err=%v", output, err)
		}
	}

	invalid := []string{
		`{"type":"function_call","name":"lookup","arguments":"{}"}`,
		`{"type":"function_call","call_id":"call","arguments":"{}"}`,
		`{"type":"function_call","call_id":"call","name":"bad name","arguments":"{}"}`,
		`{"type":"function_call","call_id":"call","name":"lookup","arguments":"[]"}`,
		`{"type":"function_call","call_id":"call","name":"lookup","arguments":"{broken"}`,
		`{"type":"custom_tool_call","name":"query","input":"status:open"}`,
		`{"type":"custom_tool_call","call_id":"call","name":"bad name","input":"status:open"}`,
	}
	for _, output := range invalid {
		document := `{"id":"r","output":[` + output + `]}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid JSON tool call accepted: %s", output)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid SSE tool call delivered: output=%s err=%v callbacks=%d", output, err, callbacks)
		}
	}

	partial := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call","name":"lookup","arguments":"{\"city\":"}}` + "\n\n" +
		`data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"city\":\"Paris\"}"}` + "\n\n" + responseTestTerminal
	if _, err := streamResponseData(strings.NewReader(partial), "m", nil); err != nil {
		t.Fatalf("partial function arguments rejected before completion: %v", err)
	}
}

func TestResponsesValidateOutputItemUnionFields(t *testing.T) {
	invalid := []string{
		`{"type":"message","role":"user","content":[{"type":"output_text","text":"wrong role"}]}`,
		`{"type":"message","content":[{"type":"output_text","text":"answer"}],"summary":[{"type":"summary_text","text":"wrong branch"}]}`,
		`{"type":"reasoning","content":[{"type":"output_text","text":"must not become output text"}]}`,
		`{"type":"function_call","call_id":"call","name":"lookup","arguments":"{}","role":"assistant"}`,
	}
	for _, output := range invalid {
		document := `{"id":"r","output":[` + output + `]}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid JSON output union accepted: %s", output)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid SSE output union delivered: output=%s err=%v callbacks=%d", output, err, callbacks)
		}
	}

	valid := `{"id":"r","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"type":"reasoning","summary":[{"type":"summary_text","text":"summary"}]}]}`
	response, err := decodeResponseJSON(strings.NewReader(valid))
	if err != nil || response.OutputText != "answer" {
		t.Fatalf("valid output union rejected: response=%+v err=%v", response, err)
	}
}

func TestResponsesRejectInvalidOutputItemIDsBeforeDelivery(t *testing.T) {
	for _, itemID := range []string{"bad/id", " bad", strings.Repeat("i", 257)} {
		document := `{"id":"r","output":[{"id":` + strconv.Quote(itemID) + `,"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid JSON output item ID accepted: %q", itemID)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid terminal output item ID delivered: id=%q err=%v callbacks=%d", itemID, err, callbacks)
		}
	}

	callbacks := 0
	wire := `data: {"type":"response.output_text.delta","item_id":"bad/id","delta":"answer"}` + "\n\n" + responseTestTerminal
	if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
		t.Fatalf("invalid incremental item_id delivered: err=%v callbacks=%d", err, callbacks)
	}
}

func TestResponsesValidateOutputItemStatuses(t *testing.T) {
	valid := map[string][]string{
		"message":                 {"in_progress", "completed", "incomplete"},
		"function_call":           {"in_progress", "completed", "incomplete"},
		"web_search_call":         {"in_progress", "searching", "completed", "failed", "incomplete"},
		"file_search_call":        {"in_progress", "searching", "completed", "failed", "incomplete"},
		"code_interpreter_call":   {"in_progress", "interpreting", "completed", "incomplete", "failed"},
		"image_generation_call":   {"in_progress", "generating", "completed", "failed"},
		"mcp_call":                {"in_progress", "calling", "completed", "incomplete", "failed"},
		"shell_call":              {"in_progress", "completed", "incomplete"},
		"apply_patch_call":        {"in_progress", "completed"},
		"apply_patch_call_output": {"completed", "failed"},
	}
	for itemType, statuses := range valid {
		for _, status := range statuses {
			if !validResponseOutputItemStatus(itemType, status) {
				t.Fatalf("valid status rejected: type=%s status=%s", itemType, status)
			}
		}
		if validResponseOutputItemStatus(itemType, "unknown") {
			t.Fatalf("unknown status accepted: type=%s", itemType)
		}
	}
	if !validResponseOutputItemStatus("future_item", "future_status") || !validResponseOutputItemStatus("message", "") {
		t.Fatal("extensible or omitted status rejected")
	}

	document := `{"id":"r","output":[{"type":"message","status":"unknown","content":[{"type":"output_text","text":"answer"}]}]}`
	if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
		t.Fatal("invalid JSON output item status accepted")
	}
	callbacks := 0
	wire := `data: {"type":"response.output_item.done","item":{"type":"message","status":"unknown","content":[{"type":"output_text","text":"answer"}]}}` + "\n\n" + responseTestTerminal
	if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
		t.Fatalf("invalid SSE output item status delivered: err=%v callbacks=%d", err, callbacks)
	}
}

func TestResponsesValidateStableStreamResponseID(t *testing.T) {
	for name, wire := range map[string]string{
		"invalid event ID": `data: {"type":"response.output_text.delta","response_id":"bad/id","delta":"answer"}` + "\n\n" + responseTestTerminal,
		"changed event ID": `data: {"type":"response.created","response":{"id":"resp_one","object":"response","status":"in_progress"}}` + "\n\n" +
			`data: {"type":"response.output_text.delta","response_id":"resp_two","delta":"answer"}` + "\n\n" + responseTestTerminal,
		"changed snapshot ID": `data: {"type":"response.created","response":{"id":"resp_one","object":"response","status":"in_progress"}}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp_two","object":"response","status":"completed"}}` + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			callbacks := 0
			if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil {
				t.Fatal("invalid response ID lifecycle accepted")
			}
			if name == "invalid event ID" && callbacks != 0 {
				t.Fatalf("invalid first event delivered: callbacks=%d", callbacks)
			}
			if name != "invalid event ID" && callbacks != 1 {
				t.Fatalf("event after established ID was delivered: callbacks=%d", callbacks)
			}
		})
	}

	wire := `data: {"type":"response.created","response":{"id":"resp_same","object":"response","status":"in_progress"}}` + "\n\n" +
		`data: {"type":"response.output_text.delta","response_id":"resp_same","delta":"answer"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"id":"resp_same","object":"response","status":"completed"}}` + "\n\n"
	response, err := streamResponseData(strings.NewReader(wire), "m", nil)
	if err != nil || response.ID != "resp_same" {
		t.Fatalf("stable response ID rejected: response=%+v err=%v", response, err)
	}
}

func TestResponsesRejectDuplicateOutputIdentities(t *testing.T) {
	for name, output := range map[string]string{
		"item ID": `[{"id":"same","type":"message","content":[{"type":"output_text","text":"A"}]},{"id":"same","type":"reasoning"}]`,
		"call ID": `[{"type":"function_call","call_id":"same","name":"first","arguments":"{}"},{"type":"custom_tool_call","call_id":"same","name":"second","input":"value"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			document := `{"id":"r","output":` + output + `}`
			if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
				t.Fatalf("duplicate output identity accepted: %s", output)
			}
			callbacks := 0
			wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
			if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
				t.Fatalf("duplicate terminal identity delivered: err=%v callbacks=%d", err, callbacks)
			}
		})
	}
}

func TestResponsesRejectDuplicateStreamingOutputIdentities(t *testing.T) {
	for name, wire := range map[string]string{
		"item ID": `data: {"type":"response.output_text.delta","output_index":0,"item_id":"same","delta":"A"}` + "\n\n" +
			`data: {"type":"response.output_text.delta","output_index":1,"item_id":"same","delta":"B"}` + "\n\n",
		"call ID": `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"first","type":"function_call","call_id":"same","name":"first","arguments":""}}` + "\n\n" +
			`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"second","type":"custom_tool_call","call_id":"same","name":"second","input":""}}` + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			callbacks := 0
			if _, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 1 {
				t.Fatalf("duplicate streaming identity delivered: err=%v callbacks=%d", err, callbacks)
			}
		})
	}
}

func TestResponsesRejectChangedStreamingOutputIdentities(t *testing.T) {
	for name, wire := range map[string]string{
		"item ID": `data: {"type":"response.output_text.delta","output_index":0,"item_id":"first","delta":"A"}` + "\n\n" +
			`data: {"type":"response.output_text.delta","output_index":0,"item_id":"second","delta":"B"}` + "\n\n",
		"call ID": `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"tool","type":"function_call","call_id":"first","name":"lookup","arguments":""}}` + "\n\n" +
			`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"tool","type":"function_call","call_id":"second","name":"lookup","arguments":"{}"}}` + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			callbacks := 0
			if _, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 1 {
				t.Fatalf("changed streaming identity delivered: err=%v callbacks=%d", err, callbacks)
			}
		})
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

func TestResponsesValidateEnvelopeIdentityAndLifecycle(t *testing.T) {
	for _, status := range []string{"", "completed", "failed", "in_progress", "cancelled", "queued", "incomplete"} {
		document := `{"id":"resp_1","object":"response","model":"m"}`
		if status != "" {
			document = `{"id":"resp_1","object":"response","model":"m","status":"` + status + `"}`
		}
		if _, err := decodeResponseJSON(strings.NewReader(document)); err != nil {
			t.Fatalf("valid status %q rejected: %v", status, err)
		}
	}

	for _, document := range []string{
		`{"id":"bad/id","object":"response","model":"m","status":"completed"}`,
		`{"id":"` + strings.Repeat("r", 257) + `","object":"response","model":"m","status":"completed"}`,
		`{"id":"resp_1","object":"chat.completion","model":"m","status":"completed"}`,
		`{"id":"resp_1","object":"response","model":" m","status":"completed"}`,
		`{"id":"resp_1","object":"response","model":"` + strings.Repeat("m", 257) + `","status":"completed"}`,
		`{"id":"resp_1","object":"response","model":"m","status":"unknown"}`,
	} {
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid response envelope accepted: %s", document)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid response envelope delivered: document=%s err=%v callbacks=%d", document, err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateAssignedServiceTier(t *testing.T) {
	for _, tier := range []string{"auto", "default", "on_demand", "flex", "performance", "scale", "priority", "fast", "ultrafast", "standard_only"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("tier=%s/stream=%v", tier, stream), func(t *testing.T) {
				document := `{"id":"resp_1","object":"response","model":"m","status":"completed","service_tier":"` + tier + `"}`
				var response openai.ResponseResponse
				var err error
				if stream {
					response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
				} else {
					response, err = decodeResponseJSON(strings.NewReader(document))
				}
				if err != nil || response.ServiceTier != tier {
					t.Fatalf("response=%+v err=%v", response, err)
				}
			})
		}
	}

	document := `{"id":"resp_1","object":"response","model":"m","status":"completed","service_tier":"unknown"}`
	if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
		t.Fatal("unknown assigned service tier accepted")
	}
	callbacks := 0
	wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
	if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
		t.Fatalf("unknown assigned service tier delivered: err=%v callbacks=%d", err, callbacks)
	}
}

func TestResponsesPreserveAndValidateMisalignmentError(t *testing.T) {
	valid := `{"code":"misalignment_policy_violation","message":"request blocked","misalignment":{"detailed_explanation":"unsafe transfer","error_type":"potentially_unintended_data_transfer","steer":{"message":"continue without private data"}}}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"failed","error":` + valid + `}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.failed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.Error == nil || response.Error.Misalignment == nil || response.Error.Misalignment.Steer == nil || response.Error.Misalignment.Steer.Message != "continue without private data" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			if err != nil || !strings.Contains(string(encoded), `"error_type":"potentially_unintended_data_transfer"`) {
				t.Fatalf("encoded=%s err=%v", encoded, err)
			}
		})
	}

	for _, responseError := range []string{
		`{"code":"misalignment_policy_violation","message":"request blocked","unknown":true}`,
		`{"code":"misalignment_policy_violation","message":"request blocked","misalignment":{"unknown":true}}`,
		`{"code":"misalignment_policy_violation","message":"request blocked","misalignment":{"steer":{"message":"continue","unknown":true}}}`,
		`{"code":"","message":"request blocked"}`,
		`{"code":" misalignment_policy_violation","message":"request blocked"}`,
		`{"code":"misalignment_policy_violation","message":" "}`,
		`{"code":"misalignment_policy_violation","message":"request blocked","misalignment":{"error_type":" invalid"}}`,
		`{"code":"misalignment_policy_violation","message":"request blocked","misalignment":{"steer":{"message":" "}}}`,
		`{"code":"` + strings.Repeat("c", 129) + `","message":"request blocked"}`,
		`{"code":"misalignment_policy_violation","message":"` + strings.Repeat("m", 8193) + `"}`,
		`{"code":"misalignment_policy_violation","message":"request blocked","misalignment":{"detailed_explanation":"` + strings.Repeat("d", 8193) + `"}}`,
		`{"code":"misalignment_policy_violation","message":"request blocked","misalignment":{"error_type":"` + strings.Repeat("e", 129) + `"}}`,
		`{"code":"misalignment_policy_violation","message":"request blocked","misalignment":{"steer":{"message":"` + strings.Repeat("s", 8193) + `"}}}`,
	} {
		document := `{"id":"r","object":"response","model":"m","status":"failed","error":` + responseError + `}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid response error accepted: %s", responseError)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.failed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid response error delivered: response_error=%s err=%v callbacks=%d", responseError, err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateIncompleteDetails(t *testing.T) {
	for _, reason := range []string{"max_output_tokens", "max_messages", "content_filter", "steered", ""} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("reason=%s/stream=%v", reason, stream), func(t *testing.T) {
				details := `{}`
				if reason != "" {
					details = `{"reason":"` + reason + `"}`
				}
				document := `{"id":"r","object":"response","model":"m","status":"incomplete","incomplete_details":` + details + `}`
				var response openai.ResponseResponse
				var err error
				if stream {
					response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.incomplete\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
				} else {
					response, err = decodeResponseJSON(strings.NewReader(document))
				}
				if err != nil || response.IncompleteDetails == nil || response.IncompleteDetails.Reason != reason {
					t.Fatalf("response=%+v err=%v", response, err)
				}
			})
		}
	}

	for _, details := range []string{
		`{"reason":"unknown"}`,
		`{"reason":"max_output_tokens","unknown":true}`,
		`{"reason":1}`,
		`null`,
	} {
		document := `{"id":"r","object":"response","model":"m","status":"incomplete","incomplete_details":` + details + `}`
		if details == "null" {
			if _, err := decodeResponseJSON(strings.NewReader(document)); err != nil {
				t.Fatalf("null incomplete_details rejected: %v", err)
			}
			continue
		}
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid incomplete_details accepted: %s", details)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.incomplete\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid incomplete_details delivered: details=%s err=%v callbacks=%d", details, err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidatePromptReference(t *testing.T) {
	valid := `{"id":"pmpt_1","version":"3","variables":{"name":"Ada","instruction":{"type":"input_text","text":"Contact {{EMAIL_1}}","prompt_cache_breakpoint":{"mode":"explicit"}},"image":{"type":"input_image","detail":"high","file_id":"file_image"},"document":{"type":"input_file","detail":"low","file_id":"file_document","filename":"facts.pdf"}}}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"resp_1","object":"response","model":"m","status":"completed","prompt":` + valid + `}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.Prompt == nil || response.Prompt.Version == nil || *response.Prompt.Version != "3" || response.Prompt.Variables["name"] != "Ada" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			if err != nil || !strings.Contains(string(encoded), `"prompt":{"id":"pmpt_1"`) || !strings.Contains(string(encoded), `"type":"input_text"`) {
				t.Fatalf("encoded=%s err=%v", encoded, err)
			}
		})
	}

	invalid := []string{
		`{}`,
		`{"id":" pmpt_1"}`,
		`{"id":"` + strings.Repeat("p", 513) + `"}`,
		`{"id":"pmpt_1","version":""}`,
		`{"id":"pmpt_1","version":"` + strings.Repeat("v", 513) + `"}`,
		`{"id":"pmpt_1","unknown":true}`,
		`{"id":"pmpt_1","variables":{" ":"value"}}`,
		`{"id":"pmpt_1","variables":{"` + strings.Repeat("k", 65) + `":"value"}}`,
		`{"id":"pmpt_1","variables":{"value":true}}`,
		`{"id":"pmpt_1","variables":{"value":{"type":"unknown"}}}`,
		`{"id":"pmpt_1","variables":{"value":{"type":"input_text"}}}`,
		`{"id":"pmpt_1","variables":{"value":{"type":"input_text","text":"x","unknown":true}}}`,
		`{"id":"pmpt_1","variables":{"value":{"type":"input_text","text":"x","prompt_cache_breakpoint":{"mode":"implicit"}}}}`,
		`{"id":"pmpt_1","variables":{"value":{"type":"input_image","detail":"medium"}}}`,
		`{"id":"pmpt_1","variables":{"value":{"type":"input_file","detail":"original"}}}`,
		`{"id":"pmpt_1","variables":{"value":{"type":"input_file","file_id":1}}}`,
		`{"id":"pmpt_1","variables":{"value":"` + strings.Repeat("x", (1<<20)+1) + `"}}`,
	}
	variables := make([]string, 257)
	for index := range variables {
		variables[index] = fmt.Sprintf(`"v%d":"x"`, index)
	}
	invalid = append(invalid, `{"id":"pmpt_1","variables":{`+strings.Join(variables, ",")+`}}`)
	for _, prompt := range invalid {
		document := `{"id":"resp_1","object":"response","model":"m","status":"completed","prompt":` + prompt + `}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid prompt accepted: %s", prompt[:min(len(prompt), 512)])
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid prompt delivered: err=%v callbacks=%d", err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateExecutionControls(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","max_output_tokens":64,"max_tool_calls":0,"parallel_tool_calls":false,"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.MaxOutputTokens == nil || *response.MaxOutputTokens != 64 || response.MaxToolCalls == nil || *response.MaxToolCalls != 0 || response.ParallelToolCalls == nil || *response.ParallelToolCalls {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			if err != nil || !strings.Contains(string(encoded), `"max_output_tokens":64`) || !strings.Contains(string(encoded), `"max_tool_calls":0`) || !strings.Contains(string(encoded), `"parallel_tool_calls":false`) {
				t.Fatalf("encoded=%s err=%v", encoded, err)
			}
		})
	}
	for _, invalid := range []string{
		`{"id":"r","object":"response","model":"m","status":"completed","max_output_tokens":0}`,
		`{"id":"r","object":"response","model":"m","status":"completed","max_output_tokens":-1}`,
	} {
		if _, err := decodeResponseJSON(strings.NewReader(invalid)); err == nil {
			t.Fatalf("invalid JSON max_output_tokens accepted: %s", invalid)
		}
	}

	document := `{"id":"r","object":"response","model":"m","status":"completed","max_tool_calls":-1}`
	if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
		t.Fatal("negative JSON max_tool_calls accepted")
	}
	callbacks := 0
	wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
	if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
		t.Fatalf("negative SSE max_tool_calls delivered: err=%v callbacks=%d", err, callbacks)
	}
}

func TestResponsesPreserveAndValidateLifecycleMetadata(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","created_at":100,"completed_at":101,"background":false,"store":false,"previous_response_id":"resp_prior"}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.CreatedAt != 100 || response.CompletedAt != 101 || response.Background == nil || *response.Background || response.Store == nil || *response.Store || response.PreviousResponseID == nil || *response.PreviousResponseID != "resp_prior" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			for _, field := range []string{`"completed_at":101`, `"background":false`, `"store":false`, `"previous_response_id":"resp_prior"`} {
				if err != nil || !strings.Contains(string(encoded), field) {
					t.Fatalf("encoded=%s missing=%s err=%v", encoded, field, err)
				}
			}
		})
	}

	for _, document := range []string{
		`{"id":"r","object":"response","model":"m","created_at":-1}`,
		`{"id":"r","object":"response","model":"m","completed_at":-1}`,
		`{"id":"r","object":"response","model":"m","created_at":2,"completed_at":1}`,
		`{"id":"r","object":"response","model":"m","previous_response_id":" "}`,
		`{"id":"r","object":"response","model":"m","previous_response_id":"` + strings.Repeat("x", 513) + `"}`,
	} {
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid lifecycle metadata accepted: %s", document)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid lifecycle metadata delivered: err=%v callbacks=%d", err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateMetadata(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","metadata":{"ticket":"42"}}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.Metadata["ticket"] != "42" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}

	tooMany := make([]string, 17)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf(`"key%d":"value"`, index)
	}
	for _, metadata := range []string{
		`{"` + strings.Repeat("я", 65) + `":"value"}`,
		`{"key":"` + strings.Repeat("я", 513) + `"}`,
		`{` + strings.Join(tooMany, ",") + `}`,
	} {
		document := `{"id":"r","object":"response","model":"m","status":"completed","metadata":` + metadata + `}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid JSON metadata accepted: %s", metadata)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid SSE metadata delivered: err=%v callbacks=%d", err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateConversationReference(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","conversation":{"id":"conv_owned"}}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.Conversation == nil || response.Conversation.ID != "conv_owned" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}

	for _, conversation := range []string{
		`{"id":""}`,
		`{"id":"response_owned"}`,
		`{"id":"conv_bad value"}`,
		`{"id":"conv_` + strings.Repeat("x", 124) + `"}`,
		`{"id":"conv_owned","unexpected":true}`,
	} {
		document := `{"id":"r","object":"response","model":"m","status":"completed","conversation":` + conversation + `}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid JSON conversation accepted: %s", conversation)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid SSE conversation delivered: err=%v callbacks=%d", err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateIsolationIdentifiers(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","user":"legacy-user","safety_identifier":"hashed-user","prompt_cache_key":"tenant-thread"}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.User != "legacy-user" || response.SafetyIdentifier != "hashed-user" || response.PromptCacheKey != "tenant-thread" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			for _, field := range []string{`"user":"legacy-user"`, `"safety_identifier":"hashed-user"`, `"prompt_cache_key":"tenant-thread"`} {
				if err != nil || !strings.Contains(string(encoded), field) {
					t.Fatalf("encoded=%s missing=%s err=%v", encoded, field, err)
				}
			}
		})
	}

	for _, document := range []string{
		`{"id":"r","object":"response","model":"m","user":"` + strings.Repeat("x", 257) + `"}`,
		`{"id":"r","object":"response","model":"m","safety_identifier":"` + strings.Repeat("x", 65) + `"}`,
		`{"id":"r","object":"response","model":"m","prompt_cache_key":"` + strings.Repeat("x", 65) + `"}`,
	} {
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid isolation identifier accepted: %s", document)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid isolation identifier delivered: err=%v callbacks=%d", err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidatePromptCacheConfiguration(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","prompt_cache_options":{"mode":"explicit","ttl":"30m","comparison_response_id":"resp_prior","prewarm":false},"prompt_cache_retention":"24h"}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.PromptCacheOptions == nil || response.PromptCacheOptions.Mode != "explicit" || response.PromptCacheOptions.TTL != "30m" || response.PromptCacheOptions.ComparisonResponseID != "resp_prior" || response.PromptCacheOptions.Prewarm == nil || *response.PromptCacheOptions.Prewarm || response.PromptCacheRetention != "24h" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			for _, field := range []string{`"prompt_cache_options":{"mode":"explicit","ttl":"30m","comparison_response_id":"resp_prior","prewarm":false}`, `"prompt_cache_retention":"24h"`} {
				if err != nil || !strings.Contains(string(encoded), field) {
					t.Fatalf("encoded=%s missing=%s err=%v", encoded, field, err)
				}
			}
		})
	}

	for _, document := range []string{
		`{"id":"r","object":"response","model":"m","prompt_cache_options":{"unknown":true}}`,
		`{"id":"r","object":"response","model":"m","prompt_cache_options":{"mode":"automatic"}}`,
		`{"id":"r","object":"response","model":"m","prompt_cache_options":{"ttl":"1h"}}`,
		`{"id":"r","object":"response","model":"m","prompt_cache_options":{"comparison_response_id":"` + strings.Repeat("x", 257) + `"}}`,
		`{"id":"r","object":"response","model":"m","prompt_cache_retention":"forever"}`,
	} {
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid prompt cache configuration accepted: %s", document)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid prompt cache configuration delivered: err=%v callbacks=%d", err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateInstructions(t *testing.T) {
	for _, instructions := range []string{`"be concise"`, `[{"role":"developer","content":"be concise"}]`} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("instructions=%s/stream=%v", instructions, stream), func(t *testing.T) {
				document := `{"id":"r","object":"response","model":"m","status":"completed","instructions":` + instructions + `}`
				var response openai.ResponseResponse
				var err error
				if stream {
					response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
				} else {
					response, err = decodeResponseJSON(strings.NewReader(document))
				}
				var expected any
				decodeErr := json.Unmarshal([]byte(instructions), &expected)
				want, wantErr := json.Marshal(expected)
				encoded, marshalErr := json.Marshal(response.Instructions)
				if err != nil || decodeErr != nil || wantErr != nil || marshalErr != nil || string(encoded) != string(want) {
					t.Fatalf("instructions=%s response=%+v err=%v marshalErr=%v", instructions, response, err, marshalErr)
				}
			})
		}
	}

	for _, instructions := range []string{`42`, `[]`, `["be concise"]`, `[null]`} {
		document := `{"id":"r","object":"response","model":"m","instructions":` + instructions + `}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid instructions accepted: %s", instructions)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid instructions delivered: instructions=%s err=%v callbacks=%d", instructions, err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateModerationResults(t *testing.T) {
	result := `{"type":"moderation_result","model":"omni-moderation","flagged":true,"categories":{"violence":true},"category_scores":{"violence":0.9},"category_applied_input_types":{"violence":["text"]}}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","moderation":{"input":` + result + `,"output":` + result + `}}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.Moderation == nil || response.Moderation.Input == nil || response.Moderation.Output == nil || !response.Moderation.Input.Flagged || response.Moderation.Output.Model != "omni-moderation" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}

	for _, moderation := range []string{
		`{}`,
		`{"input":{"type":"moderation_result","model":"m","flagged":false,"categories":{"violence":false},"category_scores":{"violence":0.1},"category_applied_input_types":{"violence":["text"]},"unknown":true}}`,
		`{"input":{"type":"unknown","model":"m","flagged":false,"categories":{"violence":false},"category_scores":{"violence":0.1},"category_applied_input_types":{"violence":["text"]}}}`,
		`{"input":{"type":"moderation_result","model":"","flagged":false,"categories":{"violence":false},"category_scores":{"violence":0.1},"category_applied_input_types":{"violence":["text"]}}}`,
		`{"input":{"type":"moderation_result","model":"m","flagged":false,"categories":{"violence":true},"category_scores":{"violence":0.1},"category_applied_input_types":{"violence":["text"]}}}`,
		`{"input":{"type":"moderation_result","model":"m","flagged":false,"categories":{"violence":false},"category_scores":{"violence":1.1},"category_applied_input_types":{"violence":["text"]}}}`,
	} {
		document := `{"id":"r","object":"response","model":"m","moderation":` + moderation + `}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid moderation accepted: %s", moderation)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid moderation delivered: moderation=%s err=%v callbacks=%d", moderation, err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidatePromptCacheDiagnostics(t *testing.T) {
	for _, diagnostics := range []string{
		`{"type":"cache_hit"}`,
		`{"type":"comparison_response_not_found"}`,
		`{"type":"unavailable"}`,
		`{"type":"cache_miss","reason":"tools_changed","cache_missed_tokens":0,"comparison_reusable_tokens":12}`,
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("diagnostics=%s/stream=%v", diagnostics, stream), func(t *testing.T) {
				document := `{"id":"r","object":"response","model":"m","status":"completed","prompt_cache_diagnostics":` + diagnostics + `}`
				var response openai.ResponseResponse
				var err error
				if stream {
					response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
				} else {
					response, err = decodeResponseJSON(strings.NewReader(document))
				}
				if err != nil || response.PromptCacheDiagnostics == nil || response.PromptCacheDiagnostics.Type == "" {
					t.Fatalf("response=%+v err=%v", response, err)
				}
			})
		}
	}

	for _, diagnostics := range []string{
		`{"type":"unknown"}`,
		`{"type":"cache_hit","reason":"tools_changed"}`,
		`{"type":"cache_hit","cache_missed_tokens":0}`,
		`{"type":"cache_miss","reason":"unknown","cache_missed_tokens":1}`,
		`{"type":"cache_miss","reason":"tools_changed"}`,
		`{"type":"cache_miss","reason":"tools_changed","cache_missed_tokens":-1}`,
		`{"type":"cache_miss","reason":"tools_changed","cache_missed_tokens":2,"comparison_reusable_tokens":1}`,
		`{"type":"cache_miss","reason":"tools_changed","cache_missed_tokens":1,"unknown":true}`,
	} {
		document := `{"id":"r","object":"response","model":"m","prompt_cache_diagnostics":` + diagnostics + `}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid prompt cache diagnostics accepted: %s", diagnostics)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid prompt cache diagnostics delivered: diagnostics=%s err=%v callbacks=%d", diagnostics, err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateContextManagement(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","context_management":[{"type":"compaction","compact_threshold":4096}]}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || len(response.ContextManagement) != 1 || response.ContextManagement[0].Type != "compaction" || response.ContextManagement[0].CompactThreshold == nil || *response.ContextManagement[0].CompactThreshold != 4096 {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}

	for _, contextManagement := range []string{
		`[]`,
		`[{"type":"unknown"}]`,
		`[{"type":"compaction","compact_threshold":0}]`,
		`[{"type":"compaction"},{"type":"compaction"}]`,
		`[{"type":"compaction","unknown":true}]`,
	} {
		document := `{"id":"r","object":"response","model":"m","context_management":` + contextManagement + `}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid context management accepted: %s", contextManagement)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid context management delivered: value=%s err=%v callbacks=%d", contextManagement, err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateGenerationSettings(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","temperature":0,"top_p":0,"truncation":"disabled"}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.Temperature == nil || *response.Temperature != 0 || response.TopP == nil || *response.TopP != 0 || response.Truncation == nil || *response.Truncation != "disabled" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			for _, field := range []string{`"temperature":0`, `"top_p":0`, `"truncation":"disabled"`} {
				if err != nil || !strings.Contains(string(encoded), field) {
					t.Fatalf("encoded=%s missing=%s err=%v", encoded, field, err)
				}
			}
		})
	}

	for _, document := range []string{
		`{"id":"r","object":"response","model":"m","temperature":-0.1}`,
		`{"id":"r","object":"response","model":"m","temperature":2.1}`,
		`{"id":"r","object":"response","model":"m","top_p":-0.1}`,
		`{"id":"r","object":"response","model":"m","top_p":1.1}`,
		`{"id":"r","object":"response","model":"m","truncation":"unknown"}`,
	} {
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid generation settings accepted: %s", document)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid generation settings delivered: err=%v callbacks=%d", err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidatePenaltySettings(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","top_logprobs":0,"frequency_penalty":0,"presence_penalty":0}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.TopLogprobs == nil || *response.TopLogprobs != 0 || response.FrequencyPenalty == nil || *response.FrequencyPenalty != 0 || response.PresencePenalty == nil || *response.PresencePenalty != 0 {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			for _, field := range []string{`"top_logprobs":0`, `"frequency_penalty":0`, `"presence_penalty":0`} {
				if err != nil || !strings.Contains(string(encoded), field) {
					t.Fatalf("encoded=%s missing=%s err=%v", encoded, field, err)
				}
			}
		})
	}

	for _, document := range []string{
		`{"id":"r","object":"response","model":"m","top_logprobs":-1}`,
		`{"id":"r","object":"response","model":"m","top_logprobs":21}`,
		`{"id":"r","object":"response","model":"m","frequency_penalty":-2.1}`,
		`{"id":"r","object":"response","model":"m","presence_penalty":2.1}`,
	} {
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid penalty settings accepted: %s", document)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid penalty settings delivered: err=%v callbacks=%d", err, callbacks)
		}
	}
}

func TestResponsesPreserveAndValidateEchoedConfiguration(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			document := `{"id":"r","object":"response","model":"m","status":"completed","reasoning":{"effort":"high","summary":"auto"},"text":{"format":{"type":"text"},"verbosity":"low"},"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"lookup"}}`
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.Reasoning == nil || response.Reasoning.Effort == nil || *response.Reasoning.Effort != "high" || len(response.Tools) != 1 || response.Tools[0].Name != "lookup" || response.Text == nil || response.ToolChoice == nil {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			for _, field := range []string{`"reasoning":{"effort":"high"`, `"text":{"format"`, `"tools":[{"type":"function","name":"lookup"`, `"tool_choice":{"name":"lookup","type":"function"}`} {
				if err != nil || !strings.Contains(string(encoded), field) {
					t.Fatalf("encoded=%s missing=%s err=%v", encoded, field, err)
				}
			}
		})
	}

	for _, document := range []string{
		`{"id":"r","object":"response","model":"m","reasoning":{"unknown":true}}`,
		`{"id":"r","object":"response","model":"m","text":{"unknown":true}}`,
		`{"id":"r","object":"response","model":"m","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"},"unknown":true}]}`,
		`{"id":"r","object":"response","model":"m","tool_choice":{"type":"function","name":"missing"}}`,
	} {
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatalf("invalid echoed configuration accepted: %s", document)
		}
		callbacks := 0
		wire := "data: {\"type\":\"response.completed\",\"response\":" + document + "}\n\n"
		if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid echoed configuration delivered: err=%v callbacks=%d", err, callbacks)
		}
	}
}

func TestResponsesAcceptEchoedAutomaticToolChoiceWithoutTools(t *testing.T) {
	document := `{"id":"r","object":"response","model":"m","status":"completed","tools":[],"tool_choice":"auto","output":[{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Summary","annotations":[]}]}],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || response.ToolChoice != "auto" || len(response.Tools) != 0 || response.Usage.TotalTokens != 5 {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
	if _, err := decodeResponseJSON(strings.NewReader(strings.Replace(document, `"tool_choice":"auto"`, `"tool_choice":"required"`, 1))); err == nil {
		t.Fatal("required tool choice without tools was accepted")
	}
}

func TestResponsesPreserveEchoedFunctionOutputSchema(t *testing.T) {
	document := `{"id":"r","object":"response","model":"m","status":"completed","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"},"output_schema":{"type":"object","properties":{"found":{"type":"boolean"}}}}],"tool_choice":"auto"}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err != nil || len(response.Tools) != 1 || response.Tools[0].OutputSchema["type"] != "object" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			if err != nil || !strings.Contains(string(encoded), `"output_schema":{"properties":{"found":{"type":"boolean"}},"type":"object"}`) {
				t.Fatalf("output_schema was not preserved: %s, err=%v", encoded, err)
			}
		})
	}
	for _, tools := range []string{
		`[{"type":"function","name":"lookup","output_schema":"invalid"}]`,
		`[{"type":"custom","name":"lookup","output_schema":{"type":"object"}}]`,
	} {
		if _, err := decodeResponseJSON(strings.NewReader(`{"id":"r","object":"response","model":"m","tools":` + tools + `}`)); err == nil {
			t.Fatalf("invalid output_schema accepted: %s", tools)
		}
	}
}

func TestResponsesReportUnsupportedEchoedToolField(t *testing.T) {
	document := `{"id":"r","object":"response","model":"m","tools":[{"type":"function","name":"lookup","unsupported_field":true}]}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var err error
			if stream {
				_, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				_, err = decodeResponseJSON(strings.NewReader(document))
			}
			if err == nil || !strings.Contains(err.Error(), `unknown field "unsupported_field"`) {
				t.Fatalf("missing unsupported field in error: %v", err)
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
