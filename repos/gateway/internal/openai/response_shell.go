package openai

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxResponseShellItems        = 128
	maxResponseShellOutputBytes  = 8 << 20
	maxResponseShellCommandBytes = 1 << 20
)

// ResponseShellCallOutput identifies one client-executed shell call returned
// to a Responses continuation.
type ResponseShellCallOutput struct {
	CallID string
}

// InspectResponseShellCallOutputs validates shell_call_output input items and
// returns the calls that must participate in authorization and accounting.
func InspectResponseShellCallOutputs(input any) ([]ResponseShellCallOutput, string) {
	if _, ok := input.(string); ok || input == nil {
		return nil, ""
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, "shell_call_output input is invalid"
	}
	var items []json.RawMessage
	if json.Unmarshal(encoded, &items) != nil {
		return nil, ""
	}
	outputs := make([]ResponseShellCallOutput, 0)
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		var itemType string
		if json.Unmarshal(item["type"], &itemType) != nil || itemType != "shell_call_output" {
			continue
		}
		if len(outputs) >= maxResponseShellItems {
			return nil, "input must contain at most 128 shell_call_output items"
		}
		if !onlyResponseShellOptionalKeys(item, "type", "id", "call_id", "output", "max_output_length", "status", "caller", "created_by") {
			return nil, "shell_call_output contains an unsupported field"
		}
		var output ResponseShellCallOutput
		if json.Unmarshal(item["call_id"], &output.CallID) != nil || !validResponseShellIdentifier(output.CallID) {
			return nil, "shell_call_output.call_id must contain between 1 and 512 characters"
		}
		if rawID, supplied := item["id"]; supplied && !bytes.Equal(bytes.TrimSpace(rawID), []byte("null")) {
			var id string
			if json.Unmarshal(rawID, &id) != nil || !validResponseShellIdentifier(id) {
				return nil, "shell_call_output.id is invalid"
			}
		}
		if rawCreator, supplied := item["created_by"]; supplied && !bytes.Equal(bytes.TrimSpace(rawCreator), []byte("null")) {
			var creator string
			if json.Unmarshal(rawCreator, &creator) != nil || !validResponseShellIdentifier(creator) {
				return nil, "shell_call_output.created_by is invalid"
			}
		}
		if message := validateResponseShellStatus(item["status"], "shell_call_output.status"); message != "" {
			return nil, message
		}
		if message := validateResponseShellCaller(item["caller"], "shell_call_output.caller"); message != "" {
			return nil, message
		}
		if rawLimit, supplied := item["max_output_length"]; supplied && !bytes.Equal(bytes.TrimSpace(rawLimit), []byte("null")) {
			var limit int
			if json.Unmarshal(rawLimit, &limit) != nil || limit < 1 || limit > maxResponseShellOutputBytes {
				return nil, "shell_call_output.max_output_length must be between 1 and 8388608"
			}
		}
		var chunks []map[string]json.RawMessage
		if json.Unmarshal(item["output"], &chunks) != nil || len(chunks) == 0 || len(chunks) > maxResponseShellItems {
			return nil, "shell_call_output.output must contain between 1 and 128 objects"
		}
		totalBytes := 0
		for _, chunk := range chunks {
			if chunk == nil || !onlyResponseShellKeys(chunk, "stdout", "stderr", "outcome") {
				return nil, "shell_call_output.output contains an invalid object"
			}
			for _, field := range []string{"stdout", "stderr"} {
				var text string
				if json.Unmarshal(chunk[field], &text) != nil || !utf8.ValidString(text) {
					return nil, "shell_call_output.output contains invalid " + field
				}
				if len(text) > maxResponseShellOutputBytes-totalBytes {
					return nil, "shell_call_output.output exceeds 8388608 UTF-8 bytes"
				}
				totalBytes += len(text)
			}
			if message := validateResponseShellOutcome(chunk["outcome"]); message != "" {
				return nil, message
			}
		}
		outputs = append(outputs, output)
	}
	return outputs, ""
}

