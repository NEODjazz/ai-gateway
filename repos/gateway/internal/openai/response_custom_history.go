package openai

import "encoding/json"

// InspectResponseCustomToolHistory finds custom calls and outputs in a
// Responses continuation so routing and authorization can account for them.
func InspectResponseCustomToolHistory(input any) (names []string, hasCustom bool, message string) {
	return inspectResponseNamedToolHistory(input, "custom_tool_call", "custom_tool_call_output")
}

// InspectResponseFunctionToolHistory finds function calls and outputs in a
// Responses continuation so routing and authorization can account for them.
func InspectResponseFunctionToolHistory(input any) (names []string, hasFunction bool, message string) {
	return inspectResponseNamedToolHistory(input, "function_call", "function_call_output")
}

func inspectResponseNamedToolHistory(input any, callType, outputType string) (names []string, hasTool bool, message string) {
	if input == nil {
		return nil, false, ""
	}
	if _, ok := input.(string); ok {
		return nil, false, ""
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, false, "tool history is invalid"
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
		if kind != callType && kind != outputType {
			continue
		}
		hasTool = true
		var callID string
		if json.Unmarshal(item["call_id"], &callID) != nil || callID == "" || len(callID) > 512 {
			return nil, false, "tool history requires a valid call_id"
		}
		if kind == outputType {
			continue
		}
		var name string
		if json.Unmarshal(item["name"], &name) != nil || !chatFunctionName.MatchString(name) {
			return nil, false, callType + " requires a valid name"
		}
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names, hasTool, ""
}
