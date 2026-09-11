package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

const (
	maxVectorSearchFilterDepth = 4
	maxVectorSearchFilterNodes = 32
	maxVectorSearchFilterItems = 16
)

type vectorSearchFilter interface {
	matches(map[string]string) bool
}

type vectorSearchExactFilter map[string]string

func (f vectorSearchExactFilter) matches(attributes map[string]string) bool {
	for key, value := range f {
		if attributes[key] != value {
			return false
		}
	}
	return true
}

type vectorSearchComparisonFilter struct {
	kind   string
	key    string
	values []any
}

func (f vectorSearchComparisonFilter) matches(attributes map[string]string) bool {
	attribute, exists := attributes[f.key]
	if !exists {
		return false
	}
	matched := false
	for _, value := range f.values {
		if compareVectorSearchAttribute(attribute, value, f.kind) {
			matched = true
			break
		}
	}
	if f.kind == "ne" || f.kind == "nin" {
		return !matched
	}
	return matched
}

type vectorSearchCompoundFilter struct {
	kind     string
	children []vectorSearchFilter
}

func (f vectorSearchCompoundFilter) matches(attributes map[string]string) bool {
	if f.kind == "and" {
		for _, child := range f.children {
			if !child.matches(attributes) {
				return false
			}
		}
		return true
	}
	for _, child := range f.children {
		if child.matches(attributes) {
			return true
		}
	}
	return false
}

func parseVectorSearchFilter(raw json.RawMessage) (vectorSearchFilter, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("filters must be an object")
	}
	_, hasType := object["type"]
	_, hasKey := object["key"]
	_, hasValue := object["value"]
	_, hasChildren := object["filters"]
	if !hasType || !hasChildren && !(hasKey && hasValue) {
		var exact map[string]string
		if err := json.Unmarshal(raw, &exact); err != nil {
			return nil, errors.New("filters must map attribute names to string values")
		}
		if message := openai.ValidateMetadata(exact); message != "" {
			return nil, errors.New(message)
		}
		return vectorSearchExactFilter(exact), nil
	}
	nodes := 0
	return parseVectorSearchTypedFilter(raw, 1, &nodes)
}

func parseVectorSearchTypedFilter(raw json.RawMessage, depth int, nodes *int) (vectorSearchFilter, error) {
	*nodes++
	if depth > maxVectorSearchFilterDepth || *nodes > maxVectorSearchFilterNodes {
		return nil, errors.New("filters exceed the maximum depth or node count")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("each filter must be an object")
	}
	var kind string
	if err := json.Unmarshal(object["type"], &kind); err != nil {
		return nil, errors.New("filter type is required")
	}
	if kind == "and" || kind == "or" {
		if !onlyVectorFilterKeys(object, "type", "filters") {
			return nil, errors.New("compound filters accept only type and filters")
		}
		var children []json.RawMessage
		if err := json.Unmarshal(object["filters"], &children); err != nil || len(children) < 1 || len(children) > maxVectorSearchFilterItems {
			return nil, errors.New("compound filters require 1 to 16 children")
		}
		parsed := make([]vectorSearchFilter, len(children))
		for index, child := range children {
			value, err := parseVectorSearchTypedFilter(child, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			parsed[index] = value
		}
		return vectorSearchCompoundFilter{kind: kind, children: parsed}, nil
	}
	if !onlyVectorFilterKeys(object, "type", "key", "value") {
		return nil, errors.New("comparison filters accept only type, key and value")
	}
	var key string
	if err := json.Unmarshal(object["key"], &key); err != nil || key == "" || !utf8.ValidString(key) || utf8.RuneCountInString(key) > 64 {
		return nil, errors.New("filter key must contain 1 to 64 valid UTF-8 characters")
	}
	allowed := map[string]bool{"eq": true, "ne": true, "in": true, "nin": true, "gt": true, "gte": true, "lt": true, "lte": true}
	if !allowed[kind] {
		return nil, errors.New("unsupported filter type")
	}
	values, err := decodeVectorSearchFilterValues(object["value"], kind == "in" || kind == "nin")
	if err != nil {
		return nil, err
	}
	if kind != "eq" && kind != "ne" && kind != "in" && kind != "nin" {
		if _, ok := values[0].(float64); !ok {
			return nil, errors.New("ordered comparison filters require a numeric value")
		}
	}
	return vectorSearchComparisonFilter{kind: kind, key: key, values: values}, nil
}

func decodeVectorSearchFilterValues(raw json.RawMessage, list bool) ([]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("filter value is required")
	}
	values := []any{value}
	if list {
		items, ok := value.([]any)
		if !ok || len(items) < 1 || len(items) > maxVectorSearchFilterItems {
			return nil, errors.New("membership filters require 1 to 16 values")
		}
		values = items
	}
	for index, item := range values {
		switch typed := item.(type) {
		case string:
			if !utf8.ValidString(typed) || utf8.RuneCountInString(typed) > 512 {
				return nil, errors.New("filter string values must contain at most 512 valid UTF-8 characters")
			}
		case bool:
		case json.Number:
			number, err := typed.Float64()
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, errors.New("filter numeric values must be finite")
			}
			values[index] = number
		default:
			return nil, errors.New("filter values must be strings, booleans or numbers")
		}
	}
	return values, nil
}

func compareVectorSearchAttribute(attribute string, value any, kind string) bool {
	switch typed := value.(type) {
	case string:
		return attribute == typed
	case bool:
		parsed, err := strconv.ParseBool(attribute)
		return err == nil && parsed == typed
	case float64:
		parsed, err := strconv.ParseFloat(attribute, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return false
		}
		switch kind {
		case "gt":
			return parsed > typed
		case "gte":
			return parsed >= typed
		case "lt":
			return parsed < typed
		case "lte":
			return parsed <= typed
		default:
			return parsed == typed
		}
	}
	return false
}

func onlyVectorFilterKeys(object map[string]json.RawMessage, keys ...string) bool {
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
	}
	for key := range object {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}
