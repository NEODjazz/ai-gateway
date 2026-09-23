package openai

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestResponseOptionsValidation(t *testing.T) {
	for _, body := range []string{`{"max_output_tokens":1}`, `{"max_tokens":1}`, `{"max_output_tokens":null,"max_tokens":null}`, `{}`, `{"top_logprobs":null,"truncation":null}`, `{"top_logprobs":0,"truncation":"auto"}`, `{"top_logprobs":20,"truncation":"disabled"}`, `{"service_tier":"priority"}`, `{"text":{"verbosity":"low"}}`, `{"text":{"verbosity":null}}`, `{"frequency_penalty":-2,"presence_penalty":2,"max_tool_calls":1000}`, `{"prompt_cache_options":{"mode":"explicit","ttl":"30m","comparison_response_id":"resp_reference"},"prompt_cache_retention":"24h"}`, `{"stream":true,"stream_options":{"include_obfuscation":false}}`} {
		var request ResponseRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		if message := request.Validate(); message != "" {
			t.Fatalf("%s: %s", body, message)
		}
	}
}

func TestResponseEnvelopeValidation(t *testing.T) {
	for _, request := range []ResponseRequest{
		{Model: "model", Input: ""},
		{Model: "model", Input: []any{map[string]any{"role": "user", "content": "hello"}}},
	} {
		if message := request.ValidateEnvelope(); message != "" {
			t.Fatalf("valid envelope rejected: %+v: %s", request, message)
		}
	}
	for _, request := range []ResponseRequest{
		{Input: "hello"},
		{Model: strings.Repeat("m", 257), Input: "hello"},
		{Model: "model"},
		{Model: "model", Input: []any{}},
		{Model: "model", Input: 42},
		{Model: "model", Input: map[string]any{"role": "user"}},
		{Model: "model", Input: []any{"hello"}},
		{Model: "model", Input: []any{nil}},
	} {
		if message := request.ValidateEnvelope(); message == "" {
			t.Fatalf("invalid envelope accepted: %+v", request)
		}
	}
}

func TestResponseStreamOptionsRequireStreaming(t *testing.T) {
	value := false
	request := ResponseRequest{StreamOptions: &ResponseStreamOptions{IncludeObfuscation: &value}}
	if message := request.Validate(); message != "stream_options requires stream=true" {
		t.Fatalf("unexpected validation result: %q", message)
	}
}

func TestResponseContextManagementValidation(t *testing.T) {
	threshold := 1000
	valid := ResponseRequest{ContextManagement: []ResponseContextEntry{{Type: "compaction", CompactThreshold: &threshold}}}
	if message := valid.Validate(); message != "" {
		t.Fatalf("valid context management rejected: %s", message)
	}
	zero := 0
	for _, request := range []ResponseRequest{
		{ContextManagement: []ResponseContextEntry{{Type: "unknown"}}},
		{ContextManagement: []ResponseContextEntry{{Type: "compaction", CompactThreshold: &zero}}},
		{ContextManagement: []ResponseContextEntry{{Type: "compaction"}, {Type: "compaction"}}},
	} {
		if message := request.Validate(); message == "" {
			t.Fatalf("invalid context management accepted: %+v", request.ContextManagement)
		}
	}
}

func TestProviderModerationValidation(t *testing.T) {
	valid := ResponseRequest{Moderation: &ProviderModeration{
		Model: "omni-moderation-latest",
		Policy: &ProviderModerationPolicy{
			Input:  &ProviderModerationRule{Mode: "block"},
			Output: &ProviderModerationRule{Mode: "score"},
		},
	}}
	if message := valid.Validate(); message != "" {
		t.Fatalf("valid moderation rejected: %s", message)
	}
	for _, moderation := range []*ProviderModeration{
		{},
		{Model: strings.Repeat("м", 257)},
		{Model: "moderation", Policy: &ProviderModerationPolicy{Input: &ProviderModerationRule{Mode: "allow"}}},
		{Model: "moderation", Policy: &ProviderModerationPolicy{Output: &ProviderModerationRule{}}},
	} {
		if message := (ResponseRequest{Moderation: moderation}).Validate(); message == "" {
			t.Fatalf("invalid moderation accepted: %+v", moderation)
		}
	}
}