// InspectResponseShellEnvironment validates the supported shell execution
// environments and returns tenant-owned resource references.
func InspectResponseShellEnvironment(value any) (kind, containerID string, fileIDs []string, message string) {
	if value == nil {
		return "", "", nil, ""
	}
	encoded, err := json.Marshal(value)
	if err != nil || bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		if err == nil {
			return "", "", nil, ""
		}
		return "", "", nil, "shell environment must be an object"
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(encoded, &object) != nil || object == nil {
		return "", "", nil, "shell environment must be an object"
	}
	if json.Unmarshal(object["type"], &kind) != nil {
		return "", "", nil, "shell environment.type is required"
	}
	switch kind {
	case "local":
		if !onlyResponseShellKeys(object, "type") {
			return "", "", nil, "shell local environment contains unsupported fields"
		}
	case "container_reference":
		if !onlyResponseShellKeys(object, "type", "container_id") || json.Unmarshal(object["container_id"], &containerID) != nil || !validResponseToolResourceID(containerID) {
			return "", "", nil, "shell container_reference requires a valid container_id"
		}
	case "container_auto":
		if !onlyResponseShellOptionalKeys(object, "type", "memory_limit", "file_ids") {
			return "", "", nil, "shell container_auto contains unsupported fields"
		}
		if raw, supplied := object["memory_limit"]; supplied {
			var limit string
			if json.Unmarshal(raw, &limit) != nil || limit != "1g" && limit != "4g" && limit != "16g" && limit != "64g" {
				return "", "", nil, "shell container_auto.memory_limit must be 1g, 4g, 16g, or 64g"
			}
		}
		if raw, supplied := object["file_ids"]; supplied {
			if json.Unmarshal(raw, &fileIDs) != nil || len(fileIDs) > 20 {
				return "", "", nil, "shell container_auto.file_ids must contain at most 20 IDs"
			}
			seen := make(map[string]struct{}, len(fileIDs))
			for _, id := range fileIDs {
				if !validResponseToolResourceID(id) {
					return "", "", nil, "shell container_auto.file_ids contain an invalid ID"
				}
				if _, duplicate := seen[id]; duplicate {
					return "", "", nil, "shell container_auto.file_ids must be unique"
				}
				seen[id] = struct{}{}
			}
		}
	default:
		return "", "", nil, "shell environment.type is unsupported"
	}
	return kind, containerID, fileIDs, ""
}

func ValidateResponseShellCallAction(raw json.RawMessage) string {
	var action map[string]json.RawMessage
	if json.Unmarshal(raw, &action) != nil || action == nil || !onlyResponseShellOptionalKeys(action, "commands", "max_output_length", "timeout_ms") {
		return "provider shell call has an invalid action"
	}
	var commands []string
	if json.Unmarshal(action["commands"], &commands) != nil || len(commands) == 0 || len(commands) > maxResponseShellItems {
		return "provider shell call action must contain between 1 and 128 commands"
	}
	total := 0
	for _, command := range commands {
		if strings.TrimSpace(command) == "" || !utf8.ValidString(command) || len(command) > maxResponseShellCommandBytes-total {
			return "provider shell call commands must be non-empty and contain at most 1048576 UTF-8 bytes"
		}
		total += len(command)
	}
	if message := validateResponseShellBoundedInt(action["max_output_length"], "provider shell call max_output_length", maxResponseShellOutputBytes); message != "" {
		return message
	}
	if message := validateResponseShellBoundedInt(action["timeout_ms"], "provider shell call timeout_ms", 600000); message != "" {
		return message
	}
	return ""
}

func ValidateResponseShellCaller(raw json.RawMessage, field string) string {
	return validateResponseShellCaller(raw, field)
}

func ValidateResponseShellStatus(raw json.RawMessage, field string) string {
	return validateResponseShellStatus(raw, field)
}

