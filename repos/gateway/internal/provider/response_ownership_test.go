package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type ownershipTestStore struct {
	data map[string][]byte
	err  error
}

func (s *ownershipTestStore) Get(_ context.Context, k string) ([]byte, bool, error) {
	v, ok := s.data[k]
	return v, ok, s.err
}
func (s *ownershipTestStore) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	if s.err != nil {
		return s.err
	}
	s.data[k] = append([]byte(nil), v...)
	return nil
}

func TestResponseOwnershipIsolation(t *testing.T) {
	backend := &ownershipTestStore{data: map[string][]byte{}}
	store := responseOwnershipStore{store: backend, ttl: time.Hour}
	owner := modules.RequestContext{CredentialID: "key1", UserID: "user1"}
	binding := responseOwnership{Endpoint: "e", Model: "m", Deployment: responseDeploymentIdentity(Endpoint{Name: "e", ProviderID: "p", Type: "openai-compatible", BaseURL: "https://example.com", CredentialID: "c"})}
	if err := store.put(t.Context(), owner, "resp_1", binding); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.get(t.Context(), owner, "resp_1")
	if err != nil || !found || got != binding {
		t.Fatalf("binding=%+v found=%v err=%v", got, found, err)
	}
	for _, other := range []modules.RequestContext{{CredentialID: "key2", UserID: "user1"}, {CredentialID: "key1", UserID: "user2"}, {UserID: "user1"}} {
		if _, found, err := store.get(t.Context(), other, "resp_1"); found || err != nil {
			t.Fatalf("cross-owner access: %v %v", found, err)
		}
	}
	backend.err = errors.New("private backend details")
	if _, _, err := store.get(t.Context(), owner, "resp_1"); !errors.Is(err, errResponseOwnershipUnavailable) {
		t.Fatal(err)
	}
	if err := store.put(t.Context(), owner, "resp_1", binding); !errors.Is(err, errResponseOwnershipUnavailable) {
		t.Fatal(err)
	}
	backend.err = nil
	backend.data[responseOwnershipKey(owner, "resp_1")] = []byte(`{}`)
	if _, _, err := store.get(t.Context(), owner, "resp_1"); !errors.Is(err, errResponseOwnershipUnavailable) {
		t.Fatal("corrupt record accepted")
	}
}

func TestResponseDeploymentIdentityDetectsReplacement(t *testing.T) {
	original := Endpoint{Name: "e", ProviderID: "p", Type: "openai-compatible", BaseURL: "https://example.com", CredentialID: "c"}
	want := responseDeploymentIdentity(original)
	for _, field := range []string{"name", "provider", "type", "url", "credential"} {
		changed := original
		switch field {
		case "name":
			changed.Name = "other"
		case "provider":
			changed.ProviderID = "other"
		case "type":
			changed.Type = "other"
		case "url":
			changed.BaseURL = "https://other.example"
		case "credential":
			changed.CredentialID = "other"
		}
		if responseDeploymentIdentity(changed) == want {
			t.Fatalf("replacement ignored: %s", field)
		}
	}
}