func TestResponsePromptCachePrewarmValidation(t *testing.T) {
	enabled := true
	request := ResponseRequest{PromptCacheOptions: &PromptCacheOptions{Prewarm: &enabled}}
	if message := request.Validate(); message != "" {
		t.Fatalf("valid prewarm rejected: %s", message)
	}
}

func TestResponseBackgroundRequiresDurableNonStreamingStorage(t *testing.T) {
	store := true
	if message := (ResponseRequest{Background: true, Store: &store}).Validate(); message != "" {
		t.Fatal(message)
	}
	for _, request := range []ResponseRequest{{Background: true}, {Background: true, Store: &store, Stream: true}} {
		if request.Validate() == "" {
			t.Fatalf("invalid background request accepted: %+v", request)
		}
	}
}

func TestResponseRejectsInvalidGenerationControls(t *testing.T) {
	tooFew, tooMany := -1, 1001
	below, above, nan := -2.1, 2.1, math.NaN()
	negative, aboveTemperature, aboveTopP := -0.1, 2.1, 1.1
	for _, request := range []ResponseRequest{
		{FrequencyPenalty: &below}, {FrequencyPenalty: &nan}, {PresencePenalty: &above},
		{Temperature: &negative}, {Temperature: &aboveTemperature}, {Temperature: &nan},
		{TopP: &negative}, {TopP: &aboveTopP}, {TopP: &nan},
		{MaxToolCalls: &tooFew}, {MaxToolCalls: &tooMany},
	} {
		if request.Validate() == "" {
			t.Fatalf("invalid controls accepted: %+v", request)
		}
	}
}

func TestResponseAcceptsGenerationControlBoundaries(t *testing.T) {
	for _, request := range []ResponseRequest{
		{Temperature: float64Pointer(0)}, {Temperature: float64Pointer(2)},
		{TopP: float64Pointer(0)}, {TopP: float64Pointer(1)},
		{MaxToolCalls: intPointer(0)}, {MaxToolCalls: intPointer(1000)},
	} {
		if message := request.Validate(); message != "" {
			t.Fatalf("boundary rejected: %+v: %s", request, message)
		}
	}
}

func float64Pointer(value float64) *float64 { return &value }

func intPointer(value int) *int { return &value }

func TestResponseRejectsInvalidTextVerbosity(t *testing.T) {
	for _, value := range []any{"unknown", 1, true} {
		request := ResponseRequest{Text: map[string]any{"verbosity": value}}
		if message := request.Validate(); message != "text.verbosity must be low, medium, or high" {
			t.Fatalf("value=%v: %q", value, message)
		}
	}
}

func TestResponseTextConfigurationValidation(t *testing.T) {
	for _, text := range []any{
		map[string]any{},
		map[string]any{"format": map[string]any{"type": "text"}},
		map[string]any{"format": json.RawMessage(`{"type":"json_schema","name":"answer","schema":{"type":"object"}}`), "verbosity": "high"},
		map[string]any{"format": nil, "verbosity": nil},
	} {
		if message := (ResponseRequest{Text: text}).Validate(); message != "" {
			t.Fatalf("valid text configuration rejected: %#v: %s", text, message)
		}
	}
	for _, text := range []any{
		"plain",
		[]any{"text"},
		map[string]any{"unknown": true},
		map[string]any{"format": "json_object"},
		map[string]any{"format": []any{}},
	} {
		if message := (ResponseRequest{Text: text}).Validate(); message == "" {
			t.Fatalf("invalid text configuration accepted: %#v", text)
		}
	}
}

func TestResponseRejectsUnknownServiceTier(t *testing.T) {
	if message := (ResponseRequest{ServiceTier: "unknown"}).Validate(); message != "unsupported service_tier value" {
		t.Fatalf("unexpected validation result: %q", message)
	}
}

