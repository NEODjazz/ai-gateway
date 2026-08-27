package gateway

import (
	"net/http"
	"strconv"
	"strings"
)

type resourceQuery struct {
	Search string
	Sort   string
	Order  string
	Limit  int
	Offset int
}

func parseResourceQuery(r *http.Request, allowedSort map[string]bool) (resourceQuery, bool) {
	values := r.URL.Query()
	query := resourceQuery{Search: strings.ToLower(strings.TrimSpace(values.Get("search"))), Sort: strings.TrimSpace(values.Get("sort")), Order: strings.ToLower(strings.TrimSpace(values.Get("order"))), Limit: 50}
	if query.Order == "" {
		query.Order = "asc"
	}
	if query.Order != "asc" && query.Order != "desc" {
		return resourceQuery{}, false
	}
	if query.Sort != "" && !allowedSort[query.Sort] {
		return resourceQuery{}, false
	}
	var err error
	if raw := values.Get("limit"); raw != "" {
		query.Limit, err = strconv.Atoi(raw)
		if err != nil || query.Limit < 1 || query.Limit > 200 {
			return resourceQuery{}, false
		}
	}
	if raw := values.Get("offset"); raw != "" {
		query.Offset, err = strconv.Atoi(raw)
		if err != nil || query.Offset < 0 || query.Offset > 1_000_000 {
			return resourceQuery{}, false
		}
	}
	return query, true
}

func pageBounds(total, offset, limit int) (int, int) {
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return offset, end
}
