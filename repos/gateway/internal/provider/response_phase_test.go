package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponsePreservesMessagePhase(t *testing.T) {
	const item = `{"id":"reason","type":"message","phase":"commentary","content":[]}`
	const body = `{"id":"r","status":"completed","output":[` + item + `]}`
	decoders := map[string]func() (openai.ResponseResponse, error){
		"json": func() (openai.ResponseResponse, error) { return decodeResponseJSON(strings.NewReader(body)) },
		"stream item": func() (openai.ResponseResponse, error) {
			return streamResponseData(strings.NewReader("data: {\"type\":\"response.output_item.done\",\"item\":"+item+"}\n\n"+responseTestTerminal), "m", nil)
		},
		"stream outcome": func() (openai.ResponseResponse, error) {
			return streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n"), "m", nil)
		},
	}
	for name, decode := range decoders {
		t.Run(name, func(t *testing.T) {
			result, err := decode()
			if err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var output struct {
				Output []map[string]any `json:"output"`
			}
			if err := json.Unmarshal(payload, &output); err != nil {
				t.Fatal(err)
			}
			if len(output.Output) != 1 || output.Output[0]["phase"] != "commentary" {
				t.Fatal("message phase was lost")
			}
		})
	}
}
