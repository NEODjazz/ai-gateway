package modelcatalog

import (
	"context"
	"sync"
	"testing"
	"time"
)

type memoryRegistryStore struct {
	mu    sync.Mutex
	value []byte
}

func (s *memoryRegistryStore) Get(context.Context, string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.value...), len(s.value) > 0, nil
}
func (s *memoryRegistryStore) Set(_ context.Context, _ string, value []byte, _ time.Duration) error {
	s.mu.Lock()
	s.value = append([]byte(nil), value...)
	s.mu.Unlock()
	return nil
}

func TestRegistrySynchronizesCatalogAcrossReplicas(t *testing.T) {
	initial, err := Parse(`{"version":"v1","unknown_model_policy":"deny","models":[{"provider":"p","model":"old"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := Parse(`{"version":"v2","unknown_model_policy":"deny","models":[{"provider":"p","model":"new"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryRegistryStore{}
	writer := NewRegistry(initial, store, time.Nanosecond)
	reader := NewRegistry(initial, store, time.Nanosecond)
	if err := writer.Update(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	if got := reader.Current(context.Background()); got.Version != "v2" {
		t.Fatalf("version=%q", got.Version)
	}
}

func TestRegistryFallsBackToInitialCatalogWithoutStore(t *testing.T) {
	initial, err := Parse(`{"version":"v1","models":[{"provider":"p","model":"m"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := NewRegistry(initial, nil, time.Second).Current(context.Background()); got.Version != "v1" {
		t.Fatalf("catalog=%+v", got)
	}
}
