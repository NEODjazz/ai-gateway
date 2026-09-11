package vectorstate

import (
	"math"
	"testing"
)

func TestValidateAttributesAcceptsScalarsAndRejectsNestedOrNonFiniteValues(t *testing.T) {
	if message := ValidateAttributes(map[string]any{"text": "value", "number": float64(2), "enabled": true}); message != "" {
		t.Fatalf("valid attributes: %s", message)
	}
	for _, attributes := range []map[string]any{
		{"nested": map[string]any{"bad": true}},
		{"array": []any{"bad"}},
		{"number": math.Inf(1)},
		{"": "empty key"},
	} {
		if message := ValidateAttributes(attributes); message == "" {
			t.Fatalf("accepted invalid attributes=%v", attributes)
		}
	}
}
