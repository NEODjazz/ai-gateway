package modules

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJWKSRefreshDoesNotBlockCachedReadersOrCanceledWaiters(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) > 1 {
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jsonWebKey{ecJWK("key", &key.PublicKey)}})
	}))
	t.Cleanup(server.Close)
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	verifier, err := newJWTVerifier(JWTAuthConfig{Issuer: "issuer", Audience: "audience", JWKSURL: server.URL, JWKSCacheTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err = verifier.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := verifier.keysForVerification(t.Context(), true); finished <- err }()
	<-entered
	cached := make(chan error, 1)
	go func() { cached <- verifier.Ready(t.Context()) }()
	select {
	case err = <-cached:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fresh cached reader blocked by network refresh")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceled := make(chan error, 1)
	go func() { _, err := verifier.keysForVerification(ctx, true); canceled <- err }()
	select {
	case err = <-canceled:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrJWTUnavailable) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled waiter blocked by network refresh")
	}
	if requests.Load() != 2 {
		t.Fatal("cached reader or canceled waiter started another refresh")
	}
	releaseOnce.Do(func() { close(release) })
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestJWKSConcurrentRefreshSharesOneResultAndDoesNotAliasKeys(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jsonWebKey{ecJWK("key", &key.PublicKey)}})
	}))
	t.Cleanup(server.Close)
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	v, err := newJWTVerifier(JWTAuthConfig{Issuer: "issuer", Audience: "audience", JWKSURL: server.URL, JWKSCacheTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	const count = 32
	var wg sync.WaitGroup
	results := make(chan map[string]verificationKey, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keys, err := v.keysForVerification(t.Context(), false)
			if err != nil {
				t.Error(err)
			}
			results <- keys
		}()
	}
	<-entered
	releaseOnce.Do(func() { close(release) })
	wg.Wait()
	close(results)
	if calls.Load() != 1 {
		t.Fatalf("duplicate refresh calls: %d", calls.Load())
	}
	for keys := range results {
		if len(keys) != 1 {
			t.Fatal("refresh result missing")
		}
		delete(keys, "key")
	}
	keys, err := v.keysForVerification(t.Context(), false)
	if err != nil || len(keys) != 1 {
		t.Fatal("caller mutation changed shared key cache")
	}
}
