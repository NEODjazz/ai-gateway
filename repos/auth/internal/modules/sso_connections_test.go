package modules

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

type memorySSOConnections struct {
	*memorySSOSessions
	connections map[string]SSOConnection
	settings    map[string]*memorySSOSettings
}

func (s *memorySSOConnections) ListSSOConnections(context.Context) ([]SSOConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []SSOConnection{}
	for _, connection := range s.connections {
		result = append(result, connection)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
func (s *memorySSOConnections) CreateSSOConnection(_ context.Context, connection SSOConnection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.connections[connection.ID]; exists {
		return ErrSSOConflict
	}
	if len(s.connections) >= maxSSOConnections {
		return ErrSSOConfiguration
	}
	s.connections[connection.ID] = connection
	s.settings[connection.ID] = &memorySSOSettings{}
	return nil
}
func (s *memorySSOConnections) LoadSSOConnectionSettings(ctx context.Context, id string) (int64, []byte, error) {
	s.mu.Lock()
	store := s.settings[id]
	s.mu.Unlock()
	if store == nil {
		return 0, nil, ErrSSOConfiguration
	}
	return store.LoadSSOSettings(ctx)
}
func (s *memorySSOConnections) SaveSSOConnectionSettings(ctx context.Context, id string, revision int64, payload []byte) error {
	s.mu.Lock()
	store := s.settings[id]
	s.mu.Unlock()
	if store == nil {
		return ErrSSOConfiguration
	}
	return store.SaveSSOSettings(ctx, revision, payload)
}
func connectionFixture(t *testing.T) (*SSOManager, *memorySSOConnections) {
	t.Helper()
	store := &memorySSOConnections{memorySSOSessions: &memorySSOSessions{memorySSOSettings: &memorySSOSettings{}, sessions: map[string][]byte{}, logins: map[string]bool{}}, connections: map[string]SSOConnection{}, settings: map[string]*memorySSOSettings{}}
	manager, err := NewSSOManager(store, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	return manager, store
}

func TestSSOConnectionsIsolateEncryptionRevisionProofAndOrganization(t *testing.T) {
	m, store := connectionFixture(t)
	for _, id := range []string{"a", "b"} {
		if err := m.CreateConnection(t.Context(), SSOConnection{ID: id, Name: id, Provider: "oidc", OrganizationID: "org-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	a, _, err := m.Connection(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := m.Connection(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	config := testSSOConfig()
	config.OrganizationID = "org-a"
	config.RoleMappings = map[string]string{"owners": "org_admin"}
	secret := "fixture-secret"
	if _, err = a.SaveDraft(t.Context(), SSODraftInput{SSOProfileConfig: config, ClientSecret: &secret}); err != nil {
		t.Fatal(err)
	}
	if _, revision, err := b.Load(t.Context()); err != nil || revision != 0 {
		t.Fatal("connection revision shared", err)
	}
	if _, err := b.SaveDraft(t.Context(), SSODraftInput{SSOProfileConfig: config}); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("connection tenant changed", err)
	}
	config.OrganizationID = "org-b"
	if _, err = b.SaveDraft(t.Context(), SSODraftInput{SSOProfileConfig: config, ClientSecret: &secret}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.StartTest(t.Context(), 1, "administrator"); err != nil {
		t.Fatal(err)
	}
	view, err := b.View(t.Context())
	if err != nil || view.TestStatus != "not_started" || view.Revision != 1 {
		t.Fatal("test proof shared", err)
	}
	if bytes.Contains(store.settings["a"].payload, []byte(secret)) {
		t.Fatal("plaintext secret persisted")
	}
	// Even under the shared key, rows and immutable organization metadata are
	// cryptographically distinct. A copied row cannot impersonate another IdP.
	original := store.settings["b"].payload
	store.settings["b"].payload = bytes.Clone(store.settings["a"].payload)
	if _, _, err = b.Load(t.Context()); !errors.Is(err, ErrSSOUnavailable) {
		t.Fatal("cross-connection ciphertext accepted", err)
	}
	store.settings["b"].payload = original
	store.connections["a"] = SSOConnection{ID: "a", Name: "a", Provider: "oidc", OrganizationID: "org-b"}
	tampered, _, err := m.Connection(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = tampered.Load(t.Context()); !errors.Is(err, ErrSSOUnavailable) {
		t.Fatal("tenant metadata ciphertext reassigned", err)
	}
	if _, _, err = m.Connection(t.Context(), "unknown"); !errors.Is(err, ErrSSOConfiguration) {
		t.Fatal("unknown connection defaulted", err)
	}
	state, _, err := m.Load(t.Context())
	if err != nil || state.Active != nil || state.APITrust != nil {
		t.Fatal("connection changed default/API trust", err)
	}
}

func TestOrganizationConnectionCannotGrantPlatformRoles(t *testing.T) {
	for _, role := range []string{"admin", "team_admin"} {
		config := testSSOConfig()
		config.OrganizationID = "org-a"
		config.RoleMappings = map[string]string{"owners": role}
		if err := config.Validate(); !errors.Is(err, ErrSSOConfiguration) {
			t.Fatal("organization granted platform role", role)
		}
	}
}

func TestSSOConnectionSessionUsesRootStoreAndRevokesIndependently(t *testing.T) {
	m, directory, oldStore, profile, key := browserIdentityFixture(t)
	registry := &memorySSOConnections{memorySSOSessions: oldStore, connections: map[string]SSOConnection{}, settings: map[string]*memorySSOSettings{}}
	root, err := NewSSOManager(registry, strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	m.sso = root
	if err = root.CreateConnection(t.Context(), SSOConnection{ID: "tenant", Name: "Tenant", Provider: "oidc", OrganizationID: "org-a"}); err != nil {
		t.Fatal(err)
	}
	child, _, err := root.Connection(t.Context(), "tenant")
	if err != nil {
		t.Fatal(err)
	}
	config := profile.SSOProfileConfig
	config.OrganizationID = "org-a"
	config.RoleMappings = map[string]string{"owners": "org_admin"}
	directory.principal.OrganizationID = "org-a"
	directory.principal.OrganizationRoles = []string{"org_admin"}
	view, err := child.SaveDraft(t.Context(), SSODraftInput{SSOProfileConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	selected := m
	selected.sso = child
	ticket, err := child.StartTest(t.Context(), view.Revision, "directory-user")
	if err != nil {
		t.Fatal(err)
	}
	state, revision, err := child.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.Repeat("n", 43)
	claims := browserIdentityClaims(state.Draft, nonce)
	claims["roles"] = []string{"owners"}
	if err = selected.VerifySSOTest(t.Context(), state.Draft.ID, ticket, signRS256JWT(t, "browser", key, claims), nonce); err != nil {
		t.Fatal(err)
	}
	if _, err = selected.ChangeSSO(t.Context(), "activate", revision+1, "directory-user"); err != nil {
		t.Fatal(err)
	}
	state, revision, err = child.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	nonce = strings.Repeat("x", 43)
	claims = browserIdentityClaims(state.Active, nonce)
	claims["roles"] = []string{"owners"}
	session, err := m.CreateSSOBrowserSession(t.Context(), SSOBrowserLogin{ProfileID: state.Active.ID, Token: signRS256JWT(t, "browser", key, claims), Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	req := RequestContext{APIKey: session.Token}
	if err = m.Handle(t.Context(), &req); err != nil || req.OrganizationID != "org-a" {
		t.Fatal("connection session not authorized by root", err)
	}
	if len(req.Roles) != 1 || req.Roles[0] != "org_admin" {
		t.Fatal("tenant session elevated platform admin")
	}
	if _, err = selected.ChangeSSO(t.Context(), "disable", revision, "directory-user"); err != nil {
		t.Fatal(err)
	}
	if err = m.Handle(t.Context(), &RequestContext{APIKey: session.Token}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("disabled connection session remains valid", err)
	}
	defaultState, _, err := root.Load(t.Context())
	if err != nil || defaultState.Active.ID != profile.ID || !defaultState.Active.Enabled {
		t.Fatal("connection disabled default login", err)
	}
}

func TestDefaultSSOConnectionPublishesActualOrganization(t *testing.T) {
	manager, _ := connectionFixture(t)
	config := testSSOConfig()
	config.OrganizationID = "org-a"
	config.RoleMappings = map[string]string{"owners": "org_admin"}
	if _, err := manager.SaveDraft(t.Context(), SSODraftInput{SSOProfileConfig: config}); err != nil {
		t.Fatal(err)
	}
	views, err := manager.Connections(t.Context())
	if err != nil || len(views) != 1 || views[0].OrganizationID != "org-a" {
		t.Fatal("default draft tenant mislabeled", err)
	}
	state, revision, err := manager.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.Active = state.Draft
	state.Draft = nil
	if err = manager.save(t.Context(), revision, state); err != nil {
		t.Fatal(err)
	}
	views, err = manager.Connections(t.Context())
	if err != nil || len(views) != 1 || views[0].OrganizationID != "org-a" {
		t.Fatal("default active tenant mislabeled", err)
	}
}
