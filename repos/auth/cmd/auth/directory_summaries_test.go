package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-auth/internal/modules"
)

type directorySummaryHTTPStore struct {
	teamOrganizationHTTPStore
	query modules.DirectorySummaryQuery
}

func (s *directorySummaryHTTPStore) DirectorySummaries(_ context.Context, q modules.DirectorySummaryQuery) ([]modules.DirectorySummary, error) {
	s.calls++
	s.query = q
	return []modules.DirectorySummary{{ID: q.IDs[0], KeyCount: 71}}, s.err
}

func TestDirectorySummaryHTTPRequiresSecretAndBoundedIdentityQuery(t *testing.T) {
	s := &directorySummaryHTTPStore{}
	m := modules.NewAuthModuleWithStore(true, s, "fixture", false)
	mux := http.NewServeMux()
	registerIdentityDirectoryRoutes(mux, &m, "fixture-secret")
	for _, tc := range []struct {
		body, secret string
		status       int
	}{
		{`{"scope_type":"team","ids":["team-620"]}`, "fixture-secret", 200},
		{`{"scope_type":"team","ids":["team-620"]}`, "", 401},
		{`{"scope_type":"global","ids":["team-620"]}`, "fixture-secret", 400},
		{`{"scope_type":"user","ids":["alice","alice"]}`, "fixture-secret", 400},
		{`{"scope_type":"team","ids":["a"],"extra":1}`, "fixture-secret", 400},
		{`{"scope_type":"team","ids":[]}`, "fixture-secret", 400},
	} {
		before := s.calls
		r := httptest.NewRequest("POST", "/internal/v1/directory-summaries", strings.NewReader(tc.body))
		r.Header.Set("X-Management-Token", tc.secret)
		r.Header.Set("X-Request-ID", "summary-request")
		r.Header.Set("X-Actor-ID", "operator")
		r.Header.Set("X-Actor-Credential-ID", "fixture-key")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.status || tc.status != 200 && s.calls != before {
			t.Fatalf("status=%d body=%s", w.Code, tc.body)
		}
		if tc.status == 200 && (!strings.Contains(w.Body.String(), `"key_count":71`) || s.query.IDs[0] != "team-620") {
			t.Fatal("summary or exact scope lost")
		}
	}
}
