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

func TestPostgresSSOConnectionsReplicaCASCardinalityAndCipherBinding(t *testing.T) {
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
	if _, err = store.PutOrganization(t.Context(), Organization{ID: "org-a", Name: "A", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	m, err := NewSSOManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	connection := SSOConnection{ID: "a", Name: "A", Provider: "entra", OrganizationID: "org-a"}
	if err = m.CreateConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	if err = m.CreateConnection(t.Context(), connection); !errors.Is(err, ErrSSOConflict) {
		t.Fatal("duplicate connection accepted", err)
	}
	bad := connection
	bad.ID = "inactive"
	bad.OrganizationID = "unknown"
	if err = m.CreateConnection(t.Context(), bad); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("unapproved organization accepted", err)
	}
	a, _, err := m.Connection(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	config := testSSOConfig()
	config.OrganizationID = "org-a"
	config.RoleMappings = map[string]string{"owners": "org_admin"}
	secret := "postgres-fixture-secret"
	input := SSODraftInput{SSOProfileConfig: config, ClientSecret: &secret}
	if _, err = a.SaveDraft(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	replica, err := NewSSOManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := replica.Connection(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	state, revision, err := other.Load(t.Context())
	if err != nil || revision != 1 || state.Draft.ClientSecret != secret {
		t.Fatal("replica connection configuration missing", err)
	}
	input.ExpectedRevision = 1
	var successes atomic.Int32
	var workers sync.WaitGroup
	for _, manager := range []*SSOManager{a, other} {
		workers.Add(1)
		go func(manager *SSOManager) {
			defer workers.Done()
			_, err := manager.SaveDraft(t.Context(), input)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrSSOConflict) {
				t.Error(err)
			}
		}(manager)
	}
	workers.Wait()
	if successes.Load() != 1 {
		t.Fatal("replica CAS did not serialize connection revision")
	}
	// Database admission is serialized across concurrent replicas and bounded.
	var created atomic.Int32
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			connection := SSOConnection{ID: fmt.Sprintf("connection-%02d", i), Name: "Bounded", Provider: "oidc"}
			err := m.CreateConnection(t.Context(), connection)
			if err == nil {
				created.Add(1)
			} else if !errors.Is(err, ErrSSOConfiguration) {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	if created.Load() != maxSSOConnections-1 {
		t.Fatal("connection cardinality limit not enforced", created.Load())
	}
	// Changing immutable tenant metadata cannot reassign encrypted credentials.
	if _, err = store.pool.Exec(t.Context(), `UPDATE auth_sso_connections SET organization_id=NULL WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	changed, _, err := m.Connection(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = changed.Load(t.Context()); !errors.Is(err, ErrSSOUnavailable) {
		t.Fatal("connection ciphertext tenant binding lost", err)
	}
}
