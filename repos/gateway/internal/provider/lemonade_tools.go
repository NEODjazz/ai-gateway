package provider

import (
	"encoding/json"
	"math"
)

func validateLemonadeToolSchema(schema any) error {
	if schema == nil {
		return nil
	}
	payload, err := json.Marshal(schema)
	if err != nil {
		return lemonadeInvalidParameter("tools.function.parameters", "tool schema cannot be encoded")
	}
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return err
	}
	if _, ok := value.(map[string]any); !ok {
		return lemonadeInvalidParameter("tools.function.parameters", "tool parameters must be a JSON Schema object")
	}
	return validateLemonadeSchemaBounds(value, 0)
}

func validateLemonadeSchemaBounds(schema any, depth int) error {
	if depth > 64 {
		return lemonadeInvalidParameter("tools.function.parameters", "tool schema nesting exceeds 64 levels")
	}
	if values, ok := schema.([]any); ok {
		for _, value := range values {
			if err := validateLemonadeSchemaBounds(value, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	object, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	// Lemonade's llama.cpp backend removes bounds at these thresholds as a
	// grammar workaround. Reject them rather than silently weaken the schema.
	for _, field := range []string{"minLength", "maxLength", "minItems", "maxItems"} {
		if raw, exists := object[field]; exists {
			limit, numeric := raw.(float64)
			threshold := float64(2000)
			if field == "minItems" || field == "maxItems" {
				threshold++
			}
			if !numeric || limit < 0 || math.Trunc(limit) != limit {
				return lemonadeInvalidParameter("tools.function.parameters", "schema length and item bounds must be non-negative integers")
			}
			if limit >= threshold {
				return lemonadeInvalidParameter("tools.function.parameters", "schema bound exceeds Lemonade's supported grammar limit")
			}
		}
	}
	for _, field := range []string{"additionalItems", "additionalProperties", "allOf", "anyOf", "contains", "contentSchema", "else", "if", "items", "not", "oneOf", "prefixItems", "propertyNames", "then", "unevaluatedItems", "unevaluatedProperties"} {
		if err := validateLemonadeSchemaBounds(object[field], depth+1); err != nil {
			return err
		}
	}
	for _, field := range []string{"$defs", "definitions", "dependencies", "dependentSchemas", "patternProperties", "properties"} {
		if children, ok := object[field].(map[string]any); ok {
			for _, child := range children {
				if err := validateLemonadeSchemaBounds(child, depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
