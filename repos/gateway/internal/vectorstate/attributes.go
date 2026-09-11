package vectorstate

import (
	"encoding/json"
	"math"
	"unicode/utf8"
)

// ValidateAttributes enforces the durable scalar attribute contract.
func ValidateAttributes(attributes map[string]any) string {
	if len(attributes) > 16 {
		return "attributes must contain at most 16 entries"
	}
	for key, value := range attributes {
		if key == "" || !utf8.ValidString(key) || utf8.RuneCountInString(key) > 64 {
			return "attribute keys must contain 1 to 64 valid UTF-8 characters"
		}
		switch typed := value.(type) {
		case string:
			if !utf8.ValidString(typed) || utf8.RuneCountInString(typed) > 512 {
				return "attribute string values must contain at most 512 valid UTF-8 characters"
			}
		case bool:
		case float64:
			if math.IsNaN(typed) || math.IsInf(typed, 0) {
				return "attribute numeric values must be finite"
			}
		case json.Number:
			parsed, err := typed.Float64()
			if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
				return "attribute numeric values must be finite"
			}
		default:
			return "attribute values must be strings, numbers or booleans"
		}
	}
	return ""
}
