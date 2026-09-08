package openai

import "testing"

func TestStopSequences(t *testing.T) {
	for _, tc := range []struct {
		value any
		valid bool
	}{
		{nil, true}, {"END", true}, {"\n", true}, {[]string{"a", "b"}, true}, {[]any{"a", "b"}, true},
		{42, false}, {"", false}, {[]any{"a", 42}, false}, {[]string{"a", "b", "c", "d", "e"}, false},
	} {
		if _, valid := StopSequences(tc.value); valid != tc.valid {
			t.Fatalf("%#v valid=%v, want %v", tc.value, valid, tc.valid)
		}
	}
}