// ResponseShellText returns command and captured-output text that must pass
// provider-output policy checks before delivery.
func ResponseShellText(item ResponseOutputItem) string {
	var value any
	switch item.Type {
	case "shell_call":
		if json.Unmarshal(item.Action, &value) != nil {
			return ""
		}
	case "shell_call_output":
		if json.Unmarshal(item.Output, &value) != nil {
			return ""
		}
	default:
		return ""
	}
	var parts []string
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, child := range typed {
				walk(child)
			}
		case map[string]any:
			for _, key := range []string{"commands", "stdout", "stderr"} {
				if child, present := typed[key]; present {
					walk(child)
				}
			}
		case string:
			parts = append(parts, typed)
		}
	}
	walk(value)
	return strings.Join(parts, "\n")
}

// TransformResponseShellText rewrites only command/stdout/stderr values while
// preserving the validated shell protocol envelope.
func TransformResponseShellText(item *ResponseOutputItem, transform func(string) string) {
	if item == nil || transform == nil {
		return
	}
	var target *json.RawMessage
	switch item.Type {
	case "shell_call":
		target = &item.Action
	case "shell_call_output":
		target = &item.Output
	default:
		return
	}
	var value any
	if json.Unmarshal(*target, &value) != nil {
		return
	}
	transformed := transformResponseShellValue(value, transform)
	if encoded, err := json.Marshal(transformed); err == nil {
		*target = encoded
	}
}

func transformResponseShellValue(value any, transform func(string) string) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = transformResponseShellValue(typed[index], transform)
		}
	case map[string]any:
		for key, child := range typed {
			if key == "commands" || key == "stdout" || key == "stderr" {
				typed[key] = transformResponseShellValue(child, transform)
			}
		}
	case string:
		return transform(typed)
	}
	return value
}

func validateResponseShellAllowedCallers(callers []string) string {
	if len(callers) > 1 {
		return "shell allowed_callers must contain at most 1 value"
	}
	for _, caller := range callers {
		if caller != "direct" {
			return "shell allowed_callers supports only direct"
		}
	}
	return ""
}

func validateResponseShellOutcome(raw json.RawMessage) string {
	var outcome map[string]json.RawMessage
	if json.Unmarshal(raw, &outcome) != nil || outcome == nil {
		return "shell_call_output.output.outcome is invalid"
	}
	var kind string
	if json.Unmarshal(outcome["type"], &kind) != nil {
		return "shell_call_output.output.outcome.type is required"
	}
	switch kind {
	case "timeout":
		if !onlyResponseShellKeys(outcome, "type") {
			return "shell_call_output timeout outcome contains unsupported fields"
		}
	case "exit":
		var code int64
		if !onlyResponseShellKeys(outcome, "type", "exit_code") || json.Unmarshal(outcome["exit_code"], &code) != nil {
			return "shell_call_output exit outcome requires an integer exit_code"
		}
	default:
		return "shell_call_output.output.outcome.type is unsupported"
	}
	return ""
}

func validateResponseShellStatus(raw json.RawMessage, field string) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var status string
	if json.Unmarshal(raw, &status) != nil || status != "in_progress" && status != "completed" && status != "incomplete" {
		return field + " is invalid"
	}
	return ""
}

func validateResponseShellCaller(raw json.RawMessage, field string) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var caller map[string]json.RawMessage
	if json.Unmarshal(raw, &caller) != nil || caller == nil {
		return field + " is invalid"
	}
	var kind string
	if json.Unmarshal(caller["type"], &kind) != nil {
		return field + ".type is required"
	}
	switch kind {
	case "direct":
		if !onlyResponseShellKeys(caller, "type") {
			return field + " contains unsupported fields"
		}
	default:
		return field + ".type supports only direct"
	}
	return ""
}

func validateResponseShellBoundedInt(raw json.RawMessage, field string, maximum int) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var value int
	if json.Unmarshal(raw, &value) != nil || value < 1 || value > maximum {
		return field + " must be between 1 and " + strconv.Itoa(maximum)
	}
	return ""
}

func validResponseShellIdentifier(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && utf8.RuneCountInString(value) <= 512
}

func onlyResponseShellKeys(object map[string]json.RawMessage, allowed ...string) bool {
	return len(object) == len(allowed) && onlyResponseShellOptionalKeys(object, allowed...)
}

func onlyResponseShellOptionalKeys(object map[string]json.RawMessage, allowed ...string) bool {
	for key := range object {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