func TestResponseIncludeValidation(t *testing.T) {
	valid := []string{
		"web_search_call.action.sources",
		"code_interpreter_call.outputs",
		"computer_call_output.output.image_url",
		"file_search_call.results",
		"message.input_image.image_url",
		"message.output_text.logprobs",
		"reasoning.encrypted_content",
	}
	if message := (ResponseRequest{Include: valid}).Validate(); message != "" {
		t.Fatalf("valid include set rejected: %s", message)
	}
	for _, include := range [][]string{
		{"unknown"},
		{"reasoning.encrypted_content", "reasoning.encrypted_content"},
		append(append([]string(nil), valid...), "reasoning.encrypted_content"),
	} {
		if message := (ResponseRequest{Include: include}).Validate(); message == "" {
			t.Fatalf("invalid include set accepted: %v", include)
		}
	}
}

func TestResponseToolChoiceValidation(t *testing.T) {
	tools := []ResponseTool{
		{Type: "function", Name: "lookup"},
		{Type: "custom", Name: "dsl", Format: &ResponseCustomToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: /[a-z]+/"}},
		{Type: "mcp", ServerLabel: "documents", ServerURL: "https://documents.example.test", AllowedTools: []string{"search"}},
		{Type: "web_search", SearchContextSize: "medium"},
		{Type: "image_generation", OutputFormat: "png"},
	}
	for _, choice := range []any{
		"none", "auto", "required",
		map[string]any{"type": "function", "name": "lookup"},
		map[string]any{"type": "custom", "name": "dsl"},
		map[string]any{"type": "mcp", "server_label": "documents", "name": "search"},
		map[string]any{"type": "web_search"},
		map[string]any{"type": "web_search_preview"},
		map[string]any{"type": "image_generation"},
	} {
		if message := (ResponseRequest{Tools: tools, ToolChoice: choice}).Validate(); message != "" {
			t.Fatalf("choice %#v rejected: %s", choice, message)
		}
	}
	for _, choice := range []any{
		"unknown", "required",
		map[string]any{"type": "function", "name": "missing"},
		map[string]any{"type": "custom", "name": "missing"},
		map[string]any{"type": "function", "name": "lookup", "extra": true},
		map[string]any{"type": "mcp", "server_label": "documents", "name": "write"},
		map[string]any{"type": "mcp", "server_label": "missing", "name": "search"},
		map[string]any{"type": "unknown", "name": "lookup"},
		42,
	} {
		request := ResponseRequest{Tools: tools, ToolChoice: choice}
		if choice == "required" {
			request.Tools = nil
		}
		if message := request.Validate(); message == "" {
			t.Fatalf("invalid choice accepted: %#v", choice)
		}
	}
}

func TestResponseImageGenerationToolValidation(t *testing.T) {
	compression, partialImages := 90, 3
	validMask := "data:image/png;base64,iVBORw0KGgpmaXh0dXJl"
	for _, tool := range []ResponseTool{
		{Type: "image_generation"},
		{Type: "image_generation", Action: "generate", Background: "opaque", Model: "gpt-image", Moderation: "low", OutputCompression: &compression, OutputFormat: "jpeg", PartialImages: &partialImages, Quality: "high", Size: "1536x864"},
		{Type: "image_generation", Action: "edit", Background: "transparent", InputFidelity: "high", InputImageMask: &ResponseInputImageMask{ImageURL: validMask}, OutputFormat: "webp", Size: "auto"},
		{Type: "image_generation", InputImageMask: &ResponseInputImageMask{FileID: "file_mask"}},
	} {
		if message := (ResponseRequest{Tools: []ResponseTool{tool}}).Validate(); message != "" {
			t.Fatalf("valid image generation tool rejected: %+v: %s", tool, message)
		}
	}

	negative, tooMany := -1, 4
	for _, tools := range [][]ResponseTool{
		{{Type: "image_generation", Action: "replace"}},
		{{Type: "image_generation", Background: "transparent", OutputFormat: "jpeg"}},
		{{Type: "image_generation", InputFidelity: "medium"}},
		{{Type: "image_generation", InputImageMask: &ResponseInputImageMask{}}},
		{{Type: "image_generation", InputImageMask: &ResponseInputImageMask{FileID: "file_mask", ImageURL: validMask}}},
		{{Type: "image_generation", InputImageMask: &ResponseInputImageMask{ImageURL: "https://example.test/mask.png"}}},
		{{Type: "image_generation", OutputCompression: &negative}},
		{{Type: "image_generation", PartialImages: &tooMany}},
		{{Type: "image_generation", Quality: "maximum"}},
		{{Type: "image_generation", Size: "1000x1000"}},
		{{Type: "image_generation"}, {Type: "image_generation"}},
		{{Type: "function", Name: "draw", OutputFormat: "png"}},
	} {
		if message := (ResponseRequest{Tools: tools}).Validate(); message == "" {
			t.Fatalf("invalid image generation tools accepted: %+v", tools)
		}
	}
}

func TestResponseCustomToolValidation(t *testing.T) {
	for _, tool := range []ResponseTool{
		{Type: "custom", Name: "shell_command"},
		{Type: "custom", Name: "plain", Description: "Free-form input", Format: &ResponseCustomToolFormat{Type: "text"}},
		{Type: "custom", Name: "query", Format: &ResponseCustomToolFormat{Type: "grammar", Syntax: "lark", Definition: "start: WORD\nWORD: /[a-z]+/"}},
		{Type: "custom", Name: "code", Format: &ResponseCustomToolFormat{Type: "grammar", Syntax: "regex", Definition: "[A-Z]{2}-[0-9]+"}},
	} {
		if message := (ResponseRequest{Tools: []ResponseTool{tool}}).Validate(); message != "" {
			t.Fatalf("valid custom tool rejected: %+v: %s", tool, message)
		}
	}

	for _, tools := range [][]ResponseTool{
		{{Type: "custom"}},
		{{Type: "custom", Name: "contains space"}},
		{{Type: "custom", Name: "tool", Parameters: map[string]any{}}},
		{{Type: "custom", Name: "tool", Format: &ResponseCustomToolFormat{Type: "json"}}},
		{{Type: "custom", Name: "tool", Format: &ResponseCustomToolFormat{Type: "text", Syntax: "regex"}}},
		{{Type: "custom", Name: "tool", Format: &ResponseCustomToolFormat{Type: "grammar", Syntax: "peg", Definition: "start"}}},
		{{Type: "custom", Name: "tool", Format: &ResponseCustomToolFormat{Type: "grammar", Syntax: "regex"}}},
		{{Type: "function", Name: "same"}, {Type: "custom", Name: "same"}},
		{{Type: "custom", Name: "same"}, {Type: "function", Name: "same"}},
	} {
		if message := (ResponseRequest{Tools: tools}).Validate(); message == "" {
			t.Fatalf("invalid custom tools accepted: %+v", tools)
		}
	}
}

func TestResponseWebSearchToolValidation(t *testing.T) {
	for _, toolType := range []string{"web_search", "web_search_2025_08_26", "web_search_preview", "web_search_preview_2025_03_11"} {
		tool := ResponseTool{Type: toolType, SearchContextSize: "high", UserLocation: &ResponseWebSearchLocation{Type: "approximate", City: "Moscow", Country: "RU", Region: "Moscow", Timezone: "Europe/Moscow"}}
		if toolType == "web_search" || toolType == "web_search_2025_08_26" {
			tool.Filters = map[string]any{"allowed_domains": []string{"example.com", "docs.example.com"}}
		}
		if message := (ResponseRequest{Tools: []ResponseTool{tool}}).Validate(); message != "" {
			t.Fatalf("type %q rejected: %s", toolType, message)
		}
	}

	for _, tools := range [][]ResponseTool{
		{{Type: "web_search", SearchContextSize: "huge"}},
		{{Type: "web_search", Filters: "example.com"}},
		{{Type: "web_search", Filters: map[string]any{"blocked_domains": []string{"example.com"}}}},
		{{Type: "web_search", Filters: map[string]any{"allowed_domains": []string{"https://example.com"}}}},
		{{Type: "web_search", Filters: map[string]any{"allowed_domains": []string{"example.com", "example.com"}}}},
		{{Type: "web_search_preview", Filters: map[string]any{"allowed_domains": []string{"example.com"}}}},
		{{Type: "web_search", UserLocation: &ResponseWebSearchLocation{Type: "exact"}}},
		{{Type: "web_search", UserLocation: &ResponseWebSearchLocation{Type: "approximate", Country: "ru"}}},
		{{Type: "web_search", UserLocation: &ResponseWebSearchLocation{Type: "approximate", City: " Moscow"}}},
		{{Type: "web_search", Name: "lookup"}},
		{{Type: "web_search"}, {Type: "web_search_preview"}},
		{{Type: "function", Name: "lookup", SearchContextSize: "low"}},
	} {
		if message := (ResponseRequest{Tools: tools}).Validate(); message == "" {
			t.Fatalf("invalid web search tools accepted: %+v", tools)
		}
	}
}

func TestResponseToolDefinitionValidation(t *testing.T) {
	strict := true
	valid := []ResponseTool{
		{Type: "function", Name: "lookup", Parameters: map[string]any{"type": "object"}, Strict: &strict},
		{Type: "mcp", ServerLabel: "documents", ServerURL: "https://documents.example.test/mcp", AllowedTools: []string{"search"}, Headers: map[string]string{"X-Tenant": "example"}},
		{Type: "code_interpreter", Container: map[string]any{"type": "auto", "memory_limit": "4g", "file_ids": []string{"file_owned"}}},
		{Type: "file_search", VectorStoreIDs: []string{"vs_owned"}, Filters: map[string]any{"type": "eq", "key": "team", "value": "support"}, MaxNumResults: intPointer(12), RankingOptions: &FileSearchRankingOptions{Ranker: "auto", ScoreThreshold: floatPointer(0.4), HybridSearch: &FileSearchHybridSearch{EmbeddingWeight: floatPointer(0.7), TextWeight: floatPointer(0.3)}}, RewriteQuery: boolPointer(true)},
	}
	if message := (ResponseRequest{Tools: valid}).Validate(); message != "" {
		t.Fatalf("valid tools rejected: %s", message)
	}

	tooMany := make([]ResponseTool, 129)
	for index := range tooMany {
		tooMany[index] = ResponseTool{Type: "function", Name: "lookup"}
	}
	tooManyAllowed := make([]string, 129)
	for index := range tooManyAllowed {
		tooManyAllowed[index] = string(rune(0x100 + index))
	}
	for _, test := range []struct {
		name  string
		tools []ResponseTool
	}{
		{name: "unsupported type", tools: []ResponseTool{{Type: "unknown"}}},
		{name: "missing function name", tools: []ResponseTool{{Type: "function"}}},
		{name: "invalid function name", tools: []ResponseTool{{Type: "function", Name: "bad name"}}},
		{name: "long function name", tools: []ResponseTool{{Type: "function", Name: strings.Repeat("a", 65)}}},
		{name: "long function description", tools: []ResponseTool{{Type: "function", Name: "lookup", Description: strings.Repeat("d", 4097)}}},
		{name: "non-object parameters", tools: []ResponseTool{{Type: "function", Name: "lookup", Parameters: []any{"invalid"}}}},
		{name: "function with MCP field", tools: []ResponseTool{{Type: "function", Name: "lookup", ServerURL: "https://example.test"}}},
		{name: "duplicate function name", tools: []ResponseTool{{Type: "function", Name: "lookup"}, {Type: "function", Name: "lookup"}}},
		{name: "missing MCP URL", tools: []ResponseTool{{Type: "mcp", ServerLabel: "documents"}}},
		{name: "unsafe MCP URL", tools: []ResponseTool{{Type: "mcp", ServerLabel: "documents", ServerURL: "https://user@example.test/mcp"}}},
		{name: "MCP with function field", tools: []ResponseTool{{Type: "mcp", ServerLabel: "documents", ServerURL: "https://example.test", Parameters: map[string]any{"type": "object"}}}},
		{name: "duplicate MCP label", tools: []ResponseTool{{Type: "mcp", ServerLabel: "documents", ServerURL: "https://one.example.test"}, {Type: "mcp", ServerLabel: "documents", ServerURL: "https://two.example.test"}}},
		{name: "empty allowed tool", tools: []ResponseTool{{Type: "mcp", ServerLabel: "documents", ServerURL: "https://example.test", AllowedTools: []string{""}}}},
		{name: "duplicate allowed tool", tools: []ResponseTool{{Type: "mcp", ServerLabel: "documents", ServerURL: "https://example.test", AllowedTools: []string{"search", "search"}}}},
		{name: "too many allowed tools", tools: []ResponseTool{{Type: "mcp", ServerLabel: "documents", ServerURL: "https://example.test", AllowedTools: tooManyAllowed}}},
		{name: "missing code interpreter container", tools: []ResponseTool{{Type: "code_interpreter"}}},
		{name: "non-object code interpreter container", tools: []ResponseTool{{Type: "code_interpreter", Container: []string{"invalid"}}}},
		{name: "code interpreter container without type", tools: []ResponseTool{{Type: "code_interpreter", Container: map[string]any{}}}},
		{name: "code interpreter container with invalid type", tools: []ResponseTool{{Type: "code_interpreter", Container: map[string]any{"type": "existing"}}}},
		{name: "code interpreter container with invalid memory", tools: []ResponseTool{{Type: "code_interpreter", Container: map[string]any{"type": "auto", "memory_limit": "2g"}}}},
		{name: "code interpreter container with invalid file reference", tools: []ResponseTool{{Type: "code_interpreter", Container: map[string]any{"type": "auto", "file_ids": []string{"bad/id"}}}}},
		{name: "code interpreter container with unknown field", tools: []ResponseTool{{Type: "code_interpreter", Container: map[string]any{"type": "auto", "network": true}}}},
		{name: "duplicate code interpreter", tools: []ResponseTool{{Type: "code_interpreter", Container: map[string]any{"type": "auto"}}, {Type: "code_interpreter", Container: map[string]any{"type": "auto"}}}},
		{name: "code interpreter with function field", tools: []ResponseTool{{Type: "code_interpreter", Name: "lookup", Container: map[string]any{"type": "auto"}}}},
		{name: "missing file search stores", tools: []ResponseTool{{Type: "file_search"}}},
		{name: "duplicate file search store", tools: []ResponseTool{{Type: "file_search", VectorStoreIDs: []string{"vs_owned", "vs_owned"}}}},
		{name: "duplicate file search", tools: []ResponseTool{{Type: "file_search", VectorStoreIDs: []string{"vs_one"}}, {Type: "file_search", VectorStoreIDs: []string{"vs_two"}}}},
		{name: "file search with MCP field", tools: []ResponseTool{{Type: "file_search", VectorStoreIDs: []string{"vs_owned"}, ServerURL: "https://example.test"}}},
		{name: "file search with scalar filter", tools: []ResponseTool{{Type: "file_search", VectorStoreIDs: []string{"vs_owned"}, Filters: "team=support"}}},
		{name: "file search with unknown comparison", tools: []ResponseTool{{Type: "file_search", VectorStoreIDs: []string{"vs_owned"}, Filters: map[string]any{"type": "contains", "key": "team", "value": "support"}}}},
		{name: "file search with invalid compound filter", tools: []ResponseTool{{Type: "file_search", VectorStoreIDs: []string{"vs_owned"}, Filters: map[string]any{"type": "and", "filters": []any{}}}}},
		{name: "file search with excessive results", tools: []ResponseTool{{Type: "file_search", VectorStoreIDs: []string{"vs_owned"}, MaxNumResults: intPointer(51)}}},
		{name: "file search with invalid score", tools: []ResponseTool{{Type: "file_search", VectorStoreIDs: []string{"vs_owned"}, RankingOptions: &FileSearchRankingOptions{ScoreThreshold: floatPointer(1.1)}}}},
		{name: "file search with empty hybrid search", tools: []ResponseTool{{Type: "file_search", VectorStoreIDs: []string{"vs_owned"}, RankingOptions: &FileSearchRankingOptions{HybridSearch: &FileSearchHybridSearch{}}}}},
		{name: "too many tools", tools: tooMany},
	} {
		t.Run(test.name, func(t *testing.T) {
			if message := (ResponseRequest{Tools: test.tools}).Validate(); message == "" {
				t.Fatalf("invalid tools accepted: %+v", test.tools)
			}
		})
	}
}

func TestResponseCodeInterpreterAcceptsReusableContainerID(t *testing.T) {
	request := ResponseRequest{Tools: []ResponseTool{{Type: "code_interpreter", Container: "cntr_owned"}}}
	if message := request.Validate(); message != "" {
		t.Fatalf("valid container ID rejected: %s", message)
	}
	request.Tools[0].Container = "bad/id"
	if message := request.Validate(); message == "" {
		t.Fatal("invalid container ID was accepted")
	}
}

func floatPointer(value float64) *float64 { return &value }

func boolPointer(value bool) *bool { return &value }

func TestResponseCodeInterpreterToolChoice(t *testing.T) {
	request := ResponseRequest{
		Tools:      []ResponseTool{{Type: "code_interpreter", Container: map[string]any{"type": "auto", "memory_limit": "4g"}}},
		ToolChoice: map[string]any{"type": "code_interpreter"},
	}
	if message := request.Validate(); message != "" {
		t.Fatalf("valid code interpreter choice rejected: %s", message)
	}
	request.Tools = []ResponseTool{{Type: "function", Name: "lookup"}}
	if message := request.Validate(); message == "" {
		t.Fatal("code interpreter choice without matching tool was accepted")
	}
}

func TestResponseReasoningValidation(t *testing.T) {
	for _, body := range []string{
		`{"reasoning":{"effort":"max","summary":"concise","generate_summary":"detailed","context":"all_turns","mode":"pro"}}`,
		`{"reasoning":{"effort":"default","context":"current_turn","mode":"provider-mode"}}`,
		`{"reasoning":{}}`,
	} {
		var request ResponseRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		if message := request.Validate(); message != "" {
			t.Fatalf("%s: %s", body, message)
		}
	}
	for _, body := range []string{
		`{"reasoning":{"effort":"extreme"}}`,
		`{"reasoning":{"summary":"full"}}`,
		`{"reasoning":{"generate_summary":"full"}}`,
		`{"reasoning":{"context":"previous_turn"}}`,
		`{"reasoning":{"mode":""}}`,
	} {
		var request ResponseRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		if message := request.Validate(); message == "" {
			t.Fatalf("invalid reasoning accepted: %s", body)
		}
	}
}

func TestServiceTierValues(t *testing.T) {
	for _, value := range []string{"", "auto", "default", "on_demand", "flex", "performance", "scale", "priority", "fast", "ultrafast"} {
		if !ValidServiceTier(value) {
			t.Fatalf("documented service tier rejected: %q", value)
		}
	}
	if ValidServiceTier("unknown") {
		t.Fatal("unknown service tier accepted")
	}
}

func TestReportedServiceTierValues(t *testing.T) {
	for _, value := range []string{"", "default", "priority", "standard", "batch"} {
		if !ValidReportedServiceTier(value) {
			t.Fatalf("valid reported service tier rejected: %q", value)
		}
	}
	if ValidReportedServiceTier("unknown") {
		t.Fatal("unknown reported service tier accepted")
	}
}

func TestVerbosityValues(t *testing.T) {
	for _, value := range []string{"", "low", "medium", "high"} {
		if !validVerbosity(value) {
			t.Fatalf("documented verbosity rejected: %q", value)
		}
	}
	if validVerbosity("unknown") {
		t.Fatal("unknown verbosity accepted")
	}
}

func TestResponseSafetyIdentifierUsesUnicodeCharacterLimit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		identifier string
		valid      bool
	}{
		{name: "64 Unicode characters", identifier: strings.Repeat("я", 64), valid: true},
		{name: "65 Unicode characters", identifier: strings.Repeat("я", 65), valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := (ResponseRequest{SafetyIdentifier: tc.identifier}).Validate()
			if (message == "") != tc.valid {
				t.Fatalf("validation result %q, valid=%v", message, tc.valid)
			}
		})
	}
}

