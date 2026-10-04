package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/controlstore"
	"ai-gateway-gateway/internal/provider"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresAgentConfigurationEncryptedRestoreIntegration(t *testing.T) {
	dsn := os.Getenv("CONTROL_PLANE_POSTGRES_TEST_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_INTEGRATION_REQUIRED") == "true" {
			t.Fatal("CONTROL_PLANE_POSTGRES_TEST_DSN is required")
		}
		t.Skip("CONTROL_PLANE_POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not connect to the dedicated test database")
	}
	t.Cleanup(admin.Close)
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "agent_configuration_test_" + hex.EncodeToString(random)
	identifier := `"` + schema + `"`
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal("could not create isolated agent test schema")
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupContext, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error("could not remove owned agent test schema")
		}
	})
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("invalid test database URL")
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=" + schema
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("could not connect to isolated schema")
	}
	t.Cleanup(pool.Close)
	var actualSchema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&actualSchema); err != nil || actualSchema != schema {
		t.Fatal("test database scope did not isolate the schema")
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE gateway_control_plane_state (singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton), revision BIGINT NOT NULL DEFAULT 0 CHECK (revision>=0), payload JSONB NOT NULL DEFAULT '{}'::jsonb, updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatal("could not prepare isolated state table")
	}
	store, err := controlstore.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal("could not open test control store")
	}
	t.Cleanup(store.Close)
	key := []byte("agent-configuration-integration-key")
	freshRuntime := func() (*AdminStateRuntime, *AgentRegistry) {
		t.Helper()
		llm, err := provider.NewWithError(provider.Config{ControlPlaneStore: store, CredentialEncryptionKey: key})
		if err != nil {
			t.Fatal(err)
		}
		controller, ok := llm.(AdminStateController)
		if !ok {
			t.Fatal("provider lacks admin state controller")
		}
		agents := NewAgentRegistry()
		runtime, err := NewAdminStateRuntime(ctx, controller, key, NewAccessRegistry(), NewMCPRegistry(), agents, newLoggingRegistryWithoutWorker(nil))
		if err != nil {
			t.Fatal(err)
		}
		return runtime, agents
	}
	runtime, agents := freshRuntime()
	if _, err := agents.PutToolPolicy("safe", ToolPolicy{Name: "Safe", AllowedTools: []string{"lookup"}, MaxToolCalls: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	router := Routes(NewHandler(modulesPipeline("admin"), nil).WithAgentRegistry(agents).WithAdminState(runtime))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/admin/v1/agent-profiles/research", strings.NewReader(`{"name":"Research","model":"test-model","tool_policy_id":"safe","max_iterations":3,"enabled":true,"instructions":"`+agentFixtureInstructions+`","generation":{"temperature":0,"max_output_tokens":127},"mcp_tools":[{"server_id":"weather","tool_name":"lookup"}]}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("durable profile update failed: status=%d", response.Code)
	}
	var persisted string
	if err := pool.QueryRow(ctx, `SELECT payload::text FROM gateway_control_plane_state WHERE singleton=TRUE`).Scan(&persisted); err != nil {
		t.Fatal("could not read persisted state")
	}
	if strings.Contains(persisted, agentFixtureInstructions) || !strings.Contains(persisted, "agent_instructions") {
		t.Fatal("PostgreSQL did not persist encrypted configuration")
	}
	_, restored := freshRuntime()
	saved, ok := restored.AgentProfile("research")
	if len(saved.MCPTools) != 1 || saved.MCPTools[0].ServerID != "weather" || saved.MCPTools[0].ToolName != "lookup" {
		t.Fatal("fresh replica lost MCP bindings")
	}
	if !ok || saved.Instructions != agentFixtureInstructions || saved.Generation == nil || *saved.Generation.Temperature != 0 || *saved.Generation.MaxOutputTokens != 127 {
		t.Fatal("fresh replica lost executable configuration")
	}
}
