package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Native Schema and JSON Schema use different type names and integer encodings.
// Unknown constraints fail rather than silently weakening the requested schema.
func generateSchema(native map[string]any) (map[string]any, error) {
	return generateSchemaAtDepth(native, 0)
}
func generateSchemaAtDepth(native map[string]any, depth int) (map[string]any, error) {
	if depth >= 64 {
		return nil, errors.New("native schema exceeds depth limit")
	}
	result := map[string]any{}
	nullable := false
	for key, value := range native {
		switch key {
		case "type":
			name, ok := value.(string)
			if !ok {
				return nil, errors.New("invalid schema type")
			}
			name = strings.ToLower(name)
			switch name {
			case "object", "array", "string", "number", "integer", "boolean", "null":
			default:
				return nil, errors.New("unsupported schema type")
			}
			result[key] = name
		case "properties":
			properties, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("invalid schema properties")
			}
			converted := map[string]any{}
			for name, property := range properties {
				schema, ok := property.(map[string]any)
				if !ok {
					return nil, errors.New("invalid property schema")
				}
				item, err := generateSchemaAtDepth(schema, depth+1)
				if err != nil {
					return nil, err
				}
				converted[name] = item
			}
			result[key] = converted
		case "items":
			schema, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("invalid items schema")
			}
			item, err := generateSchemaAtDepth(schema, depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = item
		case "anyOf":
			schemas, ok := value.([]any)
			if !ok || len(schemas) == 0 || len(schemas) > 128 {
				return nil, errors.New("anyOf must contain between 1 and 128 schemas")
			}
			converted := make([]any, 0, len(schemas))
			for _, raw := range schemas {
				schema, ok := raw.(map[string]any)
				if !ok {
					return nil, errors.New("invalid anyOf schema")
				}
				item, err := generateSchemaAtDepth(schema, depth+1)
				if err != nil {
					return nil, err
				}
				converted = append(converted, item)
			}
			result[key] = converted
		case "title", "description", "format", "pattern":
			if _, ok := value.(string); !ok {
				return nil, fmt.Errorf("invalid schema %s", key)
			}
			result[key] = value
		case "enum", "required":
			values, ok := value.([]any)
			if !ok || (key == "enum" && len(values) == 0) {
				return nil, fmt.Errorf("invalid schema %s", key)
			}
			for _, item := range values {
				if _, ok := item.(string); !ok {
					return nil, fmt.Errorf("invalid schema %s entry", key)
				}
			}
			result[key] = value
		case "minimum", "maximum":
			number, ok := value.(float64)
			if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, fmt.Errorf("invalid schema %s", key)
			}
			result[key] = number
		case "minItems", "maxItems", "minLength", "maxLength", "minProperties", "maxProperties":
			number, err := generateSchemaSize(value)
			if err != nil {
				return nil, fmt.Errorf("invalid schema %s: %w", key, err)
			}
			result[key] = json.Number(strconv.FormatInt(number, 10))
		case "nullable":
			var ok bool
			nullable, ok = value.(bool)
			if !ok {
				return nil, errors.New("invalid schema nullable")
			}
		case "default":
			result[key] = value
		case "example":
			result["examples"] = []any{value}
		default:
			return nil, fmt.Errorf("unsupported native schema field %q; use JSON Schema", key)
		}
	}
	for _, pair := range [][2]string{{"minItems", "maxItems"}, {"minLength", "maxLength"}, {"minProperties", "maxProperties"}} {
		low, hasLow := result[pair[0]].(json.Number)
		high, hasHigh := result[pair[1]].(json.Number)
		if hasLow && hasHigh {
			a, _ := low.Int64()
			b, _ := high.Int64()
			if a > b {
				return nil, fmt.Errorf("schema %s exceeds %s", pair[0], pair[1])
			}
		}
	}
	if low, ok := result["minimum"].(float64); ok {
		if high, ok := result["maximum"].(float64); ok && low > high {
			return nil, errors.New("schema minimum exceeds maximum")
		}
	}
	if nullable {
		return map[string]any{"anyOf": []any{result, map[string]any{"type": "null"}}}, nil
	}
	return result, nil
}
func generateSchemaSize(value any) (int64, error) {
	switch value := value.(type) {
	case string:
		number, err := strconv.ParseInt(value, 10, 64)
		if err == nil && number >= 0 {
			return number, nil
		}
	case float64:
		// Larger JSON numbers have already lost integer precision during decoding.
		if value >= 0 && value <= 1<<53-1 && math.Trunc(value) == value {
			return int64(value), nil
		}
	}
	return 0, errors.New("expected a nonnegative int64 string or exact JSON integer")
}
