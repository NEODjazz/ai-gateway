package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-dlp/internal/modules"
)

type fakeScanner struct {
	calls int
	err   error
	ready error
}

func (s *fakeScanner) Scan(context.Context, string, []byte) (modules.ICAPScanResult, error) {
	s.calls++
	return modules.ICAPScanResult{}, s.err
}

func (s *fakeScanner) Ready(context.Context) error { return s.ready }

func TestDLPHealth(t *testing.T) {
	response := httptest.NewRecorder()
	newHandler(&fakeScanner{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("unexpected health status: %d", response.Code)
	}
}

func TestDLPReadinessReflectsICAPDependency(t *testing.T) {
	for name, test := range map[string]struct {
		err    error
		status int
	}{
		"ready":       {status: http.StatusNoContent},
		"unavailable": {err: errors.New("connection refused"), status: http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			newHandler(&fakeScanner{ready: test.err}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if response.Code != test.status || strings.Contains(response.Body.String(), "connection refused") {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestDLPScanRejectsMalformedJSON(t *testing.T) {
	response := httptest.NewRecorder()
	newHandler(&fakeScanner{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader("{")))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected malformed request status: %d", response.Code)
	}
}

func TestDLPScanAllowsEmptyAndCleanContent(t *testing.T) {
	for name, content := range map[string]string{"empty": "   ", "clean": "public text"} {
		t.Run(name, func(t *testing.T) {
			scanner := &fakeScanner{}
			response := httptest.NewRecorder()
			body := `{"request_id":"req-1","content":"` + content + `"}`
			newHandler(scanner).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader(body)))
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"allowed":true`) {
				t.Fatalf("unexpected allow response: status=%d body=%s", response.Code, response.Body.String())
			}
			if name == "empty" && scanner.calls != 0 || name == "clean" && scanner.calls != 1 {
				t.Fatalf("unexpected scanner calls: %d", scanner.calls)
			}
		})
	}
}

func TestDLPScanMapsPolicyRejectionTo451(t *testing.T) {
	scanner := &fakeScanner{err: errors.Join(modules.ErrContentRejected, errors.New("test-policy"))}
	response := httptest.NewRecorder()
	newHandler(scanner).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader(`{"content":"secret"}`)))
	if response.Code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("unexpected rejection status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDLPScanMapsICAPFailureTo502(t *testing.T) {
	scanner := &fakeScanner{err: errors.New("icap unavailable")}
	response := httptest.NewRecorder()
	newHandler(scanner).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/scan", strings.NewReader(`{"content":"secret"}`)))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("unexpected upstream failure status=%d body=%s", response.Code, response.Body.String())
	}
}
