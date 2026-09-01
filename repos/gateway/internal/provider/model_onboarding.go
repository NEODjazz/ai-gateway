package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ai-gateway-gateway/internal/modelcatalog"
)

// ModelOnboardingInput is the complete desired model slice. Providers and
// credentials remain independently managed prerequisites, while catalog,
// deployments, and routing groups are validated and committed together.
type ModelOnboardingInput struct {
	Catalog     modelcatalog.Catalog `json:"catalog"`
	Deployments []ModelDeployment    `json:"deployments"`
	ModelGroups []ModelGroup         `json:"model_groups"`
}

type ModelOnboardingPlan struct {
	Revision       int64             `json:"revision"`
	CatalogVersion string            `json:"catalog_version"`
	Deployments    []ModelDeployment `json:"deployments"`
	ModelGroups    []ModelGroup      `json:"model_groups"`
	Changes        []string          `json:"changes"`
}

type ModelOnboardingController interface {
	PlanModelOnboarding(context.Context, ModelOnboardingInput) (ModelOnboardingPlan, error)
	ApplyModelOnboarding(context.Context, int64, ModelOnboardingInput) (ModelOnboardingPlan, error)
}

type ModelCatalogController interface {
	UpdateModelCatalog(context.Context, modelcatalog.Catalog) (int64, error)
}

var ErrInvalidModelOnboarding = errors.New("invalid model onboarding configuration")

func (r *Router) PlanModelOnboarding(ctx context.Context, input ModelOnboardingInput) (ModelOnboardingPlan, error) {
	_, unlock, err := r.beginControlMutation(ctx)
	if err != nil {
		return ModelOnboardingPlan{}, err
	}
	defer unlock()
	return r.planModelOnboardingLocked(input)
}

func (r *Router) ApplyModelOnboarding(ctx context.Context, expectedRevision int64, input ModelOnboardingInput) (ModelOnboardingPlan, error) {
	previous, unlock, err := r.beginControlMutation(ctx)
	if err != nil {
		return ModelOnboardingPlan{}, err
	}
	defer unlock()
	if r.controlPlane != nil && expectedRevision != r.controlPlane.revision {
		return ModelOnboardingPlan{}, ErrControlPlaneConflict
	}
	plan, err := r.planModelOnboardingLocked(input)
	if err != nil {
		return ModelOnboardingPlan{}, err
	}
	catalogPayload, err := json.Marshal(input.Catalog)
	if err != nil {
		return ModelOnboardingPlan{}, ErrInvalidModelOnboarding
	}
	next := previous
	next.SchemaVersion = 3
	next.ModelCatalog = catalogPayload
	next.Deployments = append(next.Deployments, plan.Deployments...)
	groups := make(map[string]ModelGroup, len(next.ModelGroups)+len(plan.ModelGroups))
	for _, group := range next.ModelGroups {
		groups[group.ID] = group
	}
	for _, group := range plan.ModelGroups {
		groups[group.ID] = group
	}
	next.ModelGroups = nil
	for _, group := range groups {
		next.ModelGroups = append(next.ModelGroups, group)
	}
	sort.Slice(next.ModelGroups, func(i, j int) bool { return next.ModelGroups[i].ID < next.ModelGroups[j].ID })
	if err := r.applyControlPlaneSnapshot(next); err != nil {
		_ = r.applyControlPlaneSnapshot(previous)
		return ModelOnboardingPlan{}, fmt.Errorf("%w: %v", ErrInvalidModelOnboarding, err)
	}
	if err := r.persistControlMutation(ctx, previous); err != nil {
		return ModelOnboardingPlan{}, err
	}
	if r.controlPlane != nil {
		plan.Revision = r.controlPlane.revision
	}
	return plan, nil
}

func (r *Router) UpdateModelCatalog(ctx context.Context, catalog modelcatalog.Catalog) (int64, error) {
	normalized, err := normalizeOnboardingCatalog(catalog)
	if err != nil {
		return 0, err
	}
	if r.controlPlane == nil || r.controlPlane.store == nil {
		if err := r.catalog.Update(ctx, normalized); err != nil {
			return 0, err
		}
		return 0, nil
	}
	previous, unlock, err := r.beginControlMutation(ctx)
	if err != nil {
		return 0, err
	}
	defer unlock()
	r.catalog.ReplaceLocal(normalized)
	if err := r.persistControlMutation(ctx, previous); err != nil {
		return 0, err
	}
	return r.controlPlane.revision, nil
}

