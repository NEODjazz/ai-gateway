package modules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func apiIssuerPostgresModule(t *testing.T) (AuthModule, *PostgresVirtualKeyStore) {
	t.Helper()
	store := ssoSessionPostgresStore(t)
	paths, err := filepath.Glob(filepath.Join("..", "..", "migrations", "postgres", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.pool.Exec(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := newAPIIssuerManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	module := NewAuthModuleWithJWT(true, JWTAuthConfig{IdentityMode: "legacy"})
	module.store, module.apiIssuers, module.demoKeys = store, manager, false
	return module, store
}
func TestPostgresAPIIssuerLifecycleReplicaProofAndRevocation(t *testing.T) {
	m, store := apiIssuerPostgresModule(t)
	input, key, _ := apiIssuerTestInput(t, "a", "org-a")
	if _, err := store.PutOrganization(t.Context(), Organization{ID: "org-a", Name: "A", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutUser(t.Context(), DirectoryUser{ID: "operator", Status: "active", Roles: []string{"admin", "org_admin"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutOrganizationMembership(t.Context(), OrganizationMembership{OrganizationID: "org-a", UserID: "operator", Roles: []string{"org_admin"}, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutJWTPrincipal(t.Context(), JWTPrincipalPolicy{Issuer: input.Issuer, Audience: input.Audience, Subject: "same-subject", UserID: "operator", OrganizationID: "org-a", Enabled: true, AllowedModels: []string{"model"}}); err != nil {
		t.Fatal(err)
	}
	view := activateAPIIssuer(t, m, input, key)
	replica := m
	var err error
	replica.apiIssuers, err = newAPIIssuerManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	req := RequestContext{APIKey: apiIssuerToken(t, input, key, nil)}
	if err = replica.Handle(t.Context(), &req); err != nil {
		t.Fatal(err)
	}
	if req.OrganizationID != "org-a" {
		t.Fatal("replica lost tenant")
	}
	var successes atomic.Int32
	var workers sync.WaitGroup
	for _, module := range []AuthModule{m, replica} {
		workers.Go(func() {
			_, err := module.SaveAPIIssuerDraft(t.Context(), "a", APIIssuerDraftInput{APIIssuerConfig: input.APIIssuerConfig, ExpectedRevision: view.Revision})
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrSSOConflict) {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if successes.Load() != 1 {
		t.Fatal("CAS admitted competing drafts")
	}
	views, err := m.APIIssuers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	view = views[0]
	view, err = m.ChangeAPIIssuer(t.Context(), "a", "disable", view.Revision, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err = replica.ReauthorizeJWTPrincipal(t.Context(), &req); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("replica retained revoked job trust", err)
	}
	if _, err = replica.ChangeAPIIssuer(t.Context(), "a", "rollback", view.Revision, "operator"); err != nil {
		t.Fatal(err)
	}
	if err = m.Handle(t.Context(), &RequestContext{APIKey: apiIssuerToken(t, input, key, nil)}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.PutOrganizationMembership(t.Context(), OrganizationMembership{OrganizationID: "org-a", UserID: "operator", Roles: []string{"org_admin"}, Status: "disabled"}); err != nil {
		t.Fatal(err)
	}
	if err = replica.Handle(t.Context(), &RequestContext{APIKey: apiIssuerToken(t, input, key, nil)}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled membership authenticated", err)
	}
	if _, err = store.pool.Exec(t.Context(), `UPDATE auth_api_issuers SET name='changed' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = m.APIIssuers(t.Context()); !errors.Is(err, ErrSSOUnavailable) {
		t.Fatal("immutable cipher metadata changed", err)
	}
}
func TestPostgresAPIIssuerConcurrentCapacityAndUniqueNamespace(t *testing.T) {
	m, store := apiIssuerPostgresModule(t)
	input, _, _ := apiIssuerTestInput(t, "first", "")
	if _, err := m.CreateAPIIssuer(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	duplicate := input
	duplicate.ID = "duplicate"
	if _, err := m.CreateAPIIssuer(t.Context(), duplicate); !errors.Is(err, ErrSSOConflict) {
		t.Fatal("namespace duplicate accepted", err)
	}
	inactive := input
	inactive.ID = "inactive"
	inactive.Audience = "inactive"
	inactive.OrganizationID = "missing"
	inactive.RoleMappings = map[string]string{"owners": "org_admin"}
	if _, err := m.CreateAPIIssuer(t.Context(), inactive); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("missing org bound", err)
	}
	var successes atomic.Int32
	var workers sync.WaitGroup
	for n := 0; n < 32; n++ {
		entry := input
		entry.ID = fmt.Sprintf("issuer-%d", n)
		entry.Audience = entry.ID
		workers.Go(func() {
			_, err := m.CreateAPIIssuer(t.Context(), entry)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrSSOConfiguration) {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if successes.Load() != maxAPIIssuers-1 {
		t.Fatalf("capacity successes=%d", successes.Load())
	}
	rows, err := store.ListAPIIssuers(t.Context())
	if err != nil || len(rows) != maxAPIIssuers {
		t.Fatal("cardinality drift", err)
	}
}
