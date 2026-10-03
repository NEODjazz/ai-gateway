package modules

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"strings"
)

const maxSSOConnections = 16

type SSOConnection struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	OrganizationID string `json:"organization_id,omitempty"`
}

type SSOConnectionView struct {
	SSOConnection
	SSOSettingsView
}

type ssoConnectionStore interface {
	ListSSOConnections(context.Context) ([]SSOConnection, error)
	CreateSSOConnection(context.Context, SSOConnection) error
	LoadSSOConnectionSettings(context.Context, string) (int64, []byte, error)
	SaveSSOConnectionSettings(context.Context, string, int64, []byte) error
}

type scopedSSOSettingsStore struct {
	registry ssoConnectionStore
	id       string
}

func (s scopedSSOSettingsStore) LoadSSOSettings(ctx context.Context) (int64, []byte, error) {
	return s.registry.LoadSSOConnectionSettings(ctx, s.id)
}
func (s scopedSSOSettingsStore) SaveSSOSettings(ctx context.Context, revision int64, payload []byte) error {
	return s.registry.SaveSSOConnectionSettings(ctx, s.id, revision, payload)
}

func validSSOConnection(connection SSOConnection) bool {
	if !validJWTIdentityValue(connection.ID, 64) || connection.ID == "default" || !validJWTIdentityValue(connection.Name, 128) {
		return false
	}
	for _, r := range connection.ID {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return (connection.Provider == "entra" || connection.Provider == "keycloak" || connection.Provider == "oidc") && (connection.OrganizationID == "" || validJWTIdentityValue(connection.OrganizationID, 256))
}

func (m *SSOManager) CreateConnection(ctx context.Context, connection SSOConnection) error {
	if m == nil || m.aead == nil {
		return ErrSSOUnavailable
	}
	if !validSSOConnection(connection) {
		return ErrSSOConfiguration
	}
	store, ok := m.store.(ssoConnectionStore)
	if !ok {
		return ErrSSOUnavailable
	}
	return store.CreateSSOConnection(ctx, connection)
}

// The legacy singleton remains the default connection. Additional rows have
// independent CAS revisions and a cipher domain bound to immutable metadata.
func (m *SSOManager) Connection(ctx context.Context, id string) (*SSOManager, SSOConnection, error) {
	if m == nil || m.aead == nil {
		return nil, SSOConnection{}, ErrSSOUnavailable
	}
	if id == "" || id == "default" {
		return m, SSOConnection{ID: "default", Name: "Default", Provider: "oidc"}, nil
	}
	store, ok := m.store.(ssoConnectionStore)
	if !ok {
		return nil, SSOConnection{}, ErrSSOUnavailable
	}
	connections, err := store.ListSSOConnections(ctx)
	if err != nil || len(connections) > maxSSOConnections {
		return nil, SSOConnection{}, ErrSSOUnavailable
	}
	for _, connection := range connections {
		if connection.ID != id {
			continue
		}
		if !validSSOConnection(connection) {
			return nil, SSOConnection{}, ErrSSOUnavailable
		}
		binding, _ := json.Marshal(connection)
		key := sha256.Sum256(append(append([]byte("ai-gateway/sso-connection/v1\x00"), m.connectionKey[:]...), binding...))
		block, err := aes.NewCipher(key[:])
		if err != nil {
			return nil, SSOConnection{}, ErrSSOUnavailable
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, SSOConnection{}, ErrSSOUnavailable
		}
		return &SSOManager{store: scopedSSOSettingsStore{registry: store, id: id}, aead: aead, now: m.now, sessionStore: m.sessionStore, sessionAEAD: m.sessionAEAD, organizationID: connection.OrganizationID}, connection, nil
	}
	return nil, SSOConnection{}, ErrSSOConfiguration
}

func (m *SSOManager) Connections(ctx context.Context) ([]SSOConnectionView, error) {
	store, ok := m.store.(ssoConnectionStore)
	if !ok {
		return nil, ErrSSOUnavailable
	}
	connections, err := store.ListSSOConnections(ctx)
	if err != nil || len(connections) > maxSSOConnections {
		return nil, ErrSSOUnavailable
	}
	connections = append([]SSOConnection{{ID: "default", Name: "Default", Provider: "oidc"}}, connections...)
	views := make([]SSOConnectionView, 0, len(connections))
	for _, connection := range connections {
		manager, _, err := m.Connection(ctx, connection.ID)
		if err != nil {
			return nil, err
		}
		view, err := manager.View(ctx)
		if err != nil {
			return nil, err
		}
		// The compatible default profile may already pin an organization.
		// Publish its actual scope rather than labeling it as platform-wide.
		if connection.ID == "default" {
			if view.Active != nil {
				connection.OrganizationID = view.Active.OrganizationID
			} else if view.Draft != nil {
				connection.OrganizationID = view.Draft.OrganizationID
			}
		}
		views = append(views, SSOConnectionView{SSOConnection: connection, SSOSettingsView: view})
	}
	return views, nil
}

func (m *SSOManager) activeProfile(ctx context.Context, id string) (*SSOProfile, error) {
	state, _, err := m.Load(ctx)
	if err != nil {
		return nil, err
	}
	if state.Active != nil && state.Active.ID == id {
		return state.Active, nil
	}
	store, ok := m.store.(ssoConnectionStore)
	if !ok {
		return nil, ErrUnauthorized
	}
	connections, err := store.ListSSOConnections(ctx)
	if err != nil || len(connections) > maxSSOConnections {
		return nil, ErrSSOUnavailable
	}
	for _, connection := range connections {
		manager, _, err := m.Connection(ctx, connection.ID)
		if err != nil {
			return nil, err
		}
		state, _, err := manager.Load(ctx)
		if err != nil {
			return nil, err
		}
		if state.Active != nil && state.Active.ID == id {
			return state.Active, nil
		}
	}
	return nil, ErrUnauthorized
}

func (m AuthModule) WithSSOConnection(ctx context.Context, id string) (AuthModule, error) {
	if strings.TrimSpace(id) != id {
		return m, ErrSSOConfiguration
	}
	api, err := m.currentJWTModule(ctx)
	if err != nil {
		return m, err
	}
	m.jwtConfig, m.jwtVerifier = api.jwtConfig, api.jwtVerifier
	manager, _, err := m.sso.Connection(ctx, id)
	if err != nil {
		return m, err
	}
	m.sso = manager
	return m, nil
}