func (r *Router) planModelOnboardingLocked(input ModelOnboardingInput) (ModelOnboardingPlan, error) {
	catalog, err := normalizeOnboardingCatalog(input.Catalog)
	if err != nil || len(input.Deployments) == 0 || len(input.Deployments) > 128 || len(input.ModelGroups) > 128 {
		return ModelOnboardingPlan{}, ErrInvalidModelOnboarding
	}
	currentDeployments := r.deployments.current.Load()
	if currentDeployments == nil {
		return ModelOnboardingPlan{}, ErrInvalidModelOnboarding
	}
	nextDeployments := make(map[string]ModelDeployment, len(*currentDeployments)+len(input.Deployments))
	for id, deployment := range *currentDeployments {
		nextDeployments[id] = deployment
	}
	normalizedDeployments := make([]ModelDeployment, 0, len(input.Deployments))
	for _, deployment := range input.Deployments {
		deployment.ID = strings.TrimSpace(deployment.ID)
		if _, exists := nextDeployments[deployment.ID]; exists {
			return ModelOnboardingPlan{}, ErrDeploymentExists
		}
		if err := r.validateDeployment(deployment); err != nil {
			return ModelOnboardingPlan{}, ErrInvalidModelOnboarding
		}
		providerConfig, found := r.managedProvider(deployment.ProviderID)
		if !found {
			return ModelOnboardingPlan{}, ErrInvalidModelOnboarding
		}
		deployment.ProviderType = providerConfig.Type
		if deployment.Weight == 0 {
			deployment.Weight = 1
		}
		deployment.Models = append([]string(nil), deployment.Models...)
		deployment.Capabilities = append([]string(nil), deployment.Capabilities...)
		if _, err := r.endpointForDeployment(deployment); err != nil {
			return ModelOnboardingPlan{}, ErrInvalidModelOnboarding
		}
		nextDeployments[deployment.ID] = deployment
		normalizedDeployments = append(normalizedDeployments, deployment)
	}
	normalizedGroups := make([]ModelGroup, 0, len(input.ModelGroups))
	seenGroups := map[string]bool{}
	for _, group := range input.ModelGroups {
		group, err = normalizeModelGroupAgainst(group, nextDeployments)
		if err != nil || seenGroups[group.ID] {
			return ModelOnboardingPlan{}, ErrInvalidModelOnboarding
		}
		seenGroups[group.ID] = true
		normalizedGroups = append(normalizedGroups, group)
	}
	nextGroups := map[string]ModelGroup{}
	if currentGroups := r.modelGroups.current.Load(); currentGroups != nil {
		nextGroups = cloneModelGroups(*currentGroups)
	}
	for _, group := range normalizedGroups {
		nextGroups[group.ID] = group
	}
	if err := validateModelGroupGraph(nextGroups); err != nil {
		return ModelOnboardingPlan{}, ErrInvalidModelOnboarding
	}
	sort.Slice(normalizedDeployments, func(i, j int) bool { return normalizedDeployments[i].ID < normalizedDeployments[j].ID })
	sort.Slice(normalizedGroups, func(i, j int) bool { return normalizedGroups[i].ID < normalizedGroups[j].ID })
	revision := int64(0)
	if r.controlPlane != nil {
		revision = r.controlPlane.revision
	}
	changes := []string{fmt.Sprintf("replace model catalog %s", catalog.Version)}
	for _, deployment := range normalizedDeployments {
		changes = append(changes, "create deployment "+deployment.ID)
	}
	for _, group := range normalizedGroups {
		changes = append(changes, "upsert model group "+group.ID)
	}
	return ModelOnboardingPlan{Revision: revision, CatalogVersion: catalog.Version, Deployments: normalizedDeployments, ModelGroups: normalizedGroups, Changes: changes}, nil
}

func normalizeOnboardingCatalog(input modelcatalog.Catalog) (modelcatalog.Catalog, error) {
	payload, err := json.Marshal(input)
	if err != nil || len(payload) > 1<<20 {
		return modelcatalog.Catalog{}, ErrInvalidModelOnboarding
	}
	catalog, err := modelcatalog.Parse(string(payload))
	if err != nil || strings.TrimSpace(catalog.Version) == "" || len(catalog.Version) > 128 || catalog.Models == nil || len(catalog.Models) > 5000 {
		return modelcatalog.Catalog{}, ErrInvalidModelOnboarding
	}
	allowedCapabilities := map[string]bool{"chat": true, "responses": true, "embeddings": true, "rerank": true, "stream": true, "tools": true, "structured_output": true, "mcp": true, "vision": true}
	for _, entry := range catalog.Models {
		if len(entry.Provider) > 256 || len(entry.Model) > 256 {
			return modelcatalog.Catalog{}, ErrInvalidModelOnboarding
		}
		seen := map[string]bool{}
		for _, capability := range entry.Capabilities {
			if !allowedCapabilities[capability] || seen[capability] {
				return modelcatalog.Catalog{}, ErrInvalidModelOnboarding
			}
			seen[capability] = true
		}
		if entry.Currency != "" && (len(entry.Currency) != 3 || entry.Currency != strings.ToUpper(entry.Currency)) || (entry.InputCostPer1M > 0 || entry.OutputCostPer1M > 0) && entry.Currency == "" {
			return modelcatalog.Catalog{}, ErrInvalidModelOnboarding
		}
	}
	return catalog, nil
}
