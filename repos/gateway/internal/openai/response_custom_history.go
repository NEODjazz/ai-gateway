package openai

import (
	"encoding/json"
)

// InspectResponseCustomToolHistory finds custom calls and outputs in a
// Responses continuation so routing and authorization can account for them.
func InspectResponseCustomToolHistory(input any) (names []string, hasCustom bool, message string) {
	if input == nil {
		return nil, false, ""
	}
	if _, ok := input.(string); ok {
		return nil, false, ""
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, false, "custom tool history is invalid"
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(encoded, &items) != nil {
		return nil, false, ""
	}
	seen := make(map[string]bool)
	for _, item := range items {
		var kind string
		if json.Unmarshal(item["type"], &kind) != nil {
			continue
		}
		if kind != "custom_tool_call" && kind != "custom_tool_call_output" {
			continue
		}
		hasCustom = true
		var callID string
		if json.Unmarshal(item["call_id"], &callID) != nil || callID == "" || len(callID) > 512 {
			return nil, false, "custom tool history requires a valid call_id"
		}
		if kind == "custom_tool_call_output" {
			continue
		}
		var name string
		if json.Unmarshal(item["name"], &name) != nil || !chatFunctionName.MatchString(name) {
			return nil, false, "custom_tool_call requires a valid name"
		}
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names, hasCustom, ""
}
