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
	"ai-gateway-gateway/internal/controlstore"
	"ai-gateway-gateway/internal/gateway"
	"ai-gateway-gateway/internal/modelcatalog"
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
	var providerControlStore *controlstore.PostgresStore
	if cfg.Provider.ControlPlaneDSN != "" {
		providerControlStore, err = controlstore.NewPostgresStore(appCtx, cfg.Provider.ControlPlaneDSN, redisStore)
		if err != nil {
			log.Fatal(err)
		}
		defer providerControlStore.Close()
	}

	gatewayPipeline := modules.NewPipelineWithObserver([]modules.Module{
		modules.Auth(cfg.Modules.Auth.Required, cfg.Modules.Auth.URL),
	}, metrics)
	guardrailMonitor := gateway.NewGuardrailMonitorWithStore(cfg.Guardrails.Capacity, gateway.NewRedisGuardrailEventStore(redisStore, cfg.Guardrails.TTL))
	loggingRegistry := gateway.NewLoggingRegistry(nil)
	accessRegistry := gateway.NewAccessRegistry()
	mcpRegistry := gateway.NewMCPRegistry()
	agentRegistry := gateway.NewAgentRegistry()
	dlpModule := gateway.NewGuardrailMonitoringModule(modules.DLP(cfg.Modules.DLP.Required, cfg.Modules.DLP.URL), guardrailMonitor)
	avModule := gateway.NewGuardrailMonitoringModule(modules.AV(cfg.Modules.AV.Required, cfg.Modules.AV.URL), guardrailMonitor)
	providerPipeline := modules.NewPipelineWithObserver([]modules.Module{
		dlpModule,
		avModule,
		modules.Anonymizer(cfg.Modules.Anonymizer.Required, cfg.Modules.Anonymizer.URL),
		modules.BillingWithSecret(cfg.Modules.Billing.Required, cfg.Modules.Billing.URL, cfg.Modules.Billing.Secret),
		gateway.NewLoggingModule(loggingRegistry),
	}, metrics)

	modelRegistry := modelcatalog.NewRegistry(cfg.Catalog, registryStoreFor(redisStore), time.Second)
	providerConfig := provider.Config{
		Default:                 cfg.Provider.Default,
		Endpoints:               cfg.Provider.Endpoints,
		GuardrailPolicies:       cfg.Provider.GuardrailPolicies,
		Modules:                 providerPipeline,
		CacheTTL:                time.Duration(cfg.Cache.TTLSeconds) * time.Second,
		CacheMaxBytes:           cfg.Cache.MaxBytes,
		Catalog:                 cfg.Catalog,
		CatalogRegistry:         modelRegistry,
		Observer:                metrics,
		RoutingStrategy:         cfg.Provider.RoutingStrategy,
		AdaptiveEWMAAlpha:       cfg.Provider.AdaptiveEWMAAlpha,
		AffinityTTL:             cfg.Provider.AffinityTTL,
		ResponseOwnershipTTL:    cfg.Provider.ResponseOwnershipTTL,
		SemanticCacheTTL:        time.Duration(cfg.Cache.Semantic.TTLSeconds) * time.Second,
		SemanticCacheThreshold:  cfg.Cache.Semantic.Threshold,
		SemanticCacheMaxEntries: cfg.Cache.Semantic.MaxEntries,
		SemanticCacheMaxBytes:   cfg.Cache.Semantic.MaxBytes,
		SemanticEmbeddingURL:    cfg.Cache.Semantic.EmbeddingURL,
		SemanticEmbeddingAPIKey: cfg.Cache.Semantic.EmbeddingAPIKey,
		SemanticEmbeddingModel:  cfg.Cache.Semantic.EmbeddingModel,
		CredentialEncryptionKey: []byte(cfg.Provider.CredentialKey),
		ControlPlaneRefresh:     cfg.Provider.ControlPlaneRefresh,
	}
	providerConfig.ControlPlaneStore = controlPlaneStoreFor(providerControlStore)
	if providerControlStore != nil {
		providerConfig.AsyncJobs = providerControlStore
	}
	if redisStore != nil {
		providerConfig.CacheStore = redisStore
		providerConfig.SessionStore = redisStore
		providerConfig.CircuitStore = redisStore
		providerConfig.DeploymentQuotaStore = redisStore
	}
	llmProvider, err := provider.NewWithError(providerConfig)
	if err != nil {
		log.Fatal(err)
	}
	var backgroundWorkerDone <-chan struct{}
	if processor, ok := llmProvider.(provider.BackgroundResponseProcessor); ok && providerControlStore != nil {
		done := make(chan struct{})
		backgroundWorkerDone = done
		go func() {
			defer close(done)
			provider.RunBackgroundResponseWorker(appCtx, processor)
		}()
	}
	adminController, ok := llmProvider.(gateway.AdminStateController)
	if !ok {
		log.Fatal("provider does not support durable admin state")
	}
	adminState, err := gateway.NewAdminStateRuntime(appCtx, adminController, []byte(cfg.Provider.CredentialKey), accessRegistry, mcpRegistry, agentRegistry, loggingRegistry)
	if err != nil {
		log.Fatal(err)
	}

	var rateLimits gateway.RateLimitStore = gateway.NewMemoryRateLimitStore()
	if redisStore != nil {
		rateLimits = gateway.NewRedisRateLimitStore(redisStore)
	}
	var readiness func(context.Context) error
	if redisStore != nil {
		readiness = redisStore.Ping
	}
	if providerControlStore != nil {
		redisReadiness := readiness
		readiness = func(ctx context.Context) error {
			if redisReadiness != nil {
				if err := redisReadiness(ctx); err != nil {
					return err
				}
			}
			return providerControlStore.Ping(ctx)
		}
	}
	handler := gateway.NewHandlerWithMetrics(gatewayPipeline, llmProvider, rateLimits, readiness, metrics).WithModelRegistry(modelRegistry).WithComplianceModules(dlpModule, avModule).WithGuardrailMonitor(guardrailMonitor).WithCacheDiagnostics(gateway.CacheRuntimeConfig{ExactTTLSeconds: cfg.Cache.TTLSeconds, ExactMaxBytes: cfg.Cache.MaxBytes, SemanticTTLSeconds: cfg.Cache.Semantic.TTLSeconds, SemanticMaxEntries: cfg.Cache.Semantic.MaxEntries, SemanticMaxBytes: cfg.Cache.Semantic.MaxBytes}).WithLoggingRegistry(loggingRegistry).WithAgentRegistry(agentRegistry).WithMCPRegistry(mcpRegistry).WithAccessRegistry(accessRegistry).WithAdminState(adminState)
	if providerControlStore != nil {
		handler = handler.WithMCPCallStore(providerControlStore).
			WithA2ATaskStore(providerControlStore, gateway.A2ATaskRuntimeConfig{
				OwnerQuota: cfg.A2ATasks.OwnerQuota, TTL: cfg.A2ATasks.TTL, SubscriptionLimit: cfg.A2ATasks.SubscriptionLimit,
				SubscriptionDuration: cfg.A2ATasks.SubscriptionDuration, SubscriptionPoll: cfg.A2ATasks.SubscriptionPoll,
			}).
			WithFileStore(providerControlStore, gateway.FileRuntimeConfig{MaxBytes: cfg.Files.MaxBytes, OwnerQuotaBytes: cfg.Files.OwnerQuotaBytes}).
			WithBatchStore(providerControlStore, providerControlStore).
			WithFineTuningStore(providerControlStore).
			WithVideoStore(providerControlStore).
			WithSkillStore(providerControlStore).
			WithVectorStore(providerControlStore, gateway.VectorStoreRuntimeConfig{OwnerQuota: cfg.VectorStores.OwnerQuota, FileQuota: cfg.VectorStores.FileQuota})
		handler = handler.WithAssistantStore(providerControlStore, gateway.AssistantRuntimeConfig{OwnerQuota: cfg.Assistants.OwnerQuota, ThreadOwnerQuota: cfg.Assistants.ThreadOwnerQuota, MessageThreadQuota: cfg.Assistants.MessageThreadQuota})
		handler, err = handler.WithA2APushNotifications(providerControlStore, []byte(cfg.Provider.CredentialKey))
		if err != nil {
			log.Fatal(err)
		}
	}
	if cfg.APIDocs.Enabled {
		handler = handler.WithAPIDocs(cfg.APIDocs.TryItOutEnabled)
	}
	if cfg.AdminUI.Enabled {
		handler = handler.WithAdminUI()
	}
	if cfg.Management.AuthURL != "" && cfg.Management.Secret != "" {
		authManagement := gateway.NewRemoteManagementClient(cfg.Management.AuthURL, cfg.Management.Secret)
		handler = handler.WithManagement(authManagement).WithIdentityDirectory(authManagement).WithOrganizations(authManagement)
	}
	if cfg.Management.BillingURL != "" && cfg.Management.BillingSecret != "" {
		billingManagement := gateway.NewRemoteBudgetManagementClient(cfg.Management.BillingURL, cfg.Management.BillingSecret)
		handler = handler.WithBudgetManagement(billingManagement).WithUsageReporting(billingManagement).WithRequestLogs(billingManagement).WithAudit(billingManagement)
	}
	var a2aPushWorkerDone <-chan struct{}
	var batchWorkerDone <-chan struct{}
	if providerControlStore != nil {
		done := make(chan struct{})
		a2aPushWorkerDone = done
		go func() {
			defer close(done)
			gateway.RunA2APushWorker(appCtx, handler)
		}()
		batchDone := make(chan struct{})
		batchWorkerDone = batchDone
		go func() {
			defer close(batchDone)
			gateway.RunBatchWorker(appCtx, handler)
		}()
	}
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
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("gateway shutdown failed: %v", err)
	}
	if backgroundWorkerDone != nil {
		select {
		case <-backgroundWorkerDone:
		case <-shutdownCtx.Done():
			log.Printf("background response worker shutdown timed out")
		}
	}
	if a2aPushWorkerDone != nil {
		select {
		case <-a2aPushWorkerDone:
		case <-shutdownCtx.Done():
			log.Printf("A2A push worker shutdown timed out")
		}
	}
	if batchWorkerDone != nil {
		select {
		case <-batchWorkerDone:
		case <-shutdownCtx.Done():
			log.Printf("batch worker shutdown timed out")
		}
	}
	if err := shutdownTelemetry(shutdownCtx); err != nil {
		log.Printf("telemetry shutdown failed: %v", err)
	}
	if serverFailed {
		os.Exit(1)
	}
}

func registryStoreFor(store *redisstore.Store) modelcatalog.RegistryStore {
	if store == nil {
		return nil
	}
	return store
}

func controlPlaneStoreFor(store *controlstore.PostgresStore) provider.ControlPlaneStore {
	if store == nil {
		return nil
	}
	return store
}
