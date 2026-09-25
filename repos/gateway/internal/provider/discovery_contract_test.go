package provider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoveredModelListsRequireProviderEnvelope(t *testing.T) {
	for _, test := range []struct {
		name         string
		providerType string
		payload      string
		wantErr      bool
	}{
		{name: "cohere missing", providerType: "cohere", payload: `{}`, wantErr: true},
		{name: "cohere null", providerType: "cohere", payload: `{"models":null}`, wantErr: true},
		{name: "cohere empty", providerType: "cohere", payload: `{"models":[]}`},
		{name: "compatible missing", providerType: "openai-compatible", payload: `{}`, wantErr: true},
		{name: "compatible null", providerType: "openai-compatible", payload: `{"data":null}`, wantErr: true},
		{name: "compatible error envelope", providerType: "openai-compatible", payload: `{"error":"temporarily unavailable"}`, wantErr: true},
		{name: "compatible empty", providerType: "openai-compatible", payload: `{"data":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			models, err := parseDiscoveredModels(test.providerType, []byte(test.payload))
			if (err != nil) != test.wantErr || !test.wantErr && len(models) != 0 {
				t.Fatalf("models=%v err=%v", models, err)
			}
		})
	}
}

func TestCompatibleProviderProbeRejectsMalformedModelList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected discovery path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"error":"temporarily unavailable"}`))
	}))
	t.Cleanup(server.Close)
	router := New(Config{}).(*Router)
	if _, err := router.CreateProvider(ManagedProvider{ID: "compatible", Type: "openai-compatible", BaseURL: server.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	probe, err := router.TestProvider(t.Context(), "compatible", "")
	if !errors.Is(err, ErrProviderProbeFailed) || probe.Status != "unavailable" {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
}
