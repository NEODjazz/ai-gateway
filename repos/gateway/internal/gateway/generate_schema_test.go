package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestGenerateSchemaPreservesConstraints(t *testing.T) {
	for _, tc := range []struct {
		name, raw      string
		valid, invalid []any
	}{
		{"nullable enum", `{"type":"STRING","enum":["ok"],"nullable":true}`, []any{nil, "ok"}, []any{"other", float64(1)}},
		{"array bounds", `{"type":"ARRAY","items":{"type":"INTEGER"},"minItems":"1","maxItems":"2"}`, []any{[]any{float64(1)}}, []any{[]any{}, []any{float64(1), float64(2), float64(3)}, []any{"bad"}}},
		{"string bounds", `{"type":"STRING","minLength":"2","maxLength":3,"pattern":"^a"}`, []any{"ab", "abc"}, []any{"a", "abcd", "xx"}},
		{"object bounds", `{"type":"OBJECT","minProperties":"1","maxProperties":"2","required":["a"]}`, []any{map[string]any{"a": true}}, []any{map[string]any{}, map[string]any{"b": true}, map[string]any{"a": true, "b": true, "c": true}}},
		{"anyOf", `{"anyOf":[{"type":"INTEGER","minimum":1,"maximum":3},{"type":"STRING","enum":["ok"]}]}`, []any{float64(2), "ok"}, []any{nil, float64(4), "bad"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var native map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &native); err != nil {
				t.Fatal(err)
			}
			converted, err := generateSchema(native)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(converted)
			if err != nil {
				t.Fatal(err)
			}
			var schema openapi3.Schema
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatal(err)
			}
			for _, value := range tc.valid {
				if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err != nil {
					t.Fatalf("valid value %v rejected: %v; schema %s", value, err, encoded)
				}
			}
			for _, value := range tc.invalid {
				if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err == nil {
					t.Fatalf("constraint weakened for %v: %s", value, encoded)
				}
			}
		})
	}
}
func TestGenerateSchemaRejectsMalformedConstraints(t *testing.T) {
	for _, raw := range []string{
		`{"nullable":"true"}`, `{"minItems":"-1"}`, `{"maxLength":1.5}`, `{"maxItems":9007199254740992}`, `{"maxItems":"9223372036854775808"}`,
		`{"minItems":"3","maxItems":"2"}`, `{"minimum":3,"maximum":2}`, `{"minimum":"1"}`, `{"required":[1]}`, `{"enum":[]}`, `{"pattern":3}`, `{"anyOf":[]}`, `{"anyOf":[true]}`, `{"anyOf":[{"unsupported":true}]}`,
	} {
		var native map[string]any
		if err := json.Unmarshal([]byte(raw), &native); err != nil {
			t.Fatal(err)
		}
		if _, err := generateSchema(native); err == nil {
			t.Fatalf("accepted invalid schema %s", raw)
		}
	}
}
func TestGenerateSchemaDepthAndExactInteger(t *testing.T) {
	raw := strings.Repeat(`{"type":"ARRAY","items":`, 64) + `{"type":"STRING"}` + strings.Repeat("}", 64)
	var native map[string]any
	if err := json.Unmarshal([]byte(raw), &native); err != nil {
		t.Fatal(err)
	}
	if _, err := generateSchema(native); err == nil {
		t.Fatal("unbounded schema recursion")
	}
	native = map[string]any{"type": "ARRAY", "maxItems": "9223372036854775807"}
	converted, err := generateSchema(native)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"maxItems":9223372036854775807`) {
		t.Fatalf("integer precision lost: %s", encoded)
	}
}
