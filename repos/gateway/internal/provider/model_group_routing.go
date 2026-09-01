package provider

import (
	"context"
	"strings"
)

// DeploymentRoutingSettings contains only the operational fields that may be
// changed from the model-group routing workspace. Provider, credential, model,
// capability, and guardrail ownership remain under deployment management.
type DeploymentRoutingSettings struct {
	ID                    string `json:"id"`
	Priority              int    `json:"priority"`
	Weight                int    `json:"weight"`
	RequestTimeoutMS      int    `json:"request_timeout_ms,omitempty"`
	MaxRetries            int    `json:"max_retries,omitempty"`
	CooldownAfterFailures int    `json:"cooldown_after_failures,omitempty"`
	CooldownSeconds       int    `json:"cooldown_seconds,omitempty"`
	MaxParallelRequests   int    `json:"max_parallel_requests,omitempty"`
	QueueCapacity         int    `json:"queue_capacity,omitempty"`
	QueueTimeoutMS        int    `json:"queue_timeout_ms,omitempty"`
}

type ModelGroupRoutingInput struct {
	DeploymentIDs []string                    `json:"deployment_ids"`
	Strategy      string                      `json:"strategy"`
	RetryPolicy   map[string]int              `json:"retry_policy,omitempty"`
	Fallbacks     map[string][]string         `json:"fallbacks,omitempty"`
	Enabled       bool                        `json:"enabled"`
	Deployments   []DeploymentRoutingSettings `json:"deployments"`
}

type ModelGroupRoutingUpdate struct {
	ExpectedRevision int64 `json:"expected_revision"`
	ModelGroupRoutingInput
}

type ModelGroupRoutingSettings struct {
	Revision    int64             `json:"revision"`
	ModelGroup  ModelGroup        `json:"model_group"`
	Deployments []ModelDeployment `json:"deployments"`
}

type ModelGroupRoutingController interface {
	GetModelGroupRouting(context.Context, string) (ModelGroupRoutingSettings, error)
	UpdateModelGroupRouting(context.Context, string, ModelGroupRoutingUpdate) (ModelGroupRoutingSettings, error)
}

func (r *Router) GetModelGroupRouting(ctx context.Context, id string) (ModelGroupRoutingSettings, error) {
	_, unlock, err := r.beginControlMutation(ctx)
	if err != nil {
		return ModelGroupRoutingSettings{}, err
	}
	defer unlock()
	return r.modelGroupRoutingLocked(id)
}

