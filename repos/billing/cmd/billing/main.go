package main

import (
	"encoding/json"
	"log"
	"net/http"

	"ai-gateway-billing/internal/modules"
)

func main() {
	module := modules.NewBillingModule(true)

	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	http.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		var ctx modules.RequestContext
		if err := json.NewDecoder(r.Body).Decode(&ctx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := module.Handle(r.Context(), &ctx); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(ctx)
	})

	log.Println("billing listening on :8083")
	log.Fatal(http.ListenAndServe(":8083", nil))
}
