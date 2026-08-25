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

func (r *Router) ListCredentials(context.Context) []Credential {
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
	return r.storeCredential("", input)
}

func (r *Router) UpdateCredential(id string, input CredentialInput) (Credential, error) {
	return r.storeCredential(strings.TrimSpace(id), input)
}

func (r *Router) storeCredential(id string, input CredentialInput) (Credential, error) {
	if r == nil || r.credentials == nil {
		return Credential{}, ErrCredentialNotFound
	}
	input.ID = strings.TrimSpace(input.ID)
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	input.Description = strings.TrimSpace(input.Description)
	if id != "" {
		input.ID = id
	}
	if input.ID == "" || len(input.ID) > 128 || len(input.ProviderID) > 128 || len(input.Description) > 512 || input.Secret == "" || len(input.Secret) > 32<<10 {
		return Credential{}, ErrInvalidCredential
	}
	if input.ProviderID != "" {
		providers := r.providers.current.Load()
		if _, found := (*providers)[input.ProviderID]; !found {
			return Credential{}, ErrInvalidCredential
		}
	}
	nonce := make([]byte, r.credentials.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Credential{}, err
	}
	ciphertext := r.credentials.aead.Seal(nil, nonce, []byte(input.Secret), []byte(input.ID))
	r.credentials.mu.Lock()
	existing, found := r.credentials.current[input.ID]
	if id == "" && found {
		r.credentials.mu.Unlock()
		return Credential{}, ErrCredentialExists
	}
	if id != "" && !found {
		r.credentials.mu.Unlock()
		return Credential{}, ErrCredentialNotFound
	}
	now := time.Now().UTC()
	created := now
	if found {
		created = existing.CreatedAt
	}
	credential := Credential{ID: input.ID, ProviderID: input.ProviderID, Description: input.Description, CreatedAt: created, UpdatedAt: now}
	r.credentials.current[input.ID] = encryptedCredential{Credential: credential, Nonce: nonce, Ciphertext: ciphertext}
	r.credentials.mu.Unlock()
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
	return credential, nil
}

func (r *Router) DeleteCredential(id string) error {
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
	defer r.credentials.mu.Unlock()
	if _, found := r.credentials.current[id]; !found {
		return ErrCredentialNotFound
	}
	delete(r.credentials.current, id)
	return nil
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
