package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/vectorstate"
)

type vectorPageStore struct {
	*memoryVectorStore
	owner string
	query vectorstate.PageQuery
}

func (s *vectorPageStore) ListVectorStorePage(_ context.Context, owner string, q vectorstate.PageQuery) ([]vectorstate.VectorStore, int, error) {
	s.owner, s.query = owner, q
	return []vectorstate.VectorStore{{ID: "vs_owned", Name: "Owned", OwnerKey: owner}}, 130, nil
}

func TestVectorStorePagePinsVerifiedOwnerAndReturnsExactTotal(t *testing.T) {
	store := &vectorPageStore{memoryVectorStore: &memoryVectorStore{}}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "key", user: "alice"}}), nil).WithVectorStore(store, VectorStoreRuntimeConfig{OwnerQuota: 1000}))
	response := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/page?offset=100&limit=25&search=Needle&status=expired&sort_by=name&sort_order=asc", "")
	var page struct {
		Total, Offset, Limit int
		Data                 []map[string]any
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || page.Total != 130 || page.Offset != 100 || page.Limit != 25 || len(page.Data) != 1 {
		t.Fatalf("page response: status=%d data=%+v err=%v", response.Code, page, err)
	}
	if store.owner != fileOwnerKey(modules.RequestContext{CredentialID: "key", UserID: "alice"}) || store.query.Search != "Needle" || store.query.SortBy != "name" || store.query.Status != "expired" {
		t.Fatal("query lost verified scope or filters")
	}
	if strings.Contains(response.Body.String(), store.owner) || strings.Contains(response.Body.String(), "owner_key") {
		t.Fatal("private storage owner leaked")
	}
	for _, query := range []string{"limit=0", "limit=201", "limit=", "offset=-1", "offset=abc", "after=vs_other", "owner=foreign", "search=a&search=b", "sort_by=unknown", "sort_order=unknown", "status=unknown"} {
		if got := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/page?"+query, ""); got.Code != 400 {
			t.Errorf("invalid query %q returned %d", query, got.Code)
		}
	}
}

func TestVectorStorePageUnavailableBackendDoesNotReturnAnEmptyDirectory(t *testing.T) {
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&fileAuthModule{credential: "key", user: "alice"}}), nil).WithVectorStore(&memoryVectorStore{}, VectorStoreRuntimeConfig{OwnerQuota: 1000}))
	if response := callVectorStore(handler, http.MethodGet, "/v1/vector_stores/page", ""); response.Code != 503 {
		t.Fatalf("missing page backend: %d %s", response.Code, response.Body.String())
	}
}
