package openai

import (
	"bytes"
	"encoding/json"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	maxResponseApplyPatchItems       = 128
	maxResponseApplyPatchPathBytes   = 4096
	maxResponseApplyPatchDiffBytes   = 4 << 20
	maxResponseApplyPatchOutputBytes = 1 << 20
)

// ResponseApplyPatchCallOutput identifies one client-executed patch returned
// to a Responses continuation.
type ResponseApplyPatchCallOutput struct {
	CallID string
}

// InspectResponseApplyPatchCallOutputs validates apply_patch_call_output input
// items and returns calls that participate in authorization and accounting.
func InspectResponseApplyPatchCallOutputs(input any) ([]ResponseApplyPatchCallOutput, string) {
	if _, ok := input.(string); ok || input == nil {
		return nil, ""
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, "apply_patch_call_output input is invalid"
	}
	var items []json.RawMessage
	if json.Unmarshal(encoded, &items) != nil {
		return nil, ""
	}
	outputs := make([]ResponseApplyPatchCallOutput, 0)
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		var itemType string
		if json.Unmarshal(item["type"], &itemType) != nil || itemType != "apply_patch_call_output" {
			continue
		}
		if len(outputs) >= maxResponseApplyPatchItems {
			return nil, "input must contain at most 128 apply_patch_call_output items"
		}
		if !onlyResponseApplyPatchOptionalKeys(item, "type", "id", "call_id", "status", "output", "caller", "created_by") {
			return nil, "apply_patch_call_output contains an unsupported field"
		}
		var output ResponseApplyPatchCallOutput
		if json.Unmarshal(item["call_id"], &output.CallID) != nil || !validResponseApplyPatchIdentifier(output.CallID) {
			return nil, "apply_patch_call_output.call_id must contain between 1 and 512 characters"
		}
		for _, field := range []string{"id", "created_by"} {
			if rawValue, supplied := item[field]; supplied && !bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
				var value string
				if json.Unmarshal(rawValue, &value) != nil || !validResponseApplyPatchIdentifier(value) {
					return nil, "apply_patch_call_output." + field + " is invalid"
				}
			}
		}
		if message := validateResponseApplyPatchOutputStatus(item["status"]); message != "" {
			return nil, message
		}
		if message := validateResponseApplyPatchCaller(item["caller"], "apply_patch_call_output.caller"); message != "" {
			return nil, message
		}
		if rawOutput, supplied := item["output"]; supplied && !bytes.Equal(bytes.TrimSpace(rawOutput), []byte("null")) {
			var text string
			if json.Unmarshal(rawOutput, &text) != nil || !utf8.ValidString(text) || len(text) > maxResponseApplyPatchOutputBytes {
				return nil, "apply_patch_call_output.output must contain at most 1048576 UTF-8 bytes"
			}
		}
		outputs = append(outputs, output)
	}
	return outputs, ""
}

// ValidateResponseApplyPatchCall validates a provider-generated patch call.
func ValidateResponseApplyPatchCall(item ResponseOutputItem) string {
	return validateResponseApplyPatchCall(item, false)
}

// ValidateResponseApplyPatchCallPartial validates an initial streaming item,
// whose create/update diff may still be empty before delta events arrive.
func ValidateResponseApplyPatchCallPartial(item ResponseOutputItem) string {
	return validateResponseApplyPatchCall(item, true)
}

func validateResponseApplyPatchCall(item ResponseOutputItem, allowEmptyDiff bool) string {
	if !validResponseApplyPatchIdentifier(item.CallID) {
		return "provider apply_patch call has an invalid call_id"
	}
	if item.ID != "" && !validResponseApplyPatchIdentifier(item.ID) {
		return "provider apply_patch call has an invalid id"
	}
	if item.CreatedBy != "" && !validResponseApplyPatchIdentifier(item.CreatedBy) {
		return "provider apply_patch call has an invalid created_by"
	}
	if item.Status != "in_progress" && item.Status != "completed" {
		return "provider apply_patch call has an invalid status"
	}
	if message := validateResponseApplyPatchCaller(item.Caller, "provider apply_patch call caller"); message != "" {
		return message
	}
	var operation map[string]json.RawMessage
	if json.Unmarshal(item.Operation, &operation) != nil || operation == nil {
		return "provider apply_patch call has an invalid operation"
	}
	var kind, targetPath string
	if json.Unmarshal(operation["type"], &kind) != nil || json.Unmarshal(operation["path"], &targetPath) != nil || !validResponseApplyPatchPath(targetPath) {
		return "provider apply_patch call operation has an invalid path"
	}
	switch kind {
	case "create_file", "update_file":
		if !onlyResponseApplyPatchKeys(operation, "type", "path", "diff") {
			return "provider apply_patch call operation contains unsupported fields"
		}
		var diff string
		if json.Unmarshal(operation["diff"], &diff) != nil || (!allowEmptyDiff && diff == "") || !utf8.ValidString(diff) || len(diff) > maxResponseApplyPatchDiffBytes {
			return "provider apply_patch call diff must contain between 1 and 4194304 UTF-8 bytes"
		}
	case "delete_file":
		if !onlyResponseApplyPatchKeys(operation, "type", "path") {
			return "provider apply_patch call delete operation contains unsupported fields"
		}
	default:
		return "provider apply_patch call operation type is unsupported"
	}
	return ""
}

