package provider

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
)

type ModelGroup struct {
	ID            string   `json:"id"`
	DeploymentIDs []string `json:"deployment_ids"`
	Strategy      string   `json:"strategy"`
	Enabled       bool     `json:"enabled"`
}

type ModelGroupController interface {
	ListModelGroups(context.Context) []ModelGroup
	CreateModelGroup(ModelGroup) (ModelGroup, error)
	UpdateModelGroup(string, ModelGroup) (ModelGroup, error)
	DeleteModelGroup(string) error
}

var (
	ErrModelGroupNotFound = errors.New("model group not found")
	ErrModelGroupExists   = errors.New("model group already exists")
	ErrInvalidModelGroup  = errors.New("invalid model group")
)

type modelGroupRegistry struct {
	current atomic.Pointer[map[string]ModelGroup]
}

func (r *Router) ListModelGroups(context.Context) []ModelGroup {
	if r == nil || r.modelGroups == nil || r.modelGroups.current.Load() == nil {
		return nil
	}
	current := r.modelGroups.current.Load()
	result := make([]ModelGroup, 0, len(*current))
	for _, group := range *current {
		group.DeploymentIDs = append([]string(nil), group.DeploymentIDs...)
		result = append(result, group)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *Router) CreateModelGroup(input ModelGroup) (ModelGroup, error) {
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
	r.modelGroups.current.Store(&next)
	return group, nil
}

func (r *Router) UpdateModelGroup(id string, input ModelGroup) (ModelGroup, error) {
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
	r.modelGroups.current.Store(&next)
	return group, nil
}

func (r *Router) DeleteModelGroup(id string) error {
	id = strings.TrimSpace(id)
	current := r.modelGroups.current.Load()
	if _, found := (*current)[id]; !found {
		return ErrModelGroupNotFound
	}
	next := cloneModelGroups(*current)
	delete(next, id)
	r.modelGroups.current.Store(&next)
	return nil
}

func (r *Router) normalizeModelGroup(input ModelGroup) (ModelGroup, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Strategy = strings.ToLower(strings.TrimSpace(input.Strategy))
	if input.Strategy == "" {
		input.Strategy = "weighted"
	}
	if input.ID == "" || len(input.ID) > 256 || len(input.DeploymentIDs) == 0 || len(input.DeploymentIDs) > 128 || (input.Strategy != "weighted" && input.Strategy != "adaptive") {
		return ModelGroup{}, ErrInvalidModelGroup
	}
	deployments := r.deployments.current.Load()
	seen := map[string]bool{}
	for index, id := range input.DeploymentIDs {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 128 || seen[id] {
			return ModelGroup{}, ErrInvalidModelGroup
		}
		if _, found := (*deployments)[id]; !found {
			return ModelGroup{}, ErrInvalidModelGroup
		}
		seen[id] = true
		input.DeploymentIDs[index] = id
	}
	input.DeploymentIDs = append([]string(nil), input.DeploymentIDs...)
	return input, nil
}

func (r Router) modelGroup(model string) (ModelGroup, bool) {
	if r.modelGroups == nil || r.modelGroups.current.Load() == nil {
		return ModelGroup{}, false
	}
	group, found := (*r.modelGroups.current.Load())[model]
	return group, found && group.Enabled
}

func cloneModelGroups(current map[string]ModelGroup) map[string]ModelGroup {
	next := make(map[string]ModelGroup, len(current))
	for key, value := range current {
		value.DeploymentIDs = append([]string(nil), value.DeploymentIDs...)
		next[key] = value
	}
	return next
}
