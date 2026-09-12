package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

func TestSCIMGroupLifecycle(t *testing.T) {
	client := &directoryClientStub{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithIdentityDirectory(client)
	router := Routes(handler)

	create := httptest.NewRecorder()
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"externalId":"department-7","displayName":"Platform","members":[{"value":"user-1"}]}`
	router.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/scim/v2/Groups", strings.NewReader(body)))
	if create.Code != http.StatusCreated || create.Header().Get("Location") == "" {
		t.Fatalf("create status=%d headers=%v body=%s", create.Code, create.Header(), create.Body.String())
	}
	var created scimGroup
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil || created.ID == "" || len(created.Members) != 1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	patch := httptest.NewRecorder()
	patchBody := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"add","path":"members","value":[{"value":"user-2"}]},{"op":"remove","path":"members[value eq \"user-1\"]"}]}`
	router.ServeHTTP(patch, httptest.NewRequest(http.MethodPatch, "/scim/v2/Groups/"+created.ID, strings.NewReader(patchBody)))
	if patch.Code != http.StatusOK || client.group == nil || len(client.group.Members) != 1 || client.group.Members[0] != "user-2" {
		t.Fatalf("patch status=%d group=%+v body=%s", patch.Code, client.group, patch.Body.String())
	}

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/scim/v2/Groups?startIndex=1&count=10", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"totalResults":1`) || !strings.Contains(list.Body.String(), `"value":"user-2"`) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	filtered := httptest.NewRecorder()
	router.ServeHTTP(filtered, httptest.NewRequest(http.MethodGet, `/scim/v2/Groups?filter=externalId%20eq%20%22department-7%22`, nil))
	if filtered.Code != http.StatusOK || client.groupAttr != "externalId" || client.groupValue != "department-7" || !strings.Contains(filtered.Body.String(), `"totalResults":1`) {
		t.Fatalf("filter status=%d attribute=%q value=%q body=%s", filtered.Code, client.groupAttr, client.groupValue, filtered.Body.String())
	}

	remove := httptest.NewRecorder()
	router.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/scim/v2/Groups/"+created.ID, nil))
	if remove.Code != http.StatusNoContent || client.group.Team.DeletedAt == nil || len(client.group.Members) != 0 {
		t.Fatalf("delete status=%d group=%+v body=%s", remove.Code, client.group, remove.Body.String())
	}
	getDeleted := httptest.NewRecorder()
	router.ServeHTTP(getDeleted, httptest.NewRequest(http.MethodGet, "/scim/v2/Groups/"+created.ID, nil))
	if getDeleted.Code != http.StatusNotFound {
		t.Fatalf("deleted get status=%d body=%s", getDeleted.Code, getDeleted.Body.String())
	}
}

func TestSCIMGroupPatchRejectsUnsupportedMemberFilter(t *testing.T) {
	client := &directoryClientStub{group: &DirectoryGroup{Team: DirectoryTeam{ID: "group-1", Name: "Platform", Status: "active"}}}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithIdentityDirectory(client)
	body := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"remove","path":"members[display eq \"Person\"]"}]}`
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/scim/v2/Groups/group-1", strings.NewReader(body)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"scimType":"invalidValue"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
