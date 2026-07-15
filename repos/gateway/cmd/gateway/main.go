package main

import (
	"log"
	"net/http"
	"os"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/gateway"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

func main() {
	cfg := config.Load()

	gatewayPipeline := modules.NewPipeline([]modules.Module{
		modules.Auth(cfg.Modules.Auth.Required, cfg.Modules.Auth.URL),
	})
	providerPipeline := modules.NewPipeline([]modules.Module{
		modules.DLP(cfg.Modules.DLP.Required, cfg.Modules.DLP.URL),
		modules.AV(cfg.Modules.AV.Required, cfg.Modules.AV.URL),
		modules.Anonymizer(cfg.Modules.Anonymizer.Required, cfg.Modules.Anonymizer.URL),
		modules.Billing(cfg.Modules.Billing.Required, cfg.Modules.Billing.URL),
	})

	llmProvider := provider.New(provider.Config{
		Default:   cfg.Provider.Default,
		Endpoints: cfg.Provider.Endpoints,
		Modules:   providerPipeline,
	})

	handler := gateway.NewHandler(gatewayPipeline, llmProvider)
	server := &http.Server{
		Addr:    cfg.HTTP.Addr,
		Handler: gateway.Routes(handler),
	}

	log.Printf("ai-gateway listening on %s", cfg.HTTP.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Println(err)
		os.Exit(1)
	}
}
