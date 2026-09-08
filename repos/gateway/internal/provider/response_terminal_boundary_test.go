package provider

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestResponseStreamStopsAtTerminalOutcome(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed"} {
		t.Run(status, func(t *testing.T) {
			terminal := fmt.Sprintf("data: {\"type\":\"response.%s\",\"response\":{\"id\":\"original\",\"status\":%q,\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"total_tokens\":5}}}\n\n", status, status)
			for _, tail := range []string{
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"replacement\",\"usage\":{\"total_tokens\":999}}}\n\n",
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"late\"}\n\n",
				"data: invalid-json\n\n",
			} {
				calls := 0
				result, err := streamResponseData(strings.NewReader(terminal+tail), "m", func(string, string) error { calls++; return nil })
				if err != nil || calls != 1 || result.ID != "original" || result.Status != status || result.Usage.TotalTokens != 5 || result.OutputText != "" {
					t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
				}
			}
			readFailure := errors.New("read after terminal")
			result, err := streamResponseData(io.MultiReader(strings.NewReader(terminal), iotest.ErrReader(readFailure)), "m", nil)
			if err != nil || result.ID != "original" {
				t.Fatalf("read beyond terminal: result=%+v err=%v", result, err)
			}
			writeFailure := errors.New("terminal write failed")
			_, err = streamResponseData(strings.NewReader(terminal), "m", func(string, string) error { return writeFailure })
			if !errors.Is(err, writeFailure) {
				t.Fatalf("write error lost: %v", err)
			}
		})
	}
}
