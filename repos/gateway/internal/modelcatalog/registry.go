package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const registryStoreKey = "runtime-model-catalog"

type RegistryStore interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
}

// Registry provides lock-free catalog snapshots and optional Redis-backed
// synchronization between gateway replicas.
type Registry struct {
	current         atomic.Pointer[Catalog]
	authoritative   atomic.Bool
	store           RegistryStore
	refreshInterval time.Duration
	nextRefresh     atomic.Int64
	refreshMu       sync.Mutex
}

func NewRegistry(initial Catalog, store RegistryStore, refreshInterval time.Duration) *Registry {
	if refreshInterval <= 0 {
		refreshInterval = time.Second
	}
	if initial.Version == "" {
		initial.Version = "static-env"
	}
	if initial.Models == nil {
		initial.Models = []Model{}
	}
	r := &Registry{store: store, refreshInterval: refreshInterval}
	r.current.Store(&initial)
	return r
}

func (r *Registry) Current(ctx context.Context) Catalog {
	if r == nil {
		catalog, _ := Parse("")
		return catalog
	}
	if !r.authoritative.Load() && r.store != nil && time.Now().UnixNano() >= r.nextRefresh.Load() {
		r.refresh(ctx)
	}
	return *r.current.Load()
}

// SetAuthoritative switches the registry to a control-plane-owned snapshot.
// Once enabled, the legacy registry store can no longer overwrite a catalog
// that was committed as part of a versioned control-plane transaction.
func (r *Registry) SetAuthoritative(catalog Catalog) {
	if r == nil {
		return
	}
	r.authoritative.Store(true)
	r.current.Store(&catalog)
}

// ReplaceLocal updates the in-process snapshot without touching the legacy
// registry store. It is used while applying or rolling back a control-plane
// transaction whose durable write is handled by the control-plane store.
func (r *Registry) ReplaceLocal(catalog Catalog) {
	if r == nil {
		return
	}
	r.current.Store(&catalog)
}

func (r *Registry) Update(ctx context.Context, catalog Catalog) error {
	if r == nil {
		return errors.New("model registry is not configured")
	}
	payload, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	if len(payload) > 1<<20 {
		return errors.New("model catalog exceeds 1 MiB")
	}
	if r.store != nil {
		if err := r.store.Set(ctx, registryStoreKey, payload, 0); err != nil {
			return err
		}
	}
	r.current.Store(&catalog)
	r.nextRefresh.Store(time.Now().Add(r.refreshInterval).UnixNano())
	return nil
}

func (r *Registry) refresh(ctx context.Context) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if time.Now().UnixNano() < r.nextRefresh.Load() {
		return
	}
	r.nextRefresh.Store(time.Now().Add(r.refreshInterval).UnixNano())
	payload, found, err := r.store.Get(ctx, registryStoreKey)
	if err != nil || !found || len(payload) > 1<<20 {
		return
	}
	catalog, err := Parse(string(payload))
	if err == nil {
		r.current.Store(&catalog)
	}
}
