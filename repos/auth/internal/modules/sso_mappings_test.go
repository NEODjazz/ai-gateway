package modules

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSSOBrowserGroupsRequireCurrentOrganizationApproval(t *testing.T) {
	m, directory, _, profile, key := browserIdentityFixture(t)
	profile.OrganizationID = "org-a"
	profile.RoleMappings = map[string]string{"users": "user"}
	profile.GroupsClaim = "realm.groups"
	profile.GroupMappings = map[string]string{"tenant-owners": "org_admin"}
	directory.principal.OrganizationID = "org-a"
	directory.principal.OrganizationRoles = []string{"org_admin"}
	nonce := strings.Repeat("n", 43)
	claims := browserIdentityClaims(profile, nonce)
	claims["roles"] = []string{}
	claims["realm"] = map[string]any{"groups": []string{"tenant-owners", "unmapped"}}
	req, err := m.verifySSOIdentity(t.Context(), profile, SSOBrowserLogin{ProfileID: profile.ID, Token: signRS256JWT(t, "browser", key, claims), Nonce: nonce})
	if err != nil || len(req.Roles) != 1 || req.Roles[0] != "org_admin" {
		t.Fatal("verified group role not approved", err)
	}
	state, revision, err := m.sso.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.Active = profile
	if err = m.sso.save(t.Context(), revision, state); err != nil {
		t.Fatal(err)
	}
	session, err := m.CreateSSOBrowserSession(t.Context(), SSOBrowserLogin{ProfileID: profile.ID, Token: signRS256JWT(t, "browser", key, claims), Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Handle(t.Context(), &RequestContext{APIKey: session.Token}); err != nil {
		t.Fatal("group role not retained in server session", err)
	}
	directory.principal.OrganizationRoles = []string{"user"}
	if _, err = m.verifySSOIdentity(t.Context(), profile, SSOBrowserLogin{ProfileID: profile.ID, Token: signRS256JWT(t, "browser", key, claims), Nonce: nonce}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("group claim bypassed removed organization approval", err)
	}
	if err = m.Handle(t.Context(), &RequestContext{APIKey: session.Token}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("group server session bypassed removed approval", err)
	}
	directory.principal.OrganizationRoles = []string{"org_admin"}
	claims["realm"] = map[string]any{"groups": []any{"tenant-owners", 7}}
	if _, err = m.verifySSOIdentity(t.Context(), profile, SSOBrowserLogin{ProfileID: profile.ID, Token: signRS256JWT(t, "browser", key, claims), Nonce: nonce}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("malformed group claim partially accepted", err)
	}
}

func TestSSOVerifiedIdentityPreviewDoesNotApproveUnboundAccount(t *testing.T) {
	m, directory, _, profile, key := browserIdentityFixture(t)
	directory.found = false
	view, err := m.SaveSSODraft(t.Context(), SSODraftInput{SSOProfileConfig: profile.SSOProfileConfig, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := m.sso.StartTest(t.Context(), view.Revision, "directory-user")
	if err != nil {
		t.Fatal(err)
	}
	state, revision, err := m.sso.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("n", 43)
	claims := browserIdentityClaims(state.Draft, nonce)
	claims["email"] = "private@example.com"
	claims["groups"] = []string{"private-group"}
	token := signRS256JWT(t, "browser", key, claims)
	if err = m.VerifySSOTest(t.Context(), state.Draft.ID, ticket, token, nonce); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("unbound account approved", err)
	}
	view, err = m.sso.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	identity := view.VerifiedIdentity
	if identity == nil || identity.Approved || identity.Subject != "external-subject" || identity.UserID != "" || len(identity.Roles) > 0 || view.TestStatus != "failed" || view.LastTestStatus != "failed" {
		t.Fatal("verified identity confused with directory approval")
	}
	payload, _ := json.Marshal(view)
	for _, secret := range []string{token, "private@example.com", "private-group", "session_hash", "policy_digest", "client_secret\""} {
		if strings.Contains(string(payload), secret) {
			t.Fatal("diagnostics disclosed token/private claims")
		}
	}
	if _, err = m.ChangeSSO(t.Context(), "activate", revision+1, "directory-user"); err == nil {
		t.Fatal("identity preview used as activation proof")
	}
	// A fresh attempt with an invalid signature must erase the old preview.
	ticket, err = m.sso.StartTest(t.Context(), view.Revision, "directory-user")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.VerifySSOTest(t.Context(), state.Draft.ID, ticket, "invalid-token", nonce); err == nil {
		t.Fatal("invalid token verified")
	}
	view, err = m.sso.View(t.Context())
	if err != nil || view.VerifiedIdentity != nil {
		t.Fatal("stale preview retained after verification failure", err)
	}
}

func TestSSOGroupMappingCannotMaskPlatformRoleOrInvalidSource(t *testing.T) {
	config := testSSOConfig()
	config.OrganizationID = "org-a"
	config.RoleMappings = map[string]string{"oidc-group-target:org_admin": "admin"}
	config.GroupsClaim = "groups"
	config.GroupMappings = map[string]string{"owners": "org_admin"}
	if err := config.Validate(); err == nil {
		t.Fatal("group key collision masked platform role")
	}
	config.RoleMappings = map[string]string{"users": "user"}
	config.GroupMappings = map[string]string{"bad\nsource": "org_admin"}
	if err := config.Validate(); err == nil {
		t.Fatal("invalid external group accepted")
	}
}
