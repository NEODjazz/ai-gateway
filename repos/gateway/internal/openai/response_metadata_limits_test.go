package openai

import (
	"fmt"
	"strings"
	"testing"
)

func TestResponseMetadataLimits(t *testing.T) {
	for _, count := range []int{16, 17} {
		m := map[string]string{}
		for i := 0; i < count; i++ {
			m[fmt.Sprint(i)] = "value"
		}
		if got := (ResponseRequest{Metadata: m}).Validate(); (got == "") != (count == 16) {
			t.Fatalf("count=%d error=%q", count, got)
		}
	}
	for _, char := range []string{"a", "界", "😀"} {
		for _, tc := range []struct {
			key, value int
			valid      bool
		}{{64, 512, true}, {65, 512, false}, {64, 513, false}} {
			m := map[string]string{strings.Repeat(char, tc.key): strings.Repeat(char, tc.value)}
			if got := (ResponseRequest{Metadata: m}).Validate(); (got == "") != tc.valid {
				t.Fatalf("char=%s key=%d value=%d error=%q", char, tc.key, tc.value, got)
			}
		}
	}
}
