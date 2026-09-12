package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

func TestSCIMUserLifecycle(t *testing.T) {
	client := &directoryClientStub{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithIdentityDirectory(client)
	router := Routes(handler)

	create := httptest.NewRecorder()
	router.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/scim/v2/Users", strings.NewReader(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"externalId":"employee-42","userName":"person@example.test","displayName":"Person","active":true,"roles":[{"value":"developer"}]}`)))
	if create.Code != http.StatusCreated || create.Header().Get("Content-Type") != "application/scim+json" || create.Header().Get("Location") == "" {
		t.Fatalf("create status=%d headers=%v body=%s", create.Code, create.Header(), create.Body.String())
	}
	var created scimUser
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil || created.ID == "" || created.ExternalID != "employee-42" || created.UserName != "person@example.test" {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	patch := httptest.NewRecorder()
	patchBody := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false},{"op":"replace","path":"displayName","value":"Renamed"}]}`
	router.ServeHTTP(patch, httptest.NewRequest(http.MethodPatch, "/scim/v2/Users/"+created.ID, strings.NewReader(patchBody)))
	if patch.Code != http.StatusOK || client.user == nil || client.user.Status != "disabled" || client.user.Name != "Renamed" {
		t.Fatalf("patch status=%d user=%+v body=%s", patch.Code, client.user, patch.Body.String())
	}

	remove := httptest.NewRecorder()
	router.ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/scim/v2/Users/"+created.ID, nil))
	if remove.Code != http.StatusNoContent || client.user.Status != "disabled" {
		t.Fatalf("delete status=%d user=%+v body=%s", remove.Code, client.user, remove.Body.String())
	}

	getDeleted := httptest.NewRecorder()
	router.ServeHTTP(getDeleted, httptest.NewRequest(http.MethodGet, "/scim/v2/Users/"+created.ID, nil))
	if getDeleted.Code != http.StatusNotFound {
		t.Fatalf("deleted get status=%d body=%s", getDeleted.Code, getDeleted.Body.String())
	}
	listDeleted := httptest.NewRecorder()
	router.ServeHTTP(listDeleted, httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil))
	if listDeleted.Code != http.StatusOK || !strings.Contains(listDeleted.Body.String(), `"totalResults":0`) {
		t.Fatalf("deleted list status=%d body=%s", listDeleted.Code, listDeleted.Body.String())
	}
}

func TestSCIMDiscoveryAndPaginationAreTruthful(t *testing.T) {
	client := &directoryClientStub{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithIdentityDirectory(client)
	router := Routes(handler)

	config := httptest.NewRecorder()
	router.ServeHTTP(config, httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil))
	if config.Code != http.StatusOK || !strings.Contains(config.Body.String(), `"filter":{"maxResults":500,"supported":true}`) || !strings.Contains(config.Body.String(), `"patch":{"supported":true}`) {
		t.Fatalf("config status=%d body=%s", config.Code, config.Body.String())
	}

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/scim/v2/Users?startIndex=4&count=2", nil))
	if list.Code != http.StatusOK || client.userOffset != 3 || client.userLimit != 2 || !strings.Contains(list.Body.String(), `"totalResults":7`) || !strings.Contains(list.Body.String(), `"startIndex":4`) {
		t.Fatalf("list status=%d offset=%d limit=%d body=%s", list.Code, client.userOffset, client.userLimit, list.Body.String())
	}

	client.user = &DirectoryUser{ID: "user-filtered", ExternalID: "employee-42", Email: "person@example.test", Status: "active"}
	filtered := httptest.NewRecorder()
	router.ServeHTTP(filtered, httptest.NewRequest(http.MethodGet, `/scim/v2/Users?filter=userName%20eq%20%22person%40example.test%22`, nil))
	if filtered.Code != http.StatusOK || client.findAttr != "userName" || client.findValue != "person@example.test" || !strings.Contains(filtered.Body.String(), `"totalResults":1`) {
		t.Fatalf("filter status=%d attribute=%q value=%q body=%s", filtered.Code, client.findAttr, client.findValue, filtered.Body.String())
	}

	unsupported := httptest.NewRecorder()
	router.ServeHTTP(unsupported, httptest.NewRequest(http.MethodGet, `/scim/v2/Users?filter=title%20eq%20%22Engineer%22`, nil))
	if unsupported.Code != http.StatusBadRequest || !strings.Contains(unsupported.Body.String(), `"scimType":"invalidFilter"`) {
		t.Fatalf("unsupported filter status=%d body=%s", unsupported.Code, unsupported.Body.String())
	}

	invalid := httptest.NewRecorder()
	router.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/scim/v2/Users?startIndex=0", nil))
	if invalid.Code != http.StatusBadRequest || invalid.Header().Get("Content-Type") != "application/scim+json" {
		t.Fatalf("invalid status=%d headers=%v body=%s", invalid.Code, invalid.Header(), invalid.Body.String())
	}
}

func TestSCIMUserReportsReadOnlyGroupMemberships(t *testing.T) {
	client := &directoryClientStub{user: &DirectoryUser{ID: "user-1", Email: "person@example.test", Name: "Person", Status: "active", TeamIDs: []string{"platform", "risk/team"}}}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithIdentityDirectory(client)
	router := Routes(handler)

	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/scim/v2/Users/user-1", nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"groups":[{"value":"platform","$ref":"/scim/v2/Groups/platform"},{"value":"risk/team","$ref":"/scim/v2/Groups/risk%2Fteam"}]`) {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}

	replace := httptest.NewRecorder()
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"renamed@example.test","groups":[{"value":"untrusted"}]}`
	router.ServeHTTP(replace, httptest.NewRequest(http.MethodPut, "/scim/v2/Users/user-1", strings.NewReader(body)))
	if replace.Code != http.StatusOK || client.user == nil || len(client.user.TeamIDs) != 2 || client.user.TeamIDs[0] != "platform" || strings.Contains(replace.Body.String(), "untrusted") {
		t.Fatalf("replace status=%d user=%+v body=%s", replace.Code, client.user, replace.Body.String())
	}
}

func TestSCIMRequiresGlobalAdmin(t *testing.T) {
	handler := NewHandler(modules.NewPipeline([]modules.Module{teamAdminModule{}}), modelsProvider{}).WithIdentityDirectory(&directoryClientStub{})
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"status":"403"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSCIMMutationFailsClosedWhenAuditIsUnavailable(t *testing.T) {
	client := &directoryClientStub{}
	audit := &recordingAuditClient{appendErr: errors.New("audit down")}
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{roles: []string{"admin"}}}), modelsProvider{}).WithIdentityDirectory(client).WithAudit(audit)
	response := httptest.NewRecorder()
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"person@example.test"}`
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/scim/v2/Users", strings.NewReader(body)))
	if response.Code != http.StatusServiceUnavailable || client.user != nil {
		t.Fatalf("status=%d user=%+v body=%s", response.Code, client.user, response.Body.String())
	}
}
