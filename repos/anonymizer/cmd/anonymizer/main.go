package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"ai-gateway-anonymizer/internal/modules"
)

func main() {
	enabledRules := modules.AnonymizerRulesFromEnv()
	module := modules.NewAnonymizerModule(true, enabledRules...)
	if configPath := os.Getenv("ANONYMIZER_RULES_CONFIG_PATH"); configPath != "" {
		config, err := modules.LoadRulesConfig(configPath)
		if err != nil {
			log.Fatal(err)
		}
		module, err = modules.NewAnonymizerModuleFromConfig(true, config, enabledRules...)
		if err != nil {
			log.Fatal(err)
		}
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	http.HandleFunc("/anonymize", func(w http.ResponseWriter, r *http.Request) {
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

	log.Println("anonymizer listening on :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}
