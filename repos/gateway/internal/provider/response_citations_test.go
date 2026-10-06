package provider

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func TestResponsesTopLevelCitationsPreserved(t *testing.T) {
	body := `{"id":"r","status":"completed","citations":["https://example.com/source","http://example.org/path#part"]}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var response openai.ResponseResponse
			var err error
			if stream {
				response, err = streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n"), "m", func(string, string) error { return nil })
			} else {
				response, err = decodeResponseJSON(strings.NewReader(body))
			}
			if err != nil || !slices.Equal(response.Citations, []string{"https://example.com/source", "http://example.org/path#part"}) {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			encoded, err := json.Marshal(response)
			if err != nil || !strings.Contains(string(encoded), `"citations":["https://example.com/source","http://example.org/path#part"]`) {
				t.Fatalf("encoded=%s err=%v", encoded, err)
			}
		})
	}
}

func TestResponsesRejectInvalidTopLevelCitations(t *testing.T) {
	tooMany := make([]string, 1025)
	for index := range tooMany {
		tooMany[index] = "https://example.com/source"
	}
	encoded, err := json.Marshal(tooMany)
	if err != nil {
		t.Fatal(err)
	}
	for _, citations := range []string{
		`["javascript:alert(1)"]`,
		`["https:///missing-host"]`,
		`["https://example.com/` + strings.Repeat("x", 8193) + `"]`,
		string(encoded),
	} {
		document := `{"id":"r","status":"completed","citations":` + citations + `}`
		if _, err := decodeResponseJSON(strings.NewReader(document)); err == nil {
			t.Fatal("invalid JSON citations accepted")
		}
		callbacks := 0
		if _, err := streamResponseData(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":"+document+"}\n\n"), "m", func(string, string) error { callbacks++; return nil }); err == nil || callbacks != 0 {
			t.Fatalf("invalid SSE citations delivered: err=%v callbacks=%d", err, callbacks)
		}
	}
}