func (r *Router) UpdateModelGroupRouting(ctx context.Context, id string, input ModelGroupRoutingUpdate) (ModelGroupRoutingSettings, error) {
	previous, unlock, err := r.beginControlMutation(ctx)
	if err != nil {
		return ModelGroupRoutingSettings{}, err
	}
	defer unlock()
	if input.ExpectedRevision != r.controlPlaneRevision() {
		return ModelGroupRoutingSettings{}, ErrControlPlaneConflict
	}
	id = strings.TrimSpace(id)
	currentGroups := r.modelGroups.current.Load()
	currentDeployments := r.deployments.current.Load()
	if currentGroups == nil || (*currentGroups)[id].ID == "" {
		return ModelGroupRoutingSettings{}, ErrModelGroupNotFound
	}
	if currentDeployments == nil || len(input.DeploymentIDs) == 0 || len(input.Deployments) != len(input.DeploymentIDs) {
		return ModelGroupRoutingSettings{}, ErrInvalidModelGroup
	}

	nextDeployments := make(map[string]ModelDeployment, len(*currentDeployments))
	for deploymentID, deployment := range *currentDeployments {
		nextDeployments[deploymentID] = deployment
	}
	memberIDs := make(map[string]bool, len(input.DeploymentIDs))
	for _, deploymentID := range input.DeploymentIDs {
		deploymentID = strings.TrimSpace(deploymentID)
		if deploymentID == "" || memberIDs[deploymentID] {
			return ModelGroupRoutingSettings{}, ErrInvalidModelGroup
		}
		memberIDs[deploymentID] = true
	}
	endpoints := make(map[string]Endpoint, len(input.Deployments))
	updated := make(map[string]bool, len(input.Deployments))
	for _, settings := range input.Deployments {
		settings.ID = strings.TrimSpace(settings.ID)
		deployment, found := nextDeployments[settings.ID]
		if !found {
			return ModelGroupRoutingSettings{}, ErrDeploymentNotFound
		}
		if !memberIDs[settings.ID] || updated[settings.ID] {
			return ModelGroupRoutingSettings{}, ErrInvalidModelGroup
		}
		updated[settings.ID] = true
		deployment.Priority = settings.Priority
		deployment.Weight = settings.Weight
		deployment.RequestTimeoutMS = settings.RequestTimeoutMS
		deployment.MaxRetries = settings.MaxRetries
		deployment.CooldownAfterFailures = settings.CooldownAfterFailures
		deployment.CooldownSeconds = settings.CooldownSeconds
		deployment.MaxParallelRequests = settings.MaxParallelRequests
		deployment.QueueCapacity = settings.QueueCapacity
		deployment.QueueTimeoutMS = settings.QueueTimeoutMS
		if deployment.Weight == 0 {
			deployment.Weight = 1
		}
		if err := r.validateDeployment(deployment); err != nil {
			return ModelGroupRoutingSettings{}, ErrInvalidDeployment
		}
		endpoint, err := r.endpointForDeployment(deployment)
		if err != nil {
			return ModelGroupRoutingSettings{}, ErrInvalidDeployment
		}
		nextDeployments[settings.ID] = deployment
		endpoints[settings.ID] = endpoint
	}
	group, err := normalizeModelGroupAgainst(ModelGroup{ID: id, DeploymentIDs: input.DeploymentIDs, Strategy: input.Strategy, RetryPolicy: input.RetryPolicy, Fallbacks: input.Fallbacks, Enabled: input.Enabled}, nextDeployments)
	if err != nil {
		return ModelGroupRoutingSettings{}, err
	}
	nextGroups := cloneModelGroups(*currentGroups)
	nextGroups[id] = group
	if err := validateModelGroupGraph(nextGroups); err != nil {
		return ModelGroupRoutingSettings{}, err
	}
	r.deployments.current.Store(&nextDeployments)
	r.modelGroups.current.Store(&nextGroups)
	for _, deploymentID := range group.DeploymentIDs {
		r.replaceRuntimeEndpoint(deploymentID, endpoints[deploymentID])
	}
	if err := r.persistControlMutation(ctx, previous); err != nil {
		return ModelGroupRoutingSettings{}, err
	}
	return r.modelGroupRoutingLocked(id)
}

func (r *Router) modelGroupRoutingLocked(id string) (ModelGroupRoutingSettings, error) {
	id = strings.TrimSpace(id)
	groups := r.modelGroups.current.Load()
	deployments := r.deployments.current.Load()
	if groups == nil || (*groups)[id].ID == "" {
		return ModelGroupRoutingSettings{}, ErrModelGroupNotFound
	}
	if deployments == nil {
		return ModelGroupRoutingSettings{}, ErrInvalidModelGroup
	}
	group := (*groups)[id]
	group.DeploymentIDs = append([]string(nil), group.DeploymentIDs...)
	group.RetryPolicy = cloneRetryPolicy(group.RetryPolicy)
	group.Fallbacks = cloneFallbacks(group.Fallbacks)
	result := ModelGroupRoutingSettings{Revision: r.controlPlaneRevision(), ModelGroup: group}
	for _, deploymentID := range group.DeploymentIDs {
		deployment, found := (*deployments)[deploymentID]
		if !found {
			return ModelGroupRoutingSettings{}, ErrInvalidModelGroup
		}
		deployment.Models = append([]string(nil), deployment.Models...)
		deployment.Capabilities = append([]string(nil), deployment.Capabilities...)
		result.Deployments = append(result.Deployments, deployment)
	}
	return result, nil
}

func (r *Router) controlPlaneRevision() int64 {
	if r == nil || r.controlPlane == nil {
		return 0
	}
	return r.controlPlane.revision
}
