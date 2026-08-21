package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"ai-gateway-anonymizer/internal/modules"
	"ai-gateway-anonymizer/internal/openai"
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

	log.Println("anonymizer listening on :8081")
	log.Fatal(http.ListenAndServe(":8081", newHandler(module)))
}

func newHandler(module modules.AnonymizerModule) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/anonymize", func(w http.ResponseWriter, r *http.Request) {
		var request anonymizeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := modules.RequestContext{Request: openai.ChatCompletionRequest{Messages: request.Messages}}
		if request.Input != nil || request.Instructions != "" {
			ctx.ResponseRequest = &openai.ResponseRequest{Input: request.Input, Instructions: request.Instructions}
		}
		if err := module.Handle(r.Context(), &ctx); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response := anonymizeResponse{Messages: ctx.Request.Messages, Replacements: ctx.AnonymizationValues}
		if ctx.ResponseRequest != nil {
			response.Input = ctx.ResponseRequest.Input
			response.Instructions = ctx.ResponseRequest.Instructions
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
	return mux
}

type anonymizeRequest struct {
	RequestID    string           `json:"request_id,omitempty"`
	Messages     []openai.Message `json:"messages,omitempty"`
	Input        any              `json:"input,omitempty"`
	Instructions string           `json:"instructions,omitempty"`
}

type anonymizeResponse struct {
	Messages     []openai.Message  `json:"messages,omitempty"`
	Input        any               `json:"input,omitempty"`
	Instructions string            `json:"instructions,omitempty"`
	Replacements map[string]string `json:"replacements,omitempty"`
}
