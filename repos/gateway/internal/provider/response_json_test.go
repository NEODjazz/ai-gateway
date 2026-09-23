package provider

import (
	"context"
	"encoding/json"
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
