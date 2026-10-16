package modules

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIdentitySpendUsesBoundedMultipartParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(256 << 10); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer func() { _ = r.MultipartForm.RemoveAll() }()
		query := r.FormValue("query")
		if !strings.Contains(query, "user_id IN ({id_0:String},{id_1:String})") || !strings.Contains(query, "phase IN ('commit','cancel')") || !strings.Contains(query, "sum(usage.cost)") || strings.Contains(query, "injected'") || r.FormValue("param_id_1") != "injected' OR 1=1" || r.URL.Query().Get("output_format_json_quote_64bit_integers") != "0" || len(r.URL.RawQuery) > 100 {
			t.Errorf("incorrect parameterized query: %s", query)
		}
		_, _ = w.Write([]byte("{\"id\":\"alice\",\"currency\":\"EUR\",\"cost\":4,\"requests\":2}\n{\"id\":\"alice\",\"currency\":\"USD\",\"cost\":3,\"requests\":1}\n"))
	}))
	defer server.Close()
	r, err := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "db", ClickHouseUsageEventsTable: "events"})
	if err != nil {
		t.Fatal(err)
	}
	q := IdentitySpendQuery{IdentityBudgetSummariesQuery: IdentityBudgetSummariesQuery{ScopeType: "user", IDs: []string{"alice", "injected' OR 1=1"}}, From: time.Unix(1000, 0), To: time.Unix(2000, 0)}
	rows, err := r.IdentitySpend(t.Context(), q)
	if err != nil || len(rows) != 2 || rows[0].Cost != 4 || rows[1].Cost != 3 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestIdentitySpendRejectsIncompleteForeignAndDuplicateResults(t *testing.T) {
	for _, body := range []string{`{"id":"foreign","currency":"USD","cost":9,"requests":1}`, `{"id":"alice","currency":"USD","cost":-1,"requests":1}`, "{\"id\":\"alice\",\"currency\":\"USD\",\"cost\":1,\"requests\":1}\n{\"id\":\"alice\",\"currency\":\"USD\",\"cost\":2,\"requests\":1}", "{\"id\":\"alice\",\"cost\":", strings.Repeat("x", (2<<20)+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		r, _ := NewClickHouseUsageReporter(Settings{UsageEventsEnabled: true, ClickHouseURL: server.URL, ClickHouseDatabase: "db", ClickHouseUsageEventsTable: "events"})
		_, err := r.IdentitySpend(t.Context(), IdentitySpendQuery{IdentityBudgetSummariesQuery: IdentityBudgetSummariesQuery{ScopeType: "user", IDs: []string{"alice"}}, From: time.Unix(1000, 0), To: time.Unix(2000, 0)})
		server.Close()
		if err == nil {
			t.Fatal("invalid source produced a plausible zero or partial spend")
		}
	}
}
