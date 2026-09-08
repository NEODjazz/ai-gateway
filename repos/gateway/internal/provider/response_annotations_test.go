package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestResponsesAnnotationRoundTrip(t *testing.T) {
	const annotation = `{"type":"url_citation","url":"https://example.com","title":"Source","start_index":0,"end_index":4}`
	body := `{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":"text","annotations":[` + annotation + `]}]}]}`
	response, err := decodeResponseJSON(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(response)
	if !strings.Contains(string(encoded), `"annotations":[`+annotation+`]`) {
		t.Fatal("JSON annotations lost")
	}
	stream := "data: {\"type\":\"response.output_text.annotation.added\",\"output_index\":1,\"content_index\":2,\"annotation_index\":0,\"item_id\":\"msg\",\"annotation\":" + annotation + "}\n\n" + responseTestTerminal
	response, err = streamResponseData(strings.NewReader(stream), "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(response)
	if len(response.Output) != 2 || response.Output[1].ID != "msg" || !strings.Contains(string(encoded), `"annotations":[`) {
		t.Fatalf("stream annotation lost: %s", encoded)
	}
}

func TestResponsesAnnotationIndexBounds(t *testing.T) {
	for _, field := range []string{"content_index", "annotation_index"} {
		for _, index := range []string{"-1", "128", "null", "0.5"} {
			stream := fmt.Sprintf("data: {\"type\":\"response.output_text.annotation.added\",%q:%s,\"annotation\":null}\n\n", field, index) + responseTestTerminal
			calls := 0
			_, err := streamResponseData(strings.NewReader(stream), "m", func(string, string) error { calls++; return nil })
			if err == nil || calls != 0 {
				t.Fatalf("field=%s index=%s err=%v calls=%d", field, index, err, calls)
			}
		}
	}
}
