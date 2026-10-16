package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

type apiIssuerClientStub struct {
	ssoClientStub
	calls int
	path  string
	audit ManagementAudit
}

func (s *apiIssuerClientStub) APIIssuers(context.Context, ManagementAudit) ([]APIIssuerView, error) {
	return []APIIssuerView{{APIIssuer: APIIssuer{ID: "a", Name: "A", Issuer: "https://idp.example", Audience: "api"}, Revision: 1}}, nil
}
func (s *apiIssuerClientStub) MutateAPIIssuer(_ context.Context, audit ManagementAudit, path, method string, input any) (APIIssuerView, error) {
	s.calls++
	s.path, s.audit = path, audit
	return APIIssuerView{APIIssuer: APIIssuer{ID: "a"}, Revision: 2}, nil
}
func TestAPIIssuerMutationsRequireRecoveryKeyAndDurableAudit(t *testing.T) {
	for _, test := range []struct {
		name      string
		auth      modules.Module
		auditFail bool
		body      string
		want      int
	}{
		{"key", managementAuthModule{roles: []string{"admin"}}, false, `{"action":"activate","expected_revision":1}`, 200},
		{"org admin", managementAuthModule{roles: []string{"org_admin"}}, false, `{}`, 403},
		{"JWT admin", ssoJWTAdminModule{managementAuthModule{roles: []string{"admin"}}}, false, `{}`, 403},
		{"audit outage", managementAuthModule{roles: []string{"admin"}}, true, `{"action":"disable","expected_revision":1}`, 503},
		{"unknown field", managementAuthModule{roles: []string{"admin"}}, false, `{"action":"activate","expected_revision":1,"issuer":"https://foreign.example"}`, 400},
		{"invalid action", managementAuthModule{roles: []string{"admin"}}, false, `{"action":"grant","expected_revision":1}`, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &apiIssuerClientStub{}
			audit := &recordingAuditClient{}
			if test.auditFail {
				audit.appendErr = errors.New("offline")
			}
			h := Routes(NewHandler(modules.NewPipeline([]modules.Module{test.auth}), modelsProvider{}).WithIdentityDirectory(&directoryClientStub{}).WithSSOManagement(client).WithAudit(audit))
			r := httptest.NewRequest("POST", "/admin/v1/api-issuers/a/action", strings.NewReader(test.body))
			r.Header.Set("Authorization", "Bearer fixture")
			r.Header.Set("X-Actor-Roles", "admin")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatal(w.Code, w.Body.String())
			}
			if test.want != 200 && client.calls != 0 {
				t.Fatal("denied request reached issuer manager")
			}
			if test.want == 200 && (client.path != "/internal/v1/api-issuers/a/action" || client.audit.ActorID == "") {
				t.Fatal("trusted actor or route lost")
			}
		})
	}
}
func TestAPIIssuerRemoteContractAndSafeList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Actor-Roles") != "admin" {
			t.Error("actor lost")
		}
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []APIIssuerView{{APIIssuer: APIIssuer{ID: "a", OrganizationID: "org-a"}, Revision: 3, LastTestStatus: "passed"}}})
			return
		}
		var input apiIssuerTest
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Token != "synthetic-resource-token" {
			t.Error("test input lost")
		}
		_ = json.NewEncoder(w).Encode(APIIssuerView{APIIssuer: APIIssuer{ID: "a"}, Revision: 4})
	}))
	defer server.Close()
	client := NewRemoteManagementClient(server.URL, "fixture-service")
	audit := ManagementAudit{RequestID: "r", ActorID: "operator", CredentialID: "key", Roles: []string{"admin"}}
	views, err := client.APIIssuers(t.Context(), audit)
	if err != nil || len(views) != 1 || views[0].OrganizationID != "org-a" {
		t.Fatal("list contract", err)
	}
	view, err := client.MutateAPIIssuer(t.Context(), audit, "/internal/v1/api-issuers/a/test", "POST", apiIssuerTest{ExpectedRevision: 3, Token: "synthetic-resource-token"})
	if err != nil || view.Revision != 4 {
		t.Fatal("test contract", err)
	}
}
