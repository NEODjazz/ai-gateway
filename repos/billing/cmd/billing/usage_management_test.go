package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-billing/internal/modules"
)

type fakeUsageReporter struct{ days int }

func (f *fakeUsageReporter) Report(_ context.Context, days int) (modules.UsageReport, error) {
	f.days = days
	return modules.UsageReport{Days: days, Totals: []modules.UsageAggregate{{Currency: "USD", Requests: 2}}}, nil
}

func TestUsageManagementRequiresSecretAndBoundsRange(t *testing.T) {
	reporter := &fakeUsageReporter{}
	mux := http.NewServeMux()
	registerUsageManagement(mux, reporter, nil, "secret")
	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/internal/v1/usage/report", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	invalidRequest := httptest.NewRequest(http.MethodGet, "/internal/v1/usage/report?days=365", nil)
	invalidRequest.Header.Set("X-Management-Token", "secret")
	invalid := httptest.NewRecorder()
	mux.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d", invalid.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/usage/report?days=7", nil)
	request.Header.Set("X-Management-Token", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || reporter.days != 7 || !strings.Contains(response.Body.String(), `"requests":2`) {
		t.Fatalf("status=%d days=%d body=%s", response.Code, reporter.days, response.Body.String())
	}
}
