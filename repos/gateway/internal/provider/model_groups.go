package provider

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
)

type ModelGroup struct {
	ID            string              `json:"id"`
	DeploymentIDs []string            `json:"deployment_ids"`
	Strategy      string              `json:"strategy"`
	RetryPolicy   map[string]int      `json:"retry_policy,omitempty"`
	Fallbacks     map[string][]string `json:"fallbacks,omitempty"`
	Enabled       bool                `json:"enabled"`
}

type ModelGroupController interface {
	ListModelGroups(context.Context) []ModelGroup
	CreateModelGroup(ModelGroup) (ModelGroup, error)
	UpdateModelGroup(string, ModelGroup) (ModelGroup, error)
	DeleteModelGroup(string) error
}

type ModelFallbackResolver interface {
	ModelFallbackTargets(context.Context, string) []string
}

var (
	ErrModelGroupNotFound = errors.New("model group not found")
	ErrModelGroupExists   = errors.New("model group already exists")
	ErrModelGroupInUse    = errors.New("model group is used by a fallback chain")
	ErrInvalidModelGroup  = errors.New("invalid model group")
)

const (
	FallbackGeneral       = "general"
	FallbackContextWindow = "context_window"
	FallbackContentPolicy = "content_policy"
)

var fallbackTypes = []string{FallbackContextWindow, FallbackContentPolicy, FallbackGeneral}

type modelGroupRegistry struct {
	current atomic.Pointer[map[string]ModelGroup]
}

func (r *Router) ListModelGroups(ctx context.Context) []ModelGroup {
	_ = r.refreshControlPlane(ctx)
	if r == nil || r.modelGroups == nil || r.modelGroups.current.Load() == nil {
		return nil
	}
	current := r.modelGroups.current.Load()
	result := make([]ModelGroup, 0, len(*current))
	for _, group := range *current {
		group.DeploymentIDs = append([]string(nil), group.DeploymentIDs...)
		group.RetryPolicy = cloneRetryPolicy(group.RetryPolicy)
		group.Fallbacks = cloneFallbacks(group.Fallbacks)
		result = append(result, group)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *Router) CreateModelGroup(input ModelGroup) (ModelGroup, error) {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return ModelGroup{}, err
	}
	defer unlock()
	group, err := r.normalizeModelGroup(input)
	if err != nil {
		return ModelGroup{}, err
	}
	current := r.modelGroups.current.Load()
	if _, found := (*current)[group.ID]; found {
		return ModelGroup{}, ErrModelGroupExists
	}
	next := cloneModelGroups(*current)
	next[group.ID] = group
	if err := validateModelGroupGraph(next); err != nil {
		return ModelGroup{}, err
	}
	r.modelGroups.current.Store(&next)
	if err := r.persistControlMutation(context.Background(), previous); err != nil {
		return ModelGroup{}, err
	}
	return group, nil
}

func (r *Router) UpdateModelGroup(id string, input ModelGroup) (ModelGroup, error) {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return ModelGroup{}, err
	}
	defer unlock()
	id = strings.TrimSpace(id)
	current := r.modelGroups.current.Load()
	if _, found := (*current)[id]; !found {
		return ModelGroup{}, ErrModelGroupNotFound
	}
	input.ID = id
	group, err := r.normalizeModelGroup(input)
	if err != nil {
		return ModelGroup{}, err
	}
	next := cloneModelGroups(*current)
	next[id] = group
	if err := validateModelGroupGraph(next); err != nil {
		return ModelGroup{}, err
	}
	r.modelGroups.current.Store(&next)
	if err := r.persistControlMutation(context.Background(), previous); err != nil {
		return ModelGroup{}, err
	}
	return group, nil
}

