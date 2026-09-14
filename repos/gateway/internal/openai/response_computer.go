package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const maxResponseComputerItems = 128

// ResponseComputerCallOutput describes a client-executed computer action whose
// screenshot is being returned to a Responses continuation.
type ResponseComputerCallOutput struct {
	CallID   string
	FileID   string
	ImageURL string
}

// InspectResponseComputerCallOutputs validates computer_call_output input
// items and returns the screenshot references that must be accounted for.
func InspectResponseComputerCallOutputs(input any) ([]ResponseComputerCallOutput, string) {
	if _, ok := input.(string); ok || input == nil {
		return nil, ""
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, "computer_call_output input is invalid"
	}
	var items []json.RawMessage
	if json.Unmarshal(encoded, &items) != nil {
		return nil, ""
	}
	outputs := make([]ResponseComputerCallOutput, 0)
	for _, raw := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		var itemType string
		if json.Unmarshal(item["type"], &itemType) != nil || itemType != "computer_call_output" {
			continue
		}
		if len(outputs) >= maxResponseComputerItems {
			return nil, "input must contain at most 128 computer_call_output items"
		}
		if !onlyResponseComputerKeys(item, "type", "call_id", "output", "id", "status", "acknowledged_safety_checks") {
			return nil, "computer_call_output contains an unsupported field"
		}
		var output ResponseComputerCallOutput
		if json.Unmarshal(item["call_id"], &output.CallID) != nil || strings.TrimSpace(output.CallID) != output.CallID || output.CallID == "" || utf8.RuneCountInString(output.CallID) > 512 {
			return nil, "computer_call_output.call_id must contain between 1 and 512 characters"
		}
		if rawID, supplied := item["id"]; supplied && !bytes.Equal(bytes.TrimSpace(rawID), []byte("null")) {
			var id string
			if json.Unmarshal(rawID, &id) != nil || strings.TrimSpace(id) != id || id == "" || utf8.RuneCountInString(id) > 512 {
				return nil, "computer_call_output.id is invalid"
			}
		}
		if rawStatus, supplied := item["status"]; supplied && !bytes.Equal(bytes.TrimSpace(rawStatus), []byte("null")) {
			var status string
			if json.Unmarshal(rawStatus, &status) != nil || status != "in_progress" && status != "completed" && status != "incomplete" {
				return nil, "computer_call_output.status is invalid"
			}
		}
		var screenshot map[string]json.RawMessage
		if json.Unmarshal(item["output"], &screenshot) != nil || screenshot == nil || !onlyResponseComputerKeys(screenshot, "type", "file_id", "image_url", "detail") {
			return nil, "computer_call_output.output must be a computer_screenshot object"
		}
		var screenshotType string
		if json.Unmarshal(screenshot["type"], &screenshotType) != nil || screenshotType != "computer_screenshot" {
			return nil, "computer_call_output.output.type must be computer_screenshot"
		}
		if rawFile, supplied := screenshot["file_id"]; supplied && !bytes.Equal(bytes.TrimSpace(rawFile), []byte("null")) {
			if json.Unmarshal(rawFile, &output.FileID) != nil || !validResponseToolResourceID(output.FileID) {
				return nil, "computer_call_output.output.file_id is invalid"
			}
		}
		if rawURL, supplied := screenshot["image_url"]; supplied && !bytes.Equal(bytes.TrimSpace(rawURL), []byte("null")) {
			if json.Unmarshal(rawURL, &output.ImageURL) != nil || len(output.ImageURL) > 32<<20 {
				return nil, "computer_call_output.output.image_url is invalid"
			}
			if _, err := ParseDataImageURL(output.ImageURL); err != nil {
				return nil, "computer_call_output.output.image_url must be a valid base64 data image URL"
			}
		}
		if (output.FileID == "") == (output.ImageURL == "") {
			return nil, "computer_call_output.output requires exactly one of file_id or image_url"
		}
		if rawDetail, supplied := screenshot["detail"]; supplied && !bytes.Equal(bytes.TrimSpace(rawDetail), []byte("null")) {
			var detail string
			if json.Unmarshal(rawDetail, &detail) != nil || detail != "auto" && detail != "low" && detail != "high" && detail != "original" {
				return nil, "computer_call_output.output.detail is invalid"
			}
		}
		if message := validateResponseComputerSafetyChecks(item["acknowledged_safety_checks"]); message != "" {
			return nil, message
		}
		outputs = append(outputs, output)
	}
	return outputs, ""
}

func validateResponseComputerSafetyChecks(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var checks []map[string]json.RawMessage
	if json.Unmarshal(raw, &checks) != nil || len(checks) > maxResponseComputerItems {
		return "acknowledged_safety_checks must contain at most 128 objects"
	}
	seen := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if check == nil || !onlyResponseComputerKeys(check, "id", "code", "message") {
			return "acknowledged_safety_checks contains an invalid object"
		}
		var id string
		if json.Unmarshal(check["id"], &id) != nil || strings.TrimSpace(id) != id || id == "" || utf8.RuneCountInString(id) > 512 {
			return "acknowledged_safety_checks.id is invalid"
		}
		if _, duplicate := seen[id]; duplicate {
			return "acknowledged_safety_checks ids must be unique"
		}
		seen[id] = struct{}{}
		for _, field := range []string{"code", "message"} {
			if value, supplied := check[field]; supplied && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				var text string
				if json.Unmarshal(value, &text) != nil || utf8.RuneCountInString(text) > 2048 {
					return "acknowledged_safety_checks contains an invalid " + field
				}
			}
		}
	}
	return ""
}

func onlyResponseComputerKeys(object map[string]json.RawMessage, allowed ...string) bool {
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
