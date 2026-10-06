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

func TestResponsesRejectInvalidSnapshotAnnotationsBeforeDelivery(t *testing.T) {
	annotations := strings.Repeat(`null,`, maxResponseStreamContentParts) + `null`
	for name, value := range map[string]string{
		"scalar":   `"invalid"`,
		"too many": annotations,
	} {
		t.Run(name, func(t *testing.T) {
			part := `{"type":"output_text","text":"text","annotations":[` + value + `]}`
			document := `{"id":"r","output":[{"type":"message","content":[` + part + `]}]}`
			if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
				t.Fatalf("invalid JSON annotations accepted: %s", name)
			}
			calls := 0
			wire := "data: {\"type\":\"response.content_part.done\",\"part\":" + part + "}\n\n" + responseTestTerminal
			if _, err := streamResponseData(strings.NewReader(wire), "m", func(string, string) error { calls++; return nil }); err == nil || calls != 0 {
				t.Fatalf("invalid SSE annotations delivered: err=%v calls=%d", err, calls)
			}
		})
	}
}
