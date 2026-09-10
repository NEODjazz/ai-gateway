package provider

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

type Credential struct {
	ID          string    `json:"id"`
	ProviderID  string    `json:"provider_id,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CredentialInput struct {
	ID          string `json:"id"`
	ProviderID  string `json:"provider_id,omitempty"`
	Description string `json:"description,omitempty"`
	Secret      string `json:"secret"`
}

type CredentialController interface {
	ListCredentials(context.Context) []Credential
	CreateCredential(CredentialInput) (Credential, error)
	UpdateCredential(string, CredentialInput) (Credential, error)
	RotateCredential(string, string) (Credential, error)
	DeleteCredential(string) error
}

var (
	ErrCredentialNotFound = errors.New("credential not found")
	ErrCredentialExists   = errors.New("credential already exists")
	ErrCredentialInUse    = errors.New("credential is in use")
	ErrInvalidCredential  = errors.New("invalid credential")
)

type encryptedCredential struct {
	Credential
	Nonce      []byte
	Ciphertext []byte
}

type credentialVault struct {
	mu      sync.RWMutex
	aead    cipher.AEAD
	current map[string]encryptedCredential
}

func newCredentialVault(keyMaterial []byte) *credentialVault {
	if len(keyMaterial) == 0 {
		keyMaterial = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, keyMaterial); err != nil {
			panic("credential encryption entropy unavailable")
		}
	}
	key := sha256.Sum256(keyMaterial)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic("credential encryption unavailable")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic("credential encryption unavailable")
	}
	return &credentialVault{aead: aead, current: map[string]encryptedCredential{}}
}

func (r *Router) ListCredentials(ctx context.Context) []Credential {
	_ = r.refreshControlPlane(ctx)
	if r == nil || r.credentials == nil {
		return nil
	}
	r.credentials.mu.RLock()
	defer r.credentials.mu.RUnlock()
	result := make([]Credential, 0, len(r.credentials.current))
	for _, item := range r.credentials.current {
		result = append(result, item.Credential)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *Router) CreateCredential(input CredentialInput) (Credential, error) {
	return r.storeCredential("", input, false)
}

func (r *Router) UpdateCredential(id string, input CredentialInput) (Credential, error) {
	return r.storeCredential(strings.TrimSpace(id), input, false)
}

func (r *Router) RotateCredential(id, secret string) (Credential, error) {
	return r.storeCredential(strings.TrimSpace(id), CredentialInput{Secret: secret}, true)
}

func (r *Router) storeCredential(id string, input CredentialInput, preserveMetadata bool) (Credential, error) {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return Credential{}, err
	}
	defer unlock()
	if r == nil || r.credentials == nil {
		return Credential{}, ErrCredentialNotFound
	}
	input.ID = strings.TrimSpace(input.ID)
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	input.Description = strings.TrimSpace(input.Description)
	if id != "" {
		input.ID = id
	}
	if input.ID == "" || len(input.ID) > 128 || len(input.ProviderID) > 128 || len(input.Description) > 512 || len(input.Secret) > 32<<10 || (id == "" && input.Secret == "") || (preserveMetadata && input.Secret == "") {
		return Credential{}, ErrInvalidCredential
	}
	if input.ProviderID != "" {
		if r.providers == nil || r.providers.current.Load() == nil {
			return Credential{}, ErrInvalidCredential
		}
		providers := r.providers.current.Load()
		if _, found := (*providers)[input.ProviderID]; !found {
			return Credential{}, ErrInvalidCredential
		}
	}
	r.credentials.mu.RLock()
	existing, found := r.credentials.current[input.ID]
	r.credentials.mu.RUnlock()
	if id == "" && found {
		return Credential{}, ErrCredentialExists
	}
	if id != "" && !found {
		return Credential{}, ErrCredentialNotFound
	}
	if preserveMetadata {
		input.ProviderID = existing.ProviderID
		input.Description = existing.Description
	}
	if input.Secret != "" && input.ProviderID != "" && r.providers != nil && r.providers.current.Load() != nil {
		if managed, found := (*r.providers.current.Load())[input.ProviderID]; found && managed.Type == "bedrock" && managed.AuthType == "aws_sigv4" {
			if _, err := parseAWSCredential(input.Secret); err != nil {
				return Credential{}, ErrInvalidCredential
			}
		}
	}
	if input.ProviderID != "" && r.deployments != nil {
		if deployments := r.deployments.current.Load(); deployments != nil {
			for _, deployment := range *deployments {
				if deployment.CredentialID == input.ID && deployment.ProviderID != input.ProviderID {
					return Credential{}, ErrInvalidCredential
				}
			}
		}
	}
	nonce := append([]byte(nil), existing.Nonce...)
	ciphertext := append([]byte(nil), existing.Ciphertext...)
	if input.Secret != "" {
		nonce = make([]byte, r.credentials.aead.NonceSize())
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return Credential{}, err
		}
		ciphertext = r.credentials.aead.Seal(nil, nonce, []byte(input.Secret), []byte(input.ID))
	}
	now := time.Now().UTC()
	created := now
	if found {
		created = existing.CreatedAt
	}
	credential := Credential{ID: input.ID, ProviderID: input.ProviderID, Description: input.Description, CreatedAt: created, UpdatedAt: now}
	r.credentials.mu.Lock()
	r.credentials.current[input.ID] = encryptedCredential{Credential: credential, Nonce: nonce, Ciphertext: ciphertext}
	r.credentials.mu.Unlock()
	if found {
		r.dropAWSCredentialSources(existing.ProviderID, input.ID)
	}
	if id != "" {
		if deployments := r.deployments.current.Load(); deployments != nil {
			for _, deployment := range *deployments {
				if deployment.CredentialID == input.ID {
					if endpoint, buildErr := r.endpointForDeployment(deployment); buildErr == nil {
						r.replaceRuntimeEndpoint(deployment.ID, endpoint)
					}
				}
			}
		}
	}
	if err := r.persistControlMutation(context.Background(), previous); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

func (r *Router) DeleteCredential(id string) error {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	if r == nil || r.credentials == nil {
		return ErrCredentialNotFound
	}
	id = strings.TrimSpace(id)
	if deployments := r.deployments.current.Load(); deployments != nil {
		for _, deployment := range *deployments {
			if deployment.CredentialID == id {
				return ErrCredentialInUse
			}
		}
	}
	r.credentials.mu.Lock()
	credential, found := r.credentials.current[id]
	if !found {
		r.credentials.mu.Unlock()
		return ErrCredentialNotFound
	}
	delete(r.credentials.current, id)
	r.credentials.mu.Unlock()
	r.dropAWSCredentialSources(credential.ProviderID, id)
	return r.persistControlMutation(context.Background(), previous)
}

func (r *Router) credentialSecret(id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", nil
	}
	r.credentials.mu.RLock()
	item, found := r.credentials.current[id]
	r.credentials.mu.RUnlock()
	if !found {
		return "", ErrCredentialNotFound
	}
	plaintext, err := r.credentials.aead.Open(nil, item.Nonce, item.Ciphertext, []byte(id))
	if err != nil {
		return "", errors.New("credential decryption failed")
	}
	return string(plaintext), nil
}

func (r *Router) providerCredentialSecret(providerID, credentialID string) (string, error) {
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		return "", nil
	}
	providerID = strings.TrimSpace(providerID)
	r.credentials.mu.RLock()
	item, found := r.credentials.current[credentialID]
	r.credentials.mu.RUnlock()
	if !found || (item.ProviderID != "" && item.ProviderID != providerID) {
		return "", ErrCredentialNotFound
	}
	plaintext, err := r.credentials.aead.Open(nil, item.Nonce, item.Ciphertext, []byte(credentialID))
	if err != nil {
		return "", errors.New("credential decryption failed")
	}
	return string(plaintext), nil
}

func (r *Router) validateCredentialsForProvider(provider ManagedProvider) error {
	if provider.Type != "bedrock" || provider.AuthType != "aws_sigv4" {
		return nil
	}
	r.credentials.mu.RLock()
	defer r.credentials.mu.RUnlock()
	for id, item := range r.credentials.current {
		if item.ProviderID != provider.ID {
			continue
		}
		plaintext, err := r.credentials.aead.Open(nil, item.Nonce, item.Ciphertext, []byte(id))
		if err != nil {
			return ErrInvalidCredential
		}
		if _, err := parseAWSCredential(string(plaintext)); err != nil {
			return ErrInvalidCredential
		}
	}
	return nil
}