func TestResponseIsolationIdentifiersUseUnicodeCharacterLimits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request ResponseRequest
		valid   bool
	}{
		{name: "64 character prompt cache key", request: ResponseRequest{PromptCacheKey: strings.Repeat("я", 64)}, valid: true},
		{name: "65 character prompt cache key", request: ResponseRequest{PromptCacheKey: strings.Repeat("я", 65)}, valid: false},
		{name: "256 character user", request: ResponseRequest{User: strings.Repeat("я", 256)}, valid: true},
		{name: "257 character user", request: ResponseRequest{User: strings.Repeat("я", 257)}, valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := tc.request.Validate()
			if (message == "") != tc.valid {
				t.Fatalf("validation result %q, valid=%v", message, tc.valid)
			}
		})
	}
}

func TestValidateResponseInstructions(t *testing.T) {
	for _, value := range []any{"be concise", []any{map[string]any{"role": "developer", "content": "be concise"}}} {
		if message := ValidateResponseInstructions(value); message != "" {
			t.Fatalf("valid instructions rejected: %v: %s", value, message)
		}
	}
	for _, value := range []any{42, []any{}, []any{"be concise"}, []any{nil}} {
		if message := ValidateResponseInstructions(value); message == "" {
			t.Fatalf("invalid instructions accepted: %v", value)
		}
	}
}

