package provider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

const maxResponseJSONBytes = 32 << 20

func decodeResponseJSON(reader io.Reader) (openai.ResponseResponse, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxResponseJSONBytes+1))
	if err != nil {
		return openai.ResponseResponse{}, err
	}
	if len(payload) > maxResponseJSONBytes {
		return openai.ResponseResponse{}, errors.New("upstream Responses JSON exceeds 32 MiB")
	}
	var response *openai.ResponseResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return openai.ResponseResponse{}, err
	}
	if response == nil {
		return openai.ResponseResponse{}, errors.New("upstream Responses JSON must be an object")
	}
	if err := validateResponseConfigurationPayload(payload, response); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := recordResponseInputUsage(payload, response); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateResponseUsage(response.Usage); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateResponseEnvelope(*response); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateResponseControls(*response); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateResponseCitations(response.Citations); err != nil {
		return openai.ResponseResponse{}, err
	}
	if err := validateResponseOutputItems(response.Output); err != nil {
		return openai.ResponseResponse{}, err
	}
	response.OutputText = responseText(*response)
	return *response, nil
}

func validateResponseEnvelope(response openai.ResponseResponse) error {
	if response.ID != "" && !validResponseResourceID(response.ID) {
		return errors.New("provider returned invalid response ID")
	}
	if response.Object != "" && response.Object != "response" {
		return errors.New("provider returned invalid response object")
	}
	if response.Model != "" && (strings.TrimSpace(response.Model) != response.Model || utf8.RuneCountInString(response.Model) > 256) {
		return errors.New("provider returned invalid response model")
	}
	switch response.Status {
	case "", "completed", "failed", "in_progress", "cancelled", "queued", "incomplete":
		return nil
	default:
		return errors.New("provider returned invalid response status")
	}
}

func validateResponseConfigurationPayload(payload []byte, response *openai.ResponseResponse) error {
	var wire struct {
		Reasoning              json.RawMessage `json:"reasoning"`
		Text                   json.RawMessage `json:"text"`
		Tools                  json.RawMessage `json:"tools"`
		ToolChoice             json.RawMessage `json:"tool_choice"`
		PromptCacheOptions     json.RawMessage `json:"prompt_cache_options"`
		Moderation             json.RawMessage `json:"moderation"`
		PromptCacheDiagnostics json.RawMessage `json:"prompt_cache_diagnostics"`
		ContextManagement      json.RawMessage `json:"context_management"`
		Error                  json.RawMessage `json:"error"`
		IncompleteDetails      json.RawMessage `json:"incomplete_details"`
		Prompt                 json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return err
	}
	if len(wire.Reasoning) > 0 && string(wire.Reasoning) != "null" {
		response.Reasoning = nil
		if err := decodeStrictResponseConfiguration(wire.Reasoning, &response.Reasoning); err != nil {
			return errors.New("provider returned invalid response reasoning configuration")
		}
	}
	if len(wire.Tools) > 0 && string(wire.Tools) != "null" {
		response.Tools = nil
		if err := decodeStrictResponseConfiguration(wire.Tools, &response.Tools); err != nil {
			return errors.New("provider returned invalid response tools configuration")
		}
	}
	if len(wire.Text) > 0 {
		response.Text = nil
		if err := json.Unmarshal(wire.Text, &response.Text); err != nil {
			return errors.New("provider returned invalid response text configuration")
		}
	}
	if len(wire.ToolChoice) > 0 {
		response.ToolChoice = nil
		if err := json.Unmarshal(wire.ToolChoice, &response.ToolChoice); err != nil {
			return errors.New("provider returned invalid response tool_choice")
		}
	}
	if len(wire.PromptCacheOptions) > 0 && string(wire.PromptCacheOptions) != "null" {
		response.PromptCacheOptions = nil
		if err := decodeStrictResponseConfiguration(wire.PromptCacheOptions, &response.PromptCacheOptions); err != nil {
			return errors.New("provider returned invalid prompt_cache_options")
		}
	}
	if len(wire.Moderation) > 0 && string(wire.Moderation) != "null" {
		response.Moderation = nil
		if err := decodeStrictResponseConfiguration(wire.Moderation, &response.Moderation); err != nil {
			return errors.New("provider returned invalid response moderation results")
		}
	}
	if len(wire.PromptCacheDiagnostics) > 0 && string(wire.PromptCacheDiagnostics) != "null" {
		response.PromptCacheDiagnostics = nil
		if err := decodeStrictResponseConfiguration(wire.PromptCacheDiagnostics, &response.PromptCacheDiagnostics); err != nil {
			return errors.New("provider returned invalid prompt_cache_diagnostics")
		}
	}
	if len(wire.ContextManagement) > 0 && string(wire.ContextManagement) != "null" {
		response.ContextManagement = nil
		if err := decodeStrictResponseConfiguration(wire.ContextManagement, &response.ContextManagement); err != nil {
			return errors.New("provider returned invalid response context_management")
		}
	}
	if len(wire.Error) > 0 && string(wire.Error) != "null" {
		response.Error = nil
		if err := decodeStrictResponseConfiguration(wire.Error, &response.Error); err != nil {
			return errors.New("provider returned invalid response error")
		}
	}
	if len(wire.IncompleteDetails) > 0 && string(wire.IncompleteDetails) != "null" {
		response.IncompleteDetails = nil
		if err := decodeStrictResponseConfiguration(wire.IncompleteDetails, &response.IncompleteDetails); err != nil {
			return errors.New("provider returned invalid response incomplete_details")
		}
	}
	if len(wire.Prompt) > 0 && string(wire.Prompt) != "null" {
		response.Prompt = nil
		if err := decodeStrictResponseConfiguration(wire.Prompt, &response.Prompt); err != nil {
			return errors.New("provider returned invalid response prompt")
		}
	}
	if message := openai.ValidateResponseConfiguration(response.Tools, response.ToolChoice, response.Reasoning, response.Text); message != "" {
		return errors.New("provider returned invalid response configuration: " + message)
	}
	return nil
}

