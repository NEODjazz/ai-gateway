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
	defer store.Close()
	owner := "skill-owner/" + time.Now().UTC().Format("20060102150405.000000000")
	other := owner + "/other"
	for _, id := range []string{"skill_integration_a", "skill_integration_b"} {
		t.Cleanup(func() {
			_, _ = store.pool.Exec(context.Background(), `DELETE FROM gateway_skill_ownership WHERE skill_id=$1`, id)
		})
	}
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
}

func TestPostgresSkillOwnershipRejectsInvalidInput(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(context.Background(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.ClaimSkill(context.Background(), skillstate.Ownership{}); !errors.Is(err, skillstate.ErrInvalid) {
		t.Fatalf("claim error=%v", err)
	}
	if _, err := store.OwnedSkills(context.Background(), "owner", "endpoint", make([]string, 1001)); !errors.Is(err, skillstate.ErrInvalid) {
		t.Fatalf("owned error=%v", err)
	}
}