func TestValidateResponseContextManagement(t *testing.T) {
	threshold := 4096
	for _, entries := range [][]ResponseContextEntry{nil, {{Type: "compaction"}}, {{Type: "compaction", CompactThreshold: &threshold}}} {
		if message := ValidateResponseContextManagement(entries); message != "" {
			t.Fatalf("valid context management rejected: %+v: %s", entries, message)
		}
	}
	zero := 0
	for _, entries := range [][]ResponseContextEntry{{}, {{Type: "unknown"}}, {{Type: "compaction", CompactThreshold: &zero}}, {{Type: "compaction"}, {Type: "compaction"}}} {
		if message := ValidateResponseContextManagement(entries); message == "" {
			t.Fatalf("invalid context management accepted: %+v", entries)
		}
	}
}

func TestResponseRejectsNonPositiveOutputLimits(t *testing.T) {
	for _, value := range []int{-1, 0} {
		for _, request := range []ResponseRequest{{MaxOutputTokens: &value}, {MaxTokens: &value}} {
			if request.Validate() == "" {
				t.Fatalf("non-positive output limit accepted: %+v", request)
			}
		}
	}
}

func TestResponseRejectsConflictingOutputLimits(t *testing.T) {
	for _, pair := range [][2]int{{1, 1000}, {1000, 1}, {10, 10}} {
		request := ResponseRequest{MaxOutputTokens: &pair[0], MaxTokens: &pair[1]}
		if request.Validate() == "" {
			t.Fatalf("both output caps accepted: %v", pair)
		}
	}
}

func TestResponseConversationReferenceValidation(t *testing.T) {
	for _, body := range []string{
		`{"conversation":"conv_one"}`,
		`{"conversation":{"id":"conv_two"}}`,
	} {
		var request ResponseRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil || request.Conversation == nil || request.Conversation.ID == "" || request.Validate() != "" {
			t.Fatalf("body=%s request=%+v err=%v validation=%q", body, request, err, request.Validate())
		}
	}
	invalid := []ResponseRequest{
		{Conversation: &ResponseConversation{ID: "bad"}},
		{Conversation: &ResponseConversation{ID: "conv_one"}, PreviousResponse: "resp_one"},
	}
	for _, request := range invalid {
		if request.Validate() == "" {
			t.Fatalf("invalid conversation request accepted: %+v", request)
		}
	}
	store := true
	if message := (ResponseRequest{Conversation: &ResponseConversation{ID: "conv_one"}, Background: true, Store: &store}).Validate(); message != "" {
		t.Fatalf("background conversation rejected: %s", message)
	}
}