func decodeStrictResponseConfiguration(payload []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func validateResponseControls(response openai.ResponseResponse) error {
	if response.CreatedAt < 0 || response.CompletedAt < 0 || response.CreatedAt > 0 && response.CompletedAt > 0 && response.CompletedAt < response.CreatedAt {
		return errors.New("provider returned invalid response timestamps")
	}
	if message := openai.ValidateMetadata(response.Metadata); message != "" {
		return errors.New("provider returned invalid response metadata: " + message)
	}
	if response.PreviousResponseID != nil && (*response.PreviousResponseID == "" || strings.TrimSpace(*response.PreviousResponseID) != *response.PreviousResponseID || utf8.RuneCountInString(*response.PreviousResponseID) > 512) {
		return errors.New("provider returned invalid previous_response_id")
	}
	if response.Conversation != nil && (!strings.HasPrefix(response.Conversation.ID, "conv_") || len(response.Conversation.ID) > 128 || !validResponseResourceID(response.Conversation.ID)) {
		return errors.New("provider returned invalid conversation ID")
	}
	if utf8.RuneCountInString(response.User) > 256 {
		return errors.New("provider returned invalid user")
	}
	if utf8.RuneCountInString(response.SafetyIdentifier) > 64 {
		return errors.New("provider returned invalid safety_identifier")
	}
	if utf8.RuneCountInString(response.PromptCacheKey) > 64 {
		return errors.New("provider returned invalid prompt_cache_key")
	}
	if message := openai.ValidatePromptCacheOptions(response.PromptCacheOptions); message != "" {
		return errors.New("provider returned invalid prompt_cache_options: " + message)
	}
	if response.PromptCacheRetention != "" && response.PromptCacheRetention != "in_memory" && response.PromptCacheRetention != "24h" {
		return errors.New("provider returned invalid prompt_cache_retention")
	}
	if !openai.ValidServiceTier(response.ServiceTier) {
		return errors.New("provider returned invalid response service_tier")
	}
	if message := openai.ValidateResponseInstructions(response.Instructions); message != "" {
		return errors.New("provider returned invalid instructions: " + message)
	}
	if err := validateResponseModeration(response.Moderation); err != nil {
		return err
	}
	if err := validateResponsePromptCacheDiagnostics(response.PromptCacheDiagnostics); err != nil {
		return err
	}
	if message := openai.ValidateResponseContextManagement(response.ContextManagement); message != "" {
		return errors.New("provider returned invalid response context_management: " + message)
	}
	if err := validateResponseError(response.Error); err != nil {
		return err
	}
	if err := validateResponseIncompleteDetails(response.IncompleteDetails); err != nil {
		return err
	}
	if err := validateResponsePrompt(response.Prompt); err != nil {
		return err
	}
	if response.MaxOutputTokens != nil && *response.MaxOutputTokens <= 0 {
		return errors.New("provider returned invalid max_output_tokens")
	}
	if response.MaxToolCalls != nil && *response.MaxToolCalls < 0 {
		return errors.New("provider returned invalid negative max_tool_calls")
	}
	if response.Temperature != nil && (math.IsNaN(*response.Temperature) || math.IsInf(*response.Temperature, 0) || *response.Temperature < 0 || *response.Temperature > 2) {
		return errors.New("provider returned invalid temperature")
	}
	if response.TopP != nil && (math.IsNaN(*response.TopP) || math.IsInf(*response.TopP, 0) || *response.TopP < 0 || *response.TopP > 1) {
		return errors.New("provider returned invalid top_p")
	}
	if response.TopLogprobs != nil && (*response.TopLogprobs < 0 || *response.TopLogprobs > 20) {
		return errors.New("provider returned invalid top_logprobs")
	}
	for _, penalty := range []*float64{response.FrequencyPenalty, response.PresencePenalty} {
		if penalty != nil && (math.IsNaN(*penalty) || math.IsInf(*penalty, 0) || *penalty < -2 || *penalty > 2) {
			return errors.New("provider returned invalid frequency or presence penalty")
		}
	}
	if response.Truncation != nil && *response.Truncation != "auto" && *response.Truncation != "disabled" {
		return errors.New("provider returned invalid truncation")
	}
	return nil
}

func validateResponsePrompt(prompt *openai.ResponsePrompt) error {
	if prompt == nil {
		return nil
	}
	if strings.TrimSpace(prompt.ID) != prompt.ID || prompt.ID == "" || utf8.RuneCountInString(prompt.ID) > 512 {
		return errors.New("provider returned invalid response prompt ID")
	}
	if prompt.Version != nil && (strings.TrimSpace(*prompt.Version) != *prompt.Version || *prompt.Version == "" || utf8.RuneCountInString(*prompt.Version) > 512) {
		return errors.New("provider returned invalid response prompt version")
	}
	if len(prompt.Variables) > 256 {
		return errors.New("provider returned too many response prompt variables")
	}
	for key, value := range prompt.Variables {
		if strings.TrimSpace(key) == "" || utf8.RuneCountInString(key) > 64 {
			return errors.New("provider returned invalid response prompt variable name")
		}
		if text, ok := value.(string); ok {
			if utf8.RuneCountInString(text) > 1<<20 {
				return errors.New("provider returned oversized response prompt variable")
			}
			continue
		}
		object, ok := value.(map[string]any)
		if !ok || !validResponsePromptVariableObject(object) {
			return errors.New("provider returned invalid response prompt variable")
		}
	}
	return nil
}

func validResponsePromptVariableObject(object map[string]any) bool {
	typeName, ok := object["type"].(string)
	if !ok {
		return false
	}
	allowed := map[string]bool{"type": true, "prompt_cache_breakpoint": true}
	stringLimits := map[string]int{}
	switch typeName {
	case "input_text":
		allowed["text"], stringLimits["text"] = true, 1<<20
		if _, ok := object["text"].(string); !ok {
			return false
		}
	case "input_image":
		allowed["detail"], allowed["file_id"], allowed["image_url"] = true, true, true
		stringLimits["file_id"], stringLimits["image_url"] = 512, 16<<20
		if detail, present := object["detail"]; present && detail != "low" && detail != "high" && detail != "auto" && detail != "original" {
			return false
		}
	case "input_file":
		for _, key := range []string{"detail", "file_data", "file_id", "file_url", "filename"} {
			allowed[key] = true
		}
		stringLimits["file_data"], stringLimits["file_id"], stringLimits["file_url"], stringLimits["filename"] = 24<<20, 512, 8192, 512
		if detail, present := object["detail"]; present && detail != "auto" && detail != "low" && detail != "high" {
			return false
		}
	default:
		return false
	}
	for key, value := range object {
		if !allowed[key] {
			return false
		}
		if limit, bounded := stringLimits[key]; bounded {
			text, ok := value.(string)
			if !ok || utf8.RuneCountInString(text) > limit {
				return false
			}
		}
	}
	if breakpoint, present := object["prompt_cache_breakpoint"]; present {
		value, ok := breakpoint.(map[string]any)
		if !ok || len(value) != 1 || value["mode"] != "explicit" {
			return false
		}
	}
	return true
}

func validateResponseIncompleteDetails(details *openai.ResponseIncompleteDetails) error {
	if details == nil || details.Reason == "" {
		return nil
	}
	switch details.Reason {
	case "max_output_tokens", "max_messages", "content_filter", "steered":
		return nil
	default:
		return errors.New("provider returned invalid response incomplete reason")
	}
}

func validateResponseError(responseError *openai.ResponseError) error {
	if responseError == nil {
		return nil
	}
	if strings.TrimSpace(responseError.Code) != responseError.Code || responseError.Code == "" || utf8.RuneCountInString(responseError.Code) > 128 {
		return errors.New("provider returned invalid response error code")
	}
	if strings.TrimSpace(responseError.Message) == "" || utf8.RuneCountInString(responseError.Message) > 8192 {
		return errors.New("provider returned invalid response error message")
	}
	misalignment := responseError.Misalignment
	if misalignment == nil {
		return nil
	}
	if utf8.RuneCountInString(misalignment.DetailedExplanation) > 8192 {
		return errors.New("provider returned oversized response misalignment explanation")
	}
	if strings.TrimSpace(misalignment.ErrorType) != misalignment.ErrorType || utf8.RuneCountInString(misalignment.ErrorType) > 128 {
		return errors.New("provider returned invalid response misalignment error type")
	}
	if misalignment.Steer != nil && (strings.TrimSpace(misalignment.Steer.Message) == "" || utf8.RuneCountInString(misalignment.Steer.Message) > 8192) {
		return errors.New("provider returned invalid response misalignment steer")
	}
	return nil
}

func validateResponseModeration(moderation *openai.ResponseModeration) error {
	if moderation == nil {
		return nil
	}
	if moderation.Input == nil && moderation.Output == nil {
		return errors.New("provider returned empty response moderation results")
	}
	for _, result := range []*openai.ResponseModerationResult{moderation.Input, moderation.Output} {
		if result == nil {
			continue
		}
		if result.Type != "moderation_result" || strings.TrimSpace(result.Model) == "" || len(result.Model) > 512 {
			return errors.New("provider returned invalid response moderation identity")
		}
		if err := validateModerationResult(openai.ModerationResult{
			Flagged: result.Flagged, Categories: result.Categories, CategoryScores: result.CategoryScores, CategoryAppliedInputTypes: result.CategoryAppliedInputTypes,
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateResponsePromptCacheDiagnostics(diagnostics *openai.ResponsePromptCacheDiagnostics) error {
	if diagnostics == nil {
		return nil
	}
	switch diagnostics.Type {
	case "cache_hit", "comparison_response_not_found", "unavailable":
		if diagnostics.Reason != "" || diagnostics.CacheMissedTokens != nil || diagnostics.ComparisonReusableTokens != nil {
			return errors.New("provider returned inconsistent prompt_cache_diagnostics")
		}
	case "cache_miss":
		validReasons := map[string]bool{
			"model_changed": true, "prompt_cache_key_changed": true, "service_tier_changed": true,
			"tools_changed": true, "text_format_changed": true, "reasoning_effort_changed": true,
			"verbosity_changed": true, "context_compacted": true, "input_changed": true,
		}
		if !validReasons[diagnostics.Reason] || diagnostics.CacheMissedTokens == nil || *diagnostics.CacheMissedTokens < 0 {
			return errors.New("provider returned invalid prompt cache miss diagnostics")
		}
		if diagnostics.ComparisonReusableTokens != nil && (*diagnostics.ComparisonReusableTokens < 0 || *diagnostics.CacheMissedTokens > *diagnostics.ComparisonReusableTokens) {
			return errors.New("provider returned invalid prompt cache diagnostic token counts")
		}
	default:
		return errors.New("provider returned unknown prompt_cache_diagnostics type")
	}
	return nil
}

func validateResponseCitations(citations []string) error {
	if len(citations) > 1024 {
		return errors.New("provider returned too many response citations")
	}
	for _, citation := range citations {
		parsed, err := url.Parse(citation)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || utf8.RuneCountInString(citation) > 8192 {
			return errors.New("provider returned an invalid response citation")
		}
	}
	return nil
}

func validateResponseOutputItems(items []openai.ResponseOutputItem) error {
	return validateResponseOutputItemsMode(items, false, false)
}

func validateResponseOutputItemsAllowSparse(items []openai.ResponseOutputItem, allowSparse bool) error {
	return validateResponseOutputItemsMode(items, allowSparse, false)
}

func validatePartialResponseOutputItems(items []openai.ResponseOutputItem) error {
	return validateResponseOutputItemsMode(items, false, true)
}

func validateResponseOutputItemsMode(items []openai.ResponseOutputItem, allowSparse, allowPartial bool) error {
	if len(items) > maxResponseStreamOutputItems {
		return errors.New("provider returned too many response output items")
	}
	for _, item := range items {
		if item.Type == "" {
			if allowSparse {
				continue
			}
			return errors.New("provider returned response output item without type")
		}
		if len(item.Content) > maxResponseStreamContentParts || len(item.Summary) > maxResponseStreamContentParts {
			return errors.New("provider returned too many response output content parts")
		}
		for _, parts := range [][]openai.ResponseOutputContent{item.Content, item.Summary} {
			for _, part := range parts {
				if part.Type == "" && !allowSparse {
					return errors.New("provider returned response output content without type")
				}
			}
		}
		if item.Type == "message" {
			for _, part := range item.Content {
				if part.Type != "" && part.Type != "output_text" && part.Type != "refusal" {
					return errors.New("provider returned unsupported response message content type")
				}
			}
		}
		if item.Type == "reasoning" {
			for _, part := range item.Summary {
				if part.Type != "" && part.Type != "summary_text" {
					return errors.New("provider returned unsupported response reasoning summary type")
				}
			}
		}
		switch item.Type {
		case "function_call":
			if err := validateResponseFunctionCall(item, allowPartial, allowSparse); err != nil {
				return err
			}
			continue
		case "custom_tool_call":
			if err := validateResponseCustomToolCall(item, allowSparse); err != nil {
				return err
			}
			continue
		case "computer_call":
			if err := validateResponseComputerCall(item); err != nil {
				return err
			}
			continue
		case "shell_call":
			if err := validateResponseShellCall(item); err != nil {
				return err
			}
			continue
		case "shell_call_output":
			if _, message := openai.InspectResponseShellCallOutputs([]openai.ResponseOutputItem{item}); message != "" {
				return errors.New("provider " + message)
			}
			continue
		case "apply_patch_call":
			if message := openai.ValidateResponseApplyPatchCall(item); message != "" {
				return errors.New(message)
			}
			continue
		case "apply_patch_call_output":
			if _, message := openai.InspectResponseApplyPatchCallOutputs([]openai.ResponseOutputItem{item}); message != "" {
				return errors.New("provider " + message)
			}
			continue
		}
		if item.Type != "image_generation_call" {
			continue
		}
		if len(item.Result) == 0 {
			return errors.New("provider image generation call is missing result")
		}
		if string(item.Result) == "null" {
			continue
		}
		var encoded string
		if json.Unmarshal(item.Result, &encoded) != nil || encoded == "" {
			return errors.New("provider image generation result must be base64 or null")
		}
		decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded))
		if _, err := io.Copy(io.Discard, decoder); err != nil {
			return errors.New("provider image generation result is malformed base64")
		}
	}
	return nil
}

func validateResponseFunctionCall(item openai.ResponseOutputItem, allowPartial, allowSparse bool) error {
	if item.CallID == "" && !allowSparse {
		return errors.New("provider function call has an invalid call_id")
	}
	if item.CallID != "" && !validResponseCallID(item.CallID) {
		return errors.New("provider function call has an invalid call_id")
	}
	if item.Name == "" && !allowSparse {
		return errors.New("provider function call has an invalid name")
	}
	if item.Name != "" && !validResponseToolName(item.Name) {
		return errors.New("provider function call has an invalid name")
	}
	if utf8.RuneCountInString(item.Arguments) > openai.MaxChatFunctionArgumentsChars {
		return errors.New("provider function call arguments are too large")
	}
	if allowPartial {
		return nil
	}
	var arguments map[string]json.RawMessage
	if json.Unmarshal([]byte(item.Arguments), &arguments) != nil || arguments == nil {
		return errors.New("provider function call arguments must be a JSON object")
	}
	return nil
}

func validateResponseCustomToolCall(item openai.ResponseOutputItem, allowSparse bool) error {
	if item.CallID == "" && !allowSparse {
		return errors.New("provider custom tool call has an invalid call_id")
	}
	if item.CallID != "" && !validResponseCallID(item.CallID) {
		return errors.New("provider custom tool call has an invalid call_id")
	}
	if item.Name == "" && !allowSparse {
		return errors.New("provider custom tool call has an invalid name")
	}
	if item.Name != "" && !validResponseToolName(item.Name) {
		return errors.New("provider custom tool call has an invalid name")
	}
	return nil
}

func validResponseCallID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.RuneCountInString(value) <= 512
}

func validResponseToolName(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validateResponseShellCall(item openai.ResponseOutputItem) error {
	if strings.TrimSpace(item.CallID) != item.CallID || item.CallID == "" || utf8.RuneCountInString(item.CallID) > 512 {
		return errors.New("provider shell call has an invalid call_id")
	}
	if item.ID != "" && (strings.TrimSpace(item.ID) != item.ID || utf8.RuneCountInString(item.ID) > 512) {
		return errors.New("provider shell call has an invalid id")
	}
	if item.CreatedBy != "" && (strings.TrimSpace(item.CreatedBy) != item.CreatedBy || utf8.RuneCountInString(item.CreatedBy) > 512) {
		return errors.New("provider shell call has an invalid created_by")
	}
	status, _ := json.Marshal(item.Status)
	if message := openai.ValidateResponseShellStatus(status, "provider shell call status"); message != "" {
		return errors.New(message)
	}
	if message := openai.ValidateResponseShellCallAction(item.Action); message != "" {
		return errors.New(message)
	}
	if len(item.Caller) > 0 && string(item.Caller) != "null" {
		if message := openai.ValidateResponseShellCaller(item.Caller, "provider shell call caller"); message != "" {
			return errors.New(message)
		}
	}
	if len(item.Environment) > 0 && string(item.Environment) != "null" {
		var environment any
		if json.Unmarshal(item.Environment, &environment) != nil {
			return errors.New("provider shell call has an invalid environment")
		}
		kind, _, _, message := openai.InspectResponseShellEnvironment(environment)
		if message != "" || kind == "container_auto" {
			return errors.New("provider shell call has an invalid environment")
		}
	}
	return nil
}

func validateResponseComputerCall(item openai.ResponseOutputItem) error {
	if strings.TrimSpace(item.CallID) != item.CallID || item.CallID == "" || utf8.RuneCountInString(item.CallID) > 512 {
		return errors.New("provider computer call has an invalid call_id")
	}
	if item.Status != "" && item.Status != "in_progress" && item.Status != "completed" && item.Status != "incomplete" {
		return errors.New("provider computer call has an invalid status")
	}
	hasAction := len(item.Action) != 0 && string(item.Action) != "null"
	hasActions := len(item.Actions) != 0
	if hasAction == hasActions {
		return errors.New("provider computer call requires exactly one of action or actions")
	}
	actions := item.Actions
	if hasAction {
		actions = []json.RawMessage{item.Action}
	}
	if len(actions) > 128 {
		return errors.New("provider computer call contains too many actions")
	}
	for _, action := range actions {
		if err := validateResponseComputerAction(action); err != nil {
			return err
		}
	}
	if len(item.PendingSafetyChecks) > 128 {
		return errors.New("provider computer call contains too many pending safety checks")
	}
	seen := make(map[string]struct{}, len(item.PendingSafetyChecks))
	for _, raw := range item.PendingSafetyChecks {
		var check map[string]json.RawMessage
		if json.Unmarshal(raw, &check) != nil || check == nil {
			return errors.New("provider computer call contains an invalid safety check")
		}
		var id string
		if json.Unmarshal(check["id"], &id) != nil || strings.TrimSpace(id) != id || id == "" || utf8.RuneCountInString(id) > 512 {
			return errors.New("provider computer call safety check has an invalid id")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("provider computer call contains duplicate safety checks")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateResponseComputerAction(raw json.RawMessage) error {
	var action map[string]json.RawMessage
	if json.Unmarshal(raw, &action) != nil || action == nil {
		return errors.New("provider computer call contains an invalid action")
	}
	var actionType string
	if json.Unmarshal(action["type"], &actionType) != nil {
		return errors.New("provider computer call action is missing type")
	}
	requireCoordinates := func(names ...string) bool {
		for _, name := range names {
			if !responseComputerInteger(action[name]) {
				return false
			}
		}
		return true
	}
	switch actionType {
	case "click", "double_click":
		if !requireCoordinates("x", "y") {
			return errors.New("provider computer pointer action has invalid coordinates")
		}
		var button string
		if json.Unmarshal(action["button"], &button) != nil || button != "left" && button != "right" && button != "wheel" && button != "back" && button != "forward" {
			return errors.New("provider computer pointer action has an invalid button")
		}
	case "move":
		if !requireCoordinates("x", "y") {
			return errors.New("provider computer pointer action has invalid coordinates")
		}
	case "scroll":
		if !requireCoordinates("x", "y", "scroll_x", "scroll_y") {
			return errors.New("provider computer scroll action has invalid coordinates")
		}
	case "drag":
		var path []map[string]json.RawMessage
		if json.Unmarshal(action["path"], &path) != nil || len(path) == 0 || len(path) > 1024 {
			return errors.New("provider computer drag action has an invalid path")
		}
		for _, point := range path {
			if !responseComputerInteger(point["x"]) || !responseComputerInteger(point["y"]) {
				return errors.New("provider computer drag action has invalid coordinates")
			}
		}
	case "keypress":
		var keys []string
		if json.Unmarshal(action["keys"], &keys) != nil || len(keys) == 0 || len(keys) > 32 {
			return errors.New("provider computer keypress action has invalid keys")
		}
		for _, key := range keys {
			if strings.TrimSpace(key) == "" || utf8.RuneCountInString(key) > 64 {
				return errors.New("provider computer keypress action has invalid keys")
			}
		}
	case "type":
		var text string
		if json.Unmarshal(action["text"], &text) != nil || len(text) > 1<<20 || !utf8.ValidString(text) {
			return errors.New("provider computer type action has invalid text")
		}
	case "wait", "screenshot":
	default:
		return errors.New("provider computer call contains an unsupported action type")
	}
	return nil
}

func responseComputerInteger(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	value := string(raw)
	if strings.ContainsAny(value, ".eE") {
		return false
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}
