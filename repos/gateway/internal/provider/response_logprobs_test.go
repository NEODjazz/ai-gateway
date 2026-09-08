package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponseLogprobsSnapshots(t *testing.T) {
	const part = `{"type":"output_text","text":"A","logprobs":[{"token":"A","logprob":-0.1,"bytes":[65],"top_logprobs":[{"token":"B","logprob":-2,"bytes":[66]}]}]}`
	const body = `{"id":"r","status":"completed","output":[{"type":"message","content":[` + part + `]}]}`
	for _, stream := range []bool{false, true} {
		response, err := decodeResponseJSON(strings.NewReader(body))
		if stream {
			response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.content_part.done\",\"part\":"+part+"}\n\n"+responseTestTerminal), "m", nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(response.Output[0].Content[0])
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if _, ok := got["logprobs"]; !ok {
			t.Fatalf("stream=%v: logprobs lost", stream)
		}
	}
}

func TestResponseLogprobsStreamReplacement(t *testing.T) {
	for _, done := range []string{`[]`, `[{"token":"B","logprob":-2}]`} {
		wire := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"A\",\"logprobs\":[{\"token\":\"A\",\"logprob\":-1}]}\n\n"
		result, err := streamResponseData(strings.NewReader(wire+responseTestTerminal), "m", nil)
		if err != nil || len(result.Output[0].Content[0].Logprobs) != 1 {
			t.Fatalf("delta: %v %v", result, err)
		}
		wire += "data: {\"type\":\"response.output_text.done\",\"text\":\"B\",\"logprobs\":" + done + "}\n\n"
		result, err = streamResponseData(strings.NewReader(wire+responseTestTerminal), "m", nil)
		if err != nil {
			t.Fatal(err)
		}
		got := result.Output[0].Content[0].Logprobs
		if done == `[]` {
			if len(got) != 0 {
				t.Fatal("empty done did not clear probabilities")
			}
		} else if len(got) != 1 || got[0].Token != "B" {
			t.Fatalf("done=%v", got)
		}
	}
	for _, invalid := range []string{`{}`, `"invalid"`, `[{"logprob":"bad"}]`} {
		called := false
		_, err := streamResponseData(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"logprobs\":"+invalid+"}\n\n"+responseTestTerminal), "m", func(string, string) error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("invalid=%s err=%v called=%v", invalid, err, called)
		}
	}
}