// UpdateResponseApplyPatchDiff appends or replaces a streamed patch diff and
// returns an error message when the aggregate violates the patch contract.
func UpdateResponseApplyPatchDiff(item *ResponseOutputItem, value string, replace bool) string {
	if item == nil || item.Type != "apply_patch_call" || !utf8.ValidString(value) {
		return "provider apply_patch diff event is invalid"
	}
	var operation map[string]json.RawMessage
	if json.Unmarshal(item.Operation, &operation) != nil || operation == nil {
		return "provider apply_patch diff event has no operation"
	}
	var kind, current string
	if json.Unmarshal(operation["type"], &kind) != nil || kind != "create_file" && kind != "update_file" || json.Unmarshal(operation["diff"], &current) != nil {
		return "provider apply_patch diff event has an incompatible operation"
	}
	if !replace {
		if len(value) > maxResponseApplyPatchDiffBytes-len(current) {
			return "provider apply_patch call diff exceeds 4194304 UTF-8 bytes"
		}
		value = current + value
	} else if len(value) > maxResponseApplyPatchDiffBytes {
		return "provider apply_patch call diff exceeds 4194304 UTF-8 bytes"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "provider apply_patch diff event is invalid"
	}
	operation["diff"] = encoded
	item.Operation, err = json.Marshal(operation)
	if err != nil {
		return "provider apply_patch diff event is invalid"
	}
	if replace {
		return ValidateResponseApplyPatchCall(*item)
	}
	return ValidateResponseApplyPatchCallPartial(*item)
}

// ResponseApplyPatchText returns patch paths, diffs and result text that must
// pass provider-output policy checks before delivery.
func ResponseApplyPatchText(item ResponseOutputItem) string {
	switch item.Type {
	case "apply_patch_call":
		var operation map[string]json.RawMessage
		if json.Unmarshal(item.Operation, &operation) != nil {
			return ""
		}
		parts := make([]string, 0, 2)
		for _, field := range []string{"path", "diff"} {
			var value string
			if json.Unmarshal(operation[field], &value) == nil && value != "" {
				parts = append(parts, value)
			}
		}
		return strings.Join(parts, "\n")
	case "apply_patch_call_output":
		var output string
		if json.Unmarshal(item.Output, &output) == nil {
			return output
		}
	}
	return ""
}

// TransformResponseApplyPatchText rewrites patch path, diff and output text
// while preserving the validated protocol envelope and its size limits.
func TransformResponseApplyPatchText(item *ResponseOutputItem, transform func(string) string) {
	if item == nil || transform == nil {
		return
	}
	switch item.Type {
	case "apply_patch_call":
		var operation map[string]json.RawMessage
		if json.Unmarshal(item.Operation, &operation) != nil {
			return
		}
		var targetPath string
		if json.Unmarshal(operation["path"], &targetPath) == nil {
			transformed := transform(targetPath)
			if validResponseApplyPatchPath(transformed) {
				operation["path"], _ = json.Marshal(transformed)
			}
		}
		var diff string
		if json.Unmarshal(operation["diff"], &diff) == nil {
			transformed := transform(diff)
			if transformed != "" && utf8.ValidString(transformed) && len(transformed) <= maxResponseApplyPatchDiffBytes {
				operation["diff"], _ = json.Marshal(transformed)
			}
		}
		if encoded, err := json.Marshal(operation); err == nil {
			item.Operation = encoded
		}
	case "apply_patch_call_output":
		var output string
		if json.Unmarshal(item.Output, &output) == nil {
			transformed := transform(output)
			if utf8.ValidString(transformed) && len(transformed) <= maxResponseApplyPatchOutputBytes {
				item.Output, _ = json.Marshal(transformed)
			}
		}
	}
}

func validateResponseApplyPatchAllowedCallers(callers []string) string {
	if len(callers) > 1 {
		return "apply_patch allowed_callers must contain at most 1 value"
	}
	for _, caller := range callers {
		if caller != "direct" {
			return "apply_patch allowed_callers supports only direct"
		}
	}
	return ""
}

func validateResponseApplyPatchOutputStatus(raw json.RawMessage) string {
	var status string
	if json.Unmarshal(raw, &status) != nil || status != "completed" && status != "failed" {
		return "apply_patch_call_output.status must be completed or failed"
	}
	return ""
}

func validateResponseApplyPatchCaller(raw json.RawMessage, field string) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var caller map[string]json.RawMessage
	if json.Unmarshal(raw, &caller) != nil || caller == nil || !onlyResponseApplyPatchKeys(caller, "type") {
		return field + " is invalid"
	}
	var kind string
	if json.Unmarshal(caller["type"], &kind) != nil || kind != "direct" {
		return field + " supports only direct"
	}
	return ""
}

func validResponseApplyPatchIdentifier(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && utf8.RuneCountInString(value) <= 512
}

func validResponseApplyPatchPath(value string) bool {
	if value == "" || len(value) > maxResponseApplyPatchPathBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned == value && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func onlyResponseApplyPatchKeys(object map[string]json.RawMessage, allowed ...string) bool {
	return len(object) == len(allowed) && onlyResponseApplyPatchOptionalKeys(object, allowed...)
}

func onlyResponseApplyPatchOptionalKeys(object map[string]json.RawMessage, allowed ...string) bool {
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
