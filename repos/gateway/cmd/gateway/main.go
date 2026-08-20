package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/gateway"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/redisstore"
)

func main() {
	cfg := config.Load()
	redisStore := redisstore.New(redisstore.Config{
		Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB, Prefix: cfg.Redis.Prefix,
	})

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
		Default:           cfg.Provider.Default,
		Endpoints:         cfg.Provider.Endpoints,
		GuardrailPolicies: cfg.Provider.GuardrailPolicies,
		Modules:           providerPipeline,
		CacheTTL:          time.Duration(cfg.Cache.TTLSeconds) * time.Second,
		CacheMaxBytes:     cfg.Cache.MaxBytes,
		CacheStore:        redisStore,
	})

	var rateLimits gateway.RateLimitStore = gateway.NewMemoryRateLimitStore()
	if redisStore != nil {
		rateLimits = gateway.NewRedisRateLimitStore(redisStore)
	}
	var readiness func(context.Context) error
	if redisStore != nil {
		readiness = redisStore.Ping
	}
	handler := gateway.NewHandlerWithReadiness(gatewayPipeline, llmProvider, rateLimits, readiness)
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
