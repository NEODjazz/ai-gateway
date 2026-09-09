package openai

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	MaxSearchQueries       = 20
	MaxSearchQueryBytes    = 16 << 10
	MaxSearchResults       = 20
	MaxSearchDomains       = 20
	MaxSearchTokensPerPage = 4096
)

type SearchRequest struct {
	Provider           string   `json:"provider,omitempty"`
	Model              string   `json:"model,omitempty"`
	SearchToolName     string   `json:"search_tool_name,omitempty"`
	Query              any      `json:"query"`
	MaxResults         *int     `json:"max_results,omitempty"`
	SearchDomainFilter []string `json:"search_domain_filter,omitempty"`
	MaxTokensPerPage   *int     `json:"max_tokens_per_page,omitempty"`
	Country            string   `json:"country,omitempty"`
}

type SearchResponse struct {
	Object  string         `json:"object"`
	Results []SearchResult `json:"results"`
	Usage   Usage          `json:"-"`
	Model   string         `json:"-"`
}

type SearchResult struct {
	Title       string `json:"title,omitempty"`
	URL         string `json:"url"`
	Snippet     string `json:"snippet,omitempty"`
	Date        string `json:"date,omitempty"`
	LastUpdated string `json:"last_updated,omitempty"`
}

func (r SearchRequest) RoutingModel() (string, error) {
	model := strings.TrimSpace(r.Model)
	tool := strings.TrimSpace(r.SearchToolName)
	if model != "" && tool != "" && model != tool {
		return "", errors.New("model and search_tool_name must match when both are provided")
	}
	if model != "" {
		return model, nil
	}
	if tool != "" {
		return tool, nil
	}
	return "", errors.New("model or search_tool_name is required")
}

func (r *SearchRequest) SetRoutingModel(model string) {
	if r.Model != "" {
		r.Model = model
	}
	if r.SearchToolName != "" {
		r.SearchToolName = model
	}
}

func (r SearchRequest) Queries() ([]string, error) {
	switch value := r.Query.(type) {
	case string:
		if !validSearchQuery(value) {
			return nil, errors.New("query must be a non-empty UTF-8 string within 16 KiB")
		}
		return []string{value}, nil
	case []any:
		if len(value) == 0 || len(value) > MaxSearchQueries {
			return nil, errors.New("query list must contain between 1 and 20 strings")
		}
		queries := make([]string, len(value))
		for index, item := range value {
			query, ok := item.(string)
			if !ok || !validSearchQuery(query) {
				return nil, errors.New("query list must contain non-empty UTF-8 strings within 16 KiB")
			}
			queries[index] = query
		}
		return queries, nil
	case []string:
		if len(value) == 0 || len(value) > MaxSearchQueries {
			return nil, errors.New("query list must contain between 1 and 20 strings")
		}
		queries := append([]string(nil), value...)
		for _, query := range queries {
			if !validSearchQuery(query) {
				return nil, errors.New("query list must contain non-empty UTF-8 strings within 16 KiB")
			}
		}
		return queries, nil
	default:
		return nil, errors.New("query must be a string or a list of strings")
	}
}

func (r *SearchRequest) SetQueries(queries []string) {
	if _, scalar := r.Query.(string); scalar && len(queries) == 1 {
		r.Query = queries[0]
		return
	}
	r.Query = append([]string(nil), queries...)
}

func (r SearchRequest) Validate() string {
	if _, err := r.RoutingModel(); err != nil {
		return err.Error()
	}
	if _, err := r.Queries(); err != nil {
		return err.Error()
	}
	if r.MaxResults != nil && (*r.MaxResults < 1 || *r.MaxResults > MaxSearchResults) {
		return "max_results must be between 1 and 20"
	}
	if len(r.SearchDomainFilter) > MaxSearchDomains {
		return "search_domain_filter must contain at most 20 domains"
	}
	for _, domain := range r.SearchDomainFilter {
		if !validSearchDomain(domain) {
			return "search_domain_filter contains an invalid domain"
		}
	}
	if r.MaxTokensPerPage != nil && (*r.MaxTokensPerPage < 1 || *r.MaxTokensPerPage > MaxSearchTokensPerPage) {
		return "max_tokens_per_page must be between 1 and 4096"
	}
	if r.Country != "" && (len(r.Country) != 2 || r.Country != strings.ToUpper(r.Country) || r.Country[0] < 'A' || r.Country[0] > 'Z' || r.Country[1] < 'A' || r.Country[1] > 'Z') {
		return "country must be a two-letter uppercase code"
	}
	return ""
}

func SearchReserveTokens(r SearchRequest) int {
	queries, _ := r.Queries()
	return ReserveTokens(EstimateContextTokens(struct {
		Queries []string
		Domains []string
		Country string
	}{queries, r.SearchDomainFilter, r.Country}), 0)
}

func (r SearchRequest) SearchUnits() int {
	queries, err := r.Queries()
	if err != nil {
		return 0
	}
	return len(queries)
}

func validSearchQuery(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && len(value) <= MaxSearchQueryBytes
}

func validSearchDomain(value string) bool {
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "/:@?#") {
		return false
	}
	parsed, err := url.Parse("https://" + value)
	if err != nil || parsed.Hostname() != value || parsed.Port() != "" {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}
