package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponseStreamItemSnapshotReplacesAccumulatedItem(t *testing.T) {
	for _, kind := range []string{"response.output_item.added", "response.output_item.done"} {
		t.Run(kind, func(t *testing.T) {
			stream := "data: {\"type\":\"response.in_progress\",\"response\":{\"output_text\":\"obsolete\"}}\n\n" + "data: {\"type\":\"response.output_text.delta\",\"delta\":\"obsolete\"}\n\n" +
				"data: {\"type\":\"" + kind + "\",\"item\":{\"id\":\"tool\",\"type\":\"function_call\",\"name\":\"lookup\",\"call_id\":\"call\",\"arguments\":\"{}\"}}\n\n"
			result, err := streamResponseData(strings.NewReader(stream+responseTestTerminal), "m", nil)
			if err != nil {
				t.Fatal(err)
			}
			item := result.Output[0]
			if result.OutputText != "" || item.ID != "tool" || item.Type != "function_call" || item.Name != "lookup" || item.CallID != "call" || item.Arguments != "{}" || item.Role != "" || len(item.Content) != 0 || item.Status != "" {
				t.Fatalf("stale snapshot fields: result=%+v", result)
			}
		})
	}
}

func TestResponseTerminalSnapshotReplacesAccumulatedOutput(t *testing.T) {
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"obsolete\"}\n\n" +
		`data: {"type":"response.completed","response":{"id":"r","object":"response","model":"m","status":"completed","output":[{"id":"tool","type":"function_call","name":"lookup","call_id":"call","arguments":"{}"}]}}` + "\n\n"
	result, err := streamResponseData(strings.NewReader(stream), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) != 1 || result.OutputText != "" {
		t.Fatalf("result=%+v", result)
	}
	item := result.Output[0]
	if item.ID != "tool" || item.Type != "function_call" || item.Name != "lookup" || item.CallID != "call" || item.Arguments != "{}" || item.Role != "" || len(item.Content) != 0 || item.Status != "" {
		t.Fatalf("stale terminal snapshot fields: item=%+v", item)
	}
}

func TestResponseTerminalSnapshotReplacesNestedConfiguration(t *testing.T) {
	created := `{"type":"response.created","response":{"id":"r","object":"response","model":"m","status":"in_progress","reasoning":{"effort":"high","summary":"auto"},"tools":[{"type":"function","name":"lookup","description":"old","parameters":{"type":"object"}}],"prompt_cache_options":{"mode":"explicit","ttl":"30m","comparison_response_id":"resp_old"},"prompt":{"id":"pmpt_1","version":"old","variables":{"old":"value"}}}}`
	completed := `{"type":"response.completed","response":{"id":"r","object":"response","model":"m","status":"completed","reasoning":{"effort":"low"},"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"prompt_cache_options":{"mode":"implicit"},"prompt":{"id":"pmpt_1"}}}`
	result, err := streamResponseData(strings.NewReader("data: "+created+"\n\ndata: "+completed+"\n\n"), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reasoning == nil || result.Reasoning.Effort == nil || *result.Reasoning.Effort != "low" || result.Reasoning.Summary != nil {
		t.Fatalf("stale reasoning snapshot: %+v", result.Reasoning)
	}
	if len(result.Tools) != 1 || result.Tools[0].Description != "" {
		t.Fatalf("stale tools snapshot: %+v", result.Tools)
	}
	if result.PromptCacheOptions == nil || result.PromptCacheOptions.Mode != "implicit" || result.PromptCacheOptions.TTL != "" || result.PromptCacheOptions.ComparisonResponseID != "" {
		t.Fatalf("stale prompt cache snapshot: %+v", result.PromptCacheOptions)
	}
	if result.Prompt == nil || result.Prompt.Version != nil || len(result.Prompt.Variables) != 0 {
		t.Fatalf("stale prompt snapshot: %+v", result.Prompt)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), `"summary":"auto"`) || strings.Contains(string(encoded), `"description":"old"`) || strings.Contains(string(encoded), `"comparison_response_id":"resp_old"`) || strings.Contains(string(encoded), `"version":"old"`) {
		t.Fatalf("stale fields exposed: %s err=%v", encoded, err)
	}
}
