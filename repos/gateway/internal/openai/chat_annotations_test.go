package openai

import (
	"strings"
	"testing"
)

func TestValidateChatAnnotations(t *testing.T) {
	valid := ChatAnnotation{
		Type: "url_citation",
		URLCitation: &ChatURLCitation{
			StartIndex: 0,
			EndIndex:   6,
			Title:      "Example",
			URL:        "https://example.com/source",
		},
	}
	if err := ValidateChatAnnotations([]ChatAnnotation{valid}); err != nil {
		t.Fatalf("valid citation rejected: %v", err)
	}

	tests := []struct {
		name        string
		annotations []ChatAnnotation
	}{
		{"too many", make([]ChatAnnotation, 129)},
		{"unsupported type", []ChatAnnotation{{Type: "file_citation", URLCitation: valid.URLCitation}}},
		{"negative start", []ChatAnnotation{{Type: valid.Type, URLCitation: &ChatURLCitation{StartIndex: -1, EndIndex: 6, Title: valid.URLCitation.Title, URL: valid.URLCitation.URL}}}},
		{"reversed offsets", []ChatAnnotation{{Type: valid.Type, URLCitation: &ChatURLCitation{StartIndex: 7, EndIndex: 6, Title: valid.URLCitation.Title, URL: valid.URLCitation.URL}}}},
		{"empty title", []ChatAnnotation{{Type: valid.Type, URLCitation: &ChatURLCitation{EndIndex: 6, URL: valid.URLCitation.URL}}}},
		{"oversized title", []ChatAnnotation{{Type: valid.Type, URLCitation: &ChatURLCitation{EndIndex: 6, Title: strings.Repeat("я", 2049), URL: valid.URLCitation.URL}}}},
		{"unsafe scheme", []ChatAnnotation{{Type: valid.Type, URLCitation: &ChatURLCitation{EndIndex: 6, Title: valid.URLCitation.Title, URL: "javascript:alert(1)"}}}},
		{"relative URL", []ChatAnnotation{{Type: valid.Type, URLCitation: &ChatURLCitation{EndIndex: 6, Title: valid.URLCitation.Title, URL: "/source"}}}},
		{"oversized URL", []ChatAnnotation{{Type: valid.Type, URLCitation: &ChatURLCitation{EndIndex: 6, Title: valid.URLCitation.Title, URL: "https://example.com/" + strings.Repeat("a", 8192)}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateChatAnnotations(test.annotations); err == nil {
				t.Fatal("invalid annotations accepted")
			}
		})
	}
}

func TestValidateChatSourceAnnotations(t *testing.T) {
	documentIndex := 1
	valid := ChatAnnotation{Type: "source_citation", SourceCitation: &ChatSourceCitation{
		StartIndex: 2, EndIndex: 8, Title: "Report", SourceContent: []string{"source excerpt"},
		LocationType: "document_page", DocumentIndex: &documentIndex, LocationStart: 3, LocationEnd: 4,
	}}
	if err := ValidateChatAnnotations([]ChatAnnotation{valid}); err != nil {
		t.Fatalf("valid source citation rejected: %v", err)
	}

	searchIndex := 0
	tests := []ChatAnnotation{
		{Type: "source_citation", SourceCitation: &ChatSourceCitation{LocationType: "document_page"}},
		{Type: "source_citation", SourceCitation: &ChatSourceCitation{Title: "Report", LocationType: "document_page", DocumentIndex: &documentIndex, SearchResultIndex: &searchIndex}},
		{Type: "source_citation", SourceCitation: &ChatSourceCitation{Title: "Report", LocationType: "search_result", DocumentIndex: &documentIndex}},
		{Type: "source_citation", SourceCitation: &ChatSourceCitation{Title: "Report", LocationType: "unknown", DocumentIndex: &documentIndex}},
		{Type: "source_citation", SourceCitation: &ChatSourceCitation{Title: "Report", LocationType: "document_char", DocumentIndex: &documentIndex, LocationStart: 2, LocationEnd: 1}},
		{Type: "source_citation", SourceCitation: &ChatSourceCitation{Title: "Report", LocationType: "document_chunk", DocumentIndex: &documentIndex, SourceContent: []string{strings.Repeat("x", 65537)}}},
	}
	for index, annotation := range tests {
		if err := ValidateChatAnnotations([]ChatAnnotation{annotation}); err == nil {
			t.Fatalf("invalid source citation %d accepted", index)
		}
	}
}
