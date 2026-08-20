package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/gateway"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/redisstore"
	"ai-gateway-gateway/internal/telemetry"
)

func main() {
	cfg := config.Load()
	if cfg.InitErr != nil {
		log.Fatal(cfg.InitErr)
	}
	appCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownTelemetry, err := telemetry.Setup(appCtx, telemetry.Config{
		ServiceName: cfg.Telemetry.ServiceName, Version: cfg.Telemetry.Version,
		Endpoint: cfg.Telemetry.Endpoint, SampleRatio: cfg.Telemetry.SampleRatio,
	})
	if err != nil {
		log.Fatal(err)
	}
	metrics := gateway.NewMetrics()
	redisStore := redisstore.New(redisstore.Config{
		Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB, Prefix: cfg.Redis.Prefix,
	})

	gatewayPipeline := modules.NewPipelineWithObserver([]modules.Module{
		modules.Auth(cfg.Modules.Auth.Required, cfg.Modules.Auth.URL),
	}, metrics)
	providerPipeline := modules.NewPipelineWithObserver([]modules.Module{
		modules.DLP(cfg.Modules.DLP.Required, cfg.Modules.DLP.URL),
		modules.AV(cfg.Modules.AV.Required, cfg.Modules.AV.URL),
		modules.Anonymizer(cfg.Modules.Anonymizer.Required, cfg.Modules.Anonymizer.URL),
		modules.Billing(cfg.Modules.Billing.Required, cfg.Modules.Billing.URL),
	}, metrics)

	llmProvider := provider.New(provider.Config{
		Default:           cfg.Provider.Default,
		Endpoints:         cfg.Provider.Endpoints,
		GuardrailPolicies: cfg.Provider.GuardrailPolicies,
		Modules:           providerPipeline,
		CacheTTL:          time.Duration(cfg.Cache.TTLSeconds) * time.Second,
		CacheMaxBytes:     cfg.Cache.MaxBytes,
		CacheStore:        redisStore,
		Catalog:           cfg.Catalog,
		Observer:          metrics,
	})

	var rateLimits gateway.RateLimitStore = gateway.NewMemoryRateLimitStore()
	if redisStore != nil {
		rateLimits = gateway.NewRedisRateLimitStore(redisStore)
	}
	var readiness func(context.Context) error
	if redisStore != nil {
		readiness = redisStore.Ping
	}
	handler := gateway.NewHandlerWithMetrics(gatewayPipeline, llmProvider, rateLimits, readiness, metrics)
	server := &http.Server{
		Addr:    cfg.HTTP.Addr,
		Handler: gateway.Routes(handler),
	}

	serverErrors := make(chan error, 1)
	serverFailed := false
	go func() {
		log.Printf("ai-gateway listening on %s", cfg.HTTP.Addr)
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case serverErr := <-serverErrors:
		if serverErr != nil && serverErr != http.ErrServerClosed {
			log.Printf("gateway server failed: %v", serverErr)
			serverFailed = true
		}
	case <-appCtx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("gateway shutdown failed: %v", err)
	}
	if err := shutdownTelemetry(shutdownCtx); err != nil {
		log.Printf("telemetry shutdown failed: %v", err)
	}
	if serverFailed {
		os.Exit(1)
	}
}