func (r *Router) DeleteModelGroup(id string) error {
	previous, unlock, err := r.beginControlMutation(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	id = strings.TrimSpace(id)
	current := r.modelGroups.current.Load()
	if _, found := (*current)[id]; !found {
		return ErrModelGroupNotFound
	}
	for _, group := range *current {
		for _, targets := range group.Fallbacks {
			if containsDeployment(targets, id) {
				return ErrModelGroupInUse
			}
		}
	}
	next := cloneModelGroups(*current)
	delete(next, id)
	r.modelGroups.current.Store(&next)
	return r.persistControlMutation(context.Background(), previous)
}

func (r *Router) normalizeModelGroup(input ModelGroup) (ModelGroup, error) {
	deployments := r.deployments.current.Load()
	if deployments == nil {
		return ModelGroup{}, ErrInvalidModelGroup
	}
	return normalizeModelGroupAgainst(input, *deployments)
}

func normalizeModelGroupAgainst(input ModelGroup, deployments map[string]ModelDeployment) (ModelGroup, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Strategy = strings.ToLower(strings.TrimSpace(input.Strategy))
	if input.Strategy == "" {
		input.Strategy = "weighted"
	}
	if input.ID == "" || len(input.ID) > 256 || len(input.DeploymentIDs) == 0 || len(input.DeploymentIDs) > 128 || (input.Strategy != "weighted" && input.Strategy != "adaptive") {
		return ModelGroup{}, ErrInvalidModelGroup
	}
	seen := map[string]bool{}
	for index, id := range input.DeploymentIDs {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 128 || seen[id] {
			return ModelGroup{}, ErrInvalidModelGroup
		}
		if _, found := deployments[id]; !found {
			return ModelGroup{}, ErrInvalidModelGroup
		}
		seen[id] = true
		input.DeploymentIDs[index] = id
	}
	input.DeploymentIDs = append([]string(nil), input.DeploymentIDs...)
	allowedRetryClasses := map[string]bool{"timeout": true, "unavailable": true, "rate_limit": true, "unknown": true}
	for class, retries := range input.RetryPolicy {
		if !allowedRetryClasses[class] || retries < 0 || retries > 10 {
			return ModelGroup{}, ErrInvalidModelGroup
		}
	}
	input.RetryPolicy = cloneRetryPolicy(input.RetryPolicy)
	fallbacks, err := normalizeModelGroupFallbacks(input.ID, input.Fallbacks)
	if err != nil {
		return ModelGroup{}, err
	}
	input.Fallbacks = fallbacks
	return input, nil
}

func normalizeModelGroupFallbacks(source string, fallbacks map[string][]string) (map[string][]string, error) {
	if len(fallbacks) == 0 {
		return nil, nil
	}
	if len(fallbacks) > len(fallbackTypes) {
		return nil, ErrInvalidModelGroup
	}
	normalized := make(map[string][]string, len(fallbacks))
	allowed := map[string]bool{FallbackGeneral: true, FallbackContextWindow: true, FallbackContentPolicy: true}
	for fallbackType, targets := range fallbacks {
		fallbackType = strings.ToLower(strings.TrimSpace(fallbackType))
		if !allowed[fallbackType] || len(targets) == 0 || len(targets) > 32 {
			return nil, ErrInvalidModelGroup
		}
		seen := make(map[string]bool, len(targets))
		for _, target := range targets {
			target = strings.TrimSpace(target)
			if target == "" || len(target) > 256 || target == source || seen[target] {
				return nil, ErrInvalidModelGroup
			}
			seen[target] = true
			normalized[fallbackType] = append(normalized[fallbackType], target)
		}
	}
	return normalized, nil
}

func validateModelGroupGraph(groups map[string]ModelGroup) error {
	for _, group := range groups {
		for _, targets := range group.Fallbacks {
			for _, target := range targets {
				if _, found := groups[target]; !found {
					return ErrInvalidModelGroup
				}
			}
		}
	}
	visiting := make(map[string]bool, len(groups))
	visited := make(map[string]bool, len(groups))
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return false
		}
		if visited[id] {
			return true
		}
		visiting[id] = true
		group := groups[id]
		for _, targets := range group.Fallbacks {
			for _, target := range targets {
				if !visit(target) {
					return false
				}
			}
		}
		delete(visiting, id)
		visited[id] = true
		return true
	}
	for id := range groups {
		if !visit(id) {
			return ErrInvalidModelGroup
		}
	}
	return nil
}

func (r Router) modelGroup(model string) (ModelGroup, bool) {
	if r.modelGroups == nil || r.modelGroups.current.Load() == nil {
		return ModelGroup{}, false
	}
	group, found := (*r.modelGroups.current.Load())[model]
	return group, found && group.Enabled
}

func (r *Router) ModelFallbackTargets(ctx context.Context, model string) []string {
	_ = r.refreshControlPlane(ctx)
	group, found := r.modelGroup(strings.TrimSpace(model))
	if !found {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, fallbackType := range fallbackTypes {
		for _, target := range group.Fallbacks[fallbackType] {
			if !seen[target] {
				seen[target] = true
				result = append(result, target)
			}
		}
	}
	return result
}

func cloneModelGroups(current map[string]ModelGroup) map[string]ModelGroup {
	next := make(map[string]ModelGroup, len(current))
	for key, value := range current {
		value.DeploymentIDs = append([]string(nil), value.DeploymentIDs...)
		value.RetryPolicy = cloneRetryPolicy(value.RetryPolicy)
		value.Fallbacks = cloneFallbacks(value.Fallbacks)
		next[key] = value
	}
	return next
}

func cloneFallbacks(value map[string][]string) map[string][]string {
	if len(value) == 0 {
		return nil
	}
	result := make(map[string][]string, len(value))
	for fallbackType, targets := range value {
		result[fallbackType] = append([]string(nil), targets...)
	}
	return result
}

func cloneRetryPolicy(value map[string]int) map[string]int {
	if len(value) == 0 {
		return nil
	}
	result := make(map[string]int, len(value))
	for key, retries := range value {
		result[key] = retries
	}
	return result
}
