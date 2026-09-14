package modules

import "testing"

func TestEndpoint(t *testing.T) {
	tests := []struct {
		source string
		path   string
		want   string
	}{
		{
			source: "https://example.internal:8081",
			path:   "/anonymize",
			want:   "https://example.internal:8081/anonymize",
		},
		{
			source: "https://example.internal:8081/",
			path:   "/anonymize",
			want:   "https://example.internal:8081/anonymize",
		},
		{
			source: "https://example.internal:8081/anonymize",
			path:   "/anonymize",
			want:   "https://example.internal:8081/anonymize",
		},
		{
			source: "https://example.internal:8081/anonymizer?tenant=acme",
			path:   "/anonymize",
			want:   "https://example.internal:8081/anonymizer/anonymize?tenant=acme",
		},
	}
	for _, test := range tests {
		if got := endpoint(test.source, test.path); got != test.want {
			t.Fatalf("endpoint(%q, %q) = %q, want %q", test.source, test.path, got, test.want)
		}
	}
}
