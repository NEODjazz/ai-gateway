package vectorstate

import (
	"context"
	"strings"
	"unicode/utf8"
)

// PageQuery describes an owner-scoped directory, independent of cursor listing.
type PageQuery struct {
	Offset    int
	Limit     int
	Search    string
	Status    string
	SortBy    string
	SortOrder string
}

func (q PageQuery) Valid() bool {
	if q.Offset < 0 || q.Offset > 1_000_000 || q.Limit < 1 || q.Limit > 200 || !utf8.ValidString(q.Search) || utf8.RuneCountInString(q.Search) > 256 || strings.ContainsRune(q.Search, 0) {
		return false
	}
	if q.Status != "" && q.Status != "completed" && q.Status != "expired" {
		return false
	}
	if q.SortOrder != "asc" && q.SortOrder != "desc" {
		return false
	}
	switch q.SortBy {
	case "name", "id", "created_at", "last_active_at":
		return true
	}
	return false
}

type PageStore interface {
	ListVectorStorePage(context.Context, string, PageQuery) ([]VectorStore, int, error)
}
