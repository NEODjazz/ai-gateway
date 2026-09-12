package controlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-gateway-gateway/internal/skillstate"
)

func TestPostgresSkillOwnershipIsolationIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	ctx := context.Background()
	store, err := NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	prepareSkillOwnershipTable(t, store)
	owner := "skill-owner/" + time.Now().UTC().Format("20060102150405.000000000")
	other := owner + "/other"
	for _, id := range []string{"skill_integration_a", "skill_integration_b"} {
		t.Cleanup(func() {
			_, _ = store.pool.Exec(context.Background(), `DELETE FROM gateway_skill_ownership WHERE skill_id=$1`, id)
		})
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM gateway_skill_executions WHERE container_id=$1`, "container_integration_a")
	})
	claimed, err := store.ClaimSkill(ctx, skillstate.Ownership{SkillID: "skill_integration_a", OwnerKey: owner, EndpointID: "anthropic-a"})
	if err != nil || claimed.CreatedAt.IsZero() {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if _, err := store.ResolveSkill(ctx, owner, claimed.SkillID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveSkill(ctx, other, claimed.SkillID); !errors.Is(err, skillstate.ErrNotFound) {
		t.Fatalf("cross-owner resolve error=%v", err)
	}
	if _, err := store.ClaimSkill(ctx, skillstate.Ownership{SkillID: claimed.SkillID, OwnerKey: other, EndpointID: "anthropic-b"}); !errors.Is(err, skillstate.ErrConflict) {
		t.Fatalf("global collision error=%v", err)
	}
	if _, err := store.ClaimSkill(ctx, skillstate.Ownership{SkillID: "skill_integration_b", OwnerKey: owner, EndpointID: "anthropic-b"}); err != nil {
		t.Fatal(err)
	}
	owned, err := store.OwnedSkills(ctx, owner, "anthropic-a", []string{claimed.SkillID, "skill_integration_b", "missing"})
	if err != nil || len(owned) != 1 || !owned[claimed.SkillID] {
		t.Fatalf("owned=%v err=%v", owned, err)
	}
	if err := store.DeleteSkill(ctx, other, claimed.SkillID); !errors.Is(err, skillstate.ErrNotFound) {
		t.Fatalf("cross-owner delete error=%v", err)
	}
	if err := store.DeleteSkill(ctx, owner, claimed.SkillID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveSkill(ctx, owner, claimed.SkillID); !errors.Is(err, skillstate.ErrNotFound) {
		t.Fatalf("deleted resolve error=%v", err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	if _, err := store.pool.Exec(ctx, `INSERT INTO gateway_skill_executions (container_id,owner_key,endpoint_id,expires_at) VALUES ('container_expired',$1,'anthropic-a',now()-interval '1 minute')`, owner); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSkillExecution(ctx, skillstate.Execution{ContainerID: "container_integration_a", OwnerKey: owner, EndpointID: "anthropic-a", ExpiresAt: expiresAt}); err != nil {
		t.Fatal(err)
	}
	execution, err := store.ResolveSkillExecution(ctx, owner, "container_integration_a")
	if err != nil || execution.EndpointID != "anthropic-a" || execution.ExpiresAt.IsZero() {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	if _, err := store.ResolveSkillExecution(ctx, other, "container_integration_a"); !errors.Is(err, skillstate.ErrNotFound) {
		t.Fatalf("cross-owner execution error=%v", err)
	}
	if err := store.SaveSkillExecution(ctx, skillstate.Execution{ContainerID: "container_integration_a", OwnerKey: other, EndpointID: "anthropic-a", ExpiresAt: expiresAt}); !errors.Is(err, skillstate.ErrConflict) {
		t.Fatalf("execution collision error=%v", err)
	}
	var expired int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM gateway_skill_executions WHERE container_id='container_expired'`).Scan(&expired); err != nil || expired != 0 {
		t.Fatalf("expired execution rows=%d err=%v", expired, err)
	}
}

func prepareSkillOwnershipTable(t *testing.T, store *PostgresStore) {
	t.Helper()
	_, err := store.pool.Exec(t.Context(), `CREATE TABLE gateway_skill_ownership (
		skill_id TEXT PRIMARY KEY, owner_key TEXT NOT NULL, endpoint_id TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Fatalf("create isolated skill ownership table: %v", err)
	}
	_, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_skill_executions (
		container_id TEXT PRIMARY KEY, owner_key TEXT NOT NULL, endpoint_id TEXT NOT NULL,
		expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Fatalf("create isolated skill execution table: %v", err)
	}
}

func TestPostgresSkillOwnershipRejectsInvalidInput(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(context.Background(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if _, err := store.ClaimSkill(context.Background(), skillstate.Ownership{}); !errors.Is(err, skillstate.ErrInvalid) {
		t.Fatalf("claim error=%v", err)
	}
	if _, err := store.OwnedSkills(context.Background(), "owner", "endpoint", make([]string, 1001)); !errors.Is(err, skillstate.ErrInvalid) {
		t.Fatalf("owned error=%v", err)
	}
	if err := store.SaveSkillExecution(context.Background(), skillstate.Execution{}); !errors.Is(err, skillstate.ErrInvalid) {
		t.Fatalf("save execution error=%v", err)
	}
	if err := store.SaveSkillExecution(context.Background(), skillstate.Execution{ContainerID: "container", OwnerKey: "owner", EndpointID: "endpoint", ExpiresAt: time.Now().Add(-time.Minute)}); !errors.Is(err, skillstate.ErrInvalid) {
		t.Fatalf("expired execution error=%v", err)
	}
}
