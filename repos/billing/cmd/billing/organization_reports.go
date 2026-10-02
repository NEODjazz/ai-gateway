package main

import (
	"net/http"
	"slices"
	"strings"
	"unicode"
)

// Actor metadata is trusted only after the management service secret has been
// checked. Existing service-only calls remain compatible; delegated org_admin
// calls must always carry their authenticated organization.
func managementOrganization(w http.ResponseWriter, r *http.Request) (string, bool) {
	roles := strings.Split(r.Header.Get("X-Actor-Roles"), ",")
	if slices.Contains(roles, "admin") || !slices.Contains(roles, "org_admin") {
		return "", true
	}
	org := r.Header.Get("X-Actor-Organization-ID")
	if org == "" || len(org) > 256 || strings.TrimSpace(org) != org || strings.ContainsFunc(org, unicode.IsControl) {
		http.Error(w, "authenticated organization required", http.StatusForbidden)
		return "", false
	}
	return org, true
}
