package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBillingHealthDoesNotExposeReadinessErrors(t *testing.T) {
	privateError := errors.New("private database credential and request")
	for _, test := range []struct {
		name       string
		moduleErr  error
		auditErr   error
		status     int
		message    string
		checkAudit bool
	}{
		{name: "module unavailable", moduleErr: privateError, status: http.StatusServiceUnavailable, message: "billing unavailable"},
		{name: "audit unavailable", auditErr: privateError, status: http.StatusServiceUnavailable, message: "audit storage unavailable", checkAudit: true},
		{name: "ready", status: http.StatusNoContent, checkAudit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			auditCalled := false
			handler := billingHealthHandler(
				func(context.Context) error { return test.moduleErr },
				func(context.Context) error { auditCalled = true; return test.auditErr },
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if response.Code != test.status || auditCalled != test.checkAudit {
				t.Fatalf("status=%d auditCalled=%t", response.Code, auditCalled)
			}
			if body := response.Body.String(); strings.Contains(body, privateError.Error()) || (test.message != "" && !strings.Contains(body, test.message)) {
				t.Fatalf("unsafe health response: %q", body)
			}
		})
	}
}
