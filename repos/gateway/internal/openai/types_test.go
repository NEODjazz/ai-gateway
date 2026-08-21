package openai

import (
	"encoding/json"
	"testing"
)

func TestFunctionCallAcceptsStringAndObjectArguments(t *testing.T) {
	for name, payload := range map[string]string{
		"string": `{"name":"weather.get","arguments":"{\"city\":\"Moscow\"}"}`,
		"object": `{"name":"weather.get","arguments":{"city":"Moscow"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var call FunctionCall
			if err := json.Unmarshal([]byte(payload), &call); err != nil {
				t.Fatal(err)
			}
			if call.Name != "weather.get" || call.Arguments != `{"city":"Moscow"}` {
				t.Fatalf("unexpected function call: %+v", call)
			}
		})
	}
}
