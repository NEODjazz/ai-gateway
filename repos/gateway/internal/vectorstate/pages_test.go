package vectorstate

import (
	"strings"
	"testing"
)

func TestPageQueryValidation(t *testing.T) {
	valid := PageQuery{Limit: 25, SortBy: "created_at", SortOrder: "desc"}
	if !valid.Valid() {
		t.Fatal("default query rejected")
	}
	for _, change := range []func(*PageQuery){
		func(q *PageQuery) { q.Limit = 201 }, func(q *PageQuery) { q.Limit = 0 },
		func(q *PageQuery) { q.Offset = -1 }, func(q *PageQuery) { q.Offset = 1_000_001 },
		func(q *PageQuery) { q.SortBy = "name;SELECT 1" }, func(q *PageQuery) { q.SortOrder = "random" },
		func(q *PageQuery) { q.Status = "unknown" }, func(q *PageQuery) { q.Search = "\x00" },
		func(q *PageQuery) { q.Search = strings.Repeat("я", 257) }, func(q *PageQuery) { q.Search = string([]byte{0xff}) },
	} {
		q := valid
		change(&q)
		if q.Valid() {
			t.Errorf("invalid query accepted: %+v", q)
		}
	}
}
