package provider

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestResponseStreamRejectsExcessiveWireBytes(t *testing.T) {
	// Many individually valid short lines evade Scanner's line-size limit.
	padding := strings.Repeat(":"+strings.Repeat("x", 1022)+"\n", 32768)
	forwarded := 0
	_, err := streamResponseData(io.MultiReader(strings.NewReader(padding), strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\"}}\n\n")), "m", func(string, string) error { forwarded++; return nil })
	if err == nil || forwarded != 0 {
		t.Fatalf("oversized stream accepted: err=%v forwarded=%d", err, forwarded)
	}
}

func TestResponseStreamReaderBoundaryAndErrors(t *testing.T) {
	for _, size := range []int{3, 4, 5, 100} {
		source := strings.NewReader(strings.Repeat("x", size))
		reader := &responseStreamReader{source: source, remaining: 4}
		payload, err := io.ReadAll(reader)
		if size <= 4 {
			if err != nil || len(payload) != size {
				t.Fatalf("size=%d bytes=%d err=%v", size, len(payload), err)
			}
		} else {
			if !errors.Is(err, errResponseStreamTooLarge) || size-source.Len() > 5 {
				t.Fatalf("size=%d read=%d err=%v", size, size-source.Len(), err)
			}
			remaining := source.Len()
			_, err = reader.Read(make([]byte, 8))
			if !errors.Is(err, errResponseStreamTooLarge) || source.Len() != remaining {
				t.Fatal("reader continued after limit error")
			}
		}
	}
	failure := errors.New("upstream read failed")
	_, err := io.ReadAll(&responseStreamReader{source: iotest.ErrReader(failure), remaining: 4})
	if !errors.Is(err, failure) {
		t.Fatalf("read error lost: %v", err)
	}
}

func TestSSEReadErrorDoesNotFlushUnfinishedFrame(t *testing.T) {
	failure := errors.New("stream interrupted")
	source := io.MultiReader(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}"), iotest.ErrReader(failure))
	callbacks := 0
	err := scanSSEEvents(source, func(string, string) error { callbacks++; return nil })
	if !errors.Is(err, failure) || callbacks != 0 {
		t.Fatalf("err=%v callbacks=%d", err, callbacks)
	}
}
