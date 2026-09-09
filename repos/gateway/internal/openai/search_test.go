package openai

import "testing"

func TestSearchRequestValidationAndAccounting(t *testing.T) {
	maximum := 20
	pageTokens := 4096
	request := SearchRequest{
		SearchToolName: "web-search", Query: []any{"latest release", "security advisory"},
		MaxResults: &maximum, SearchDomainFilter: []string{"example.com", "docs.example.org"},
		MaxTokensPerPage: &pageTokens, Country: "US",
	}
	if message := request.Validate(); message != "" {
		t.Fatal(message)
	}
	if model, err := request.RoutingModel(); err != nil || model != "web-search" {
		t.Fatalf("model=%q err=%v", model, err)
	}
	if tokens := SearchReserveTokens(request); tokens <= 0 {
		t.Fatalf("tokens=%d", tokens)
	}
	if units := request.SearchUnits(); units != 2 {
		t.Fatalf("search units=%d", units)
	}
	request.SetQueries([]string{"redacted one", "redacted two"})
	queries, err := request.Queries()
	if err != nil || len(queries) != 2 || queries[1] != "redacted two" {
		t.Fatalf("queries=%v err=%v", queries, err)
	}
}

func TestSearchRequestRejectsInvalidInputs(t *testing.T) {
	one, tooMany, tooLarge := 1, 21, 4097
	tests := []SearchRequest{
		{Model: "search", SearchToolName: "other", Query: "query"},
		{Model: "search", Query: ""},
		{Model: "search", Query: []any{}},
		{Model: "search", Query: []any{"ok", 1}},
		{Model: "search", Query: "query", MaxResults: &tooMany},
		{Model: "search", Query: "query", MaxResults: &one, SearchDomainFilter: []string{"https://example.com"}},
		{Model: "search", Query: "query", MaxTokensPerPage: &tooLarge},
		{Model: "search", Query: "query", Country: "us"},
	}
	for index, request := range tests {
		if message := request.Validate(); message == "" {
			t.Fatalf("case %d accepted: %+v", index, request)
		}
	}
}
