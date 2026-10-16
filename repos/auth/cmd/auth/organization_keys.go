package main

import (
	"net/http"
	"slices"
	"strings"
)

func managementKeyMutationAuthorized(secret string, next http.HandlerFunc) http.HandlerFunc {
	return managementAuthorized(secret, func(w http.ResponseWriter, r *http.Request) {
		roles := r.Header.Get("X-Actor-Roles")
		if roles != "" && !slices.Contains(strings.Split(roles, ","), "admin") {
			http.Error(w, "platform administrator required", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}
