package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeReadiness struct{ err error }

func (f fakeReadiness) Ready(context.Context) error { return f.err }

func TestAVReadinessReflectsICAPDependency(t *testing.T) {
	for name, test := range map[string]struct {
		err    error
		status int
	}{
		"ready":       {status: http.StatusNoContent},
		"unavailable": {err: errors.New("connection refused"), status: http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			readinessHandler(fakeReadiness{err: test.err}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if response.Code != test.status || strings.Contains(response.Body.String(), "connection refused") {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestValidImageSignature(t *testing.T) {
	if !validImageSignature("image/png", []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("valid PNG signature rejected")
	}
	if validImageSignature("image/png", []byte("not-a-png")) {
		t.Fatal("spoofed PNG accepted")
	}
	if validImageSignature("application/octet-stream", []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("unsupported media type accepted")
	}
}
