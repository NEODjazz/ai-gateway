package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeA2AStoredTaskPreservesNumericMetadata(t *testing.T) {
	const values = `{"id":9007199254740993,"amount":0.1234567890123456789012345,"nested":[{"negative":-9007199254740993,"exponent":1.2300e+24}]}`
	const taskJSON = `{"id":"task","contextId":"context","status":{"state":"TASK_STATE_COMPLETED","message":{"messageId":"reply","taskId":"task","contextId":"context","role":"ROLE_AGENT","parts":[{"text":"done"}],"metadata":{"numeric":` + values + `}}},"artifacts":[{"artifactId":"artifact","parts":[{"text":"done"}]}],"history":[{"messageId":"user","taskId":"task","contextId":"context","role":"ROLE_USER","parts":[{"text":"hi"}]},{"messageId":"reply","taskId":"task","contextId":"context","role":"ROLE_AGENT","parts":[{"text":"done"}]}]}`
	for name, source := range map[string]string{"legacy": taskJSON, "wrapped": `{"task":` + taskJSON + `}`} {
		t.Run(name, func(t *testing.T) {
			task, _, err := decodeA2AStoredTask([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(task.Status.Message.Metadata["numeric"])
			if err != nil {
				t.Fatal(err)
			}
			for _, literal := range []string{"9007199254740993", "0.1234567890123456789012345", "-9007199254740993", "1.2300e+24"} {
				if !strings.Contains(string(encoded), literal) {
					t.Fatalf("stored task rounded numeric metadata: missing %s", literal)
				}
			}
			for _, invalid := range []string{source + ` {}`, source + ` null`, source[:len(source)-1]} {
				if _, _, err := decodeA2AStoredTask([]byte(invalid)); err == nil {
					t.Fatal("stored task accepted malformed or trailing JSON")
				}
			}
		})
	}
}
