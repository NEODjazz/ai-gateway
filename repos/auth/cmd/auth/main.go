package main

import (
	"encoding/json"
	"log"
	"net/http"

	"ai-gateway-auth/internal/modules"
)

func main() {
	module := modules.NewAuthModule(true)

	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	http.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		var ctx modules.RequestContext
		if err := json.NewDecoder(r.Body).Decode(&ctx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := module.Handle(r.Context(), &ctx); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(ctx)
	})

	log.Println("auth listening on :8082")
	log.Fatal(http.ListenAndServe(":8082", nil))
}
