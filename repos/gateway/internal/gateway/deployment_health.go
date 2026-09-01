package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-gateway-gateway/internal/provider"
)

const maxBatchDeploymentHealthChecks = 50

type deploymentHealthBatchError struct {
	DeploymentID string `json:"deployment_id"`
	Error        string `json:"error"`
}

func (h Handler) deploymentHealthController() (provider.DeploymentHealthController, bool) {
	controller, ok := h.provider.(provider.DeploymentHealthController)
	return controller, ok && controller != nil
}

func (h Handler) TestModelDeployment(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.deploymentHealthController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "deployment health checks are unavailable")
		return
	}
	id := r.PathValue("id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "model_deployment.test", TargetType: "model_deployment", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	check, err := controller.TestModelDeployment(ctx, id)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDeploymentHealthError(w, err)
		return
	}
	outcome := "succeeded"
	if check.Status != "available" {
		outcome = "failed"
	}
	h.auditOutcome(r.Context(), audit, event, outcome)
	writeJSON(w, http.StatusOK, check)
}

func (h Handler) ListModelDeploymentHealth(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.deploymentHealthController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "deployment health checks are unavailable")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 50")
			return
		}
		limit = parsed
	}
	checks, err := controller.ListModelDeploymentHealth(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeDeploymentHealthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": checks})
}

func (h Handler) ListLatestModelDeploymentHealth(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	health, ok := h.deploymentHealthController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "deployment health checks are unavailable")
		return
	}
	deployments, ok := h.deploymentController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "runtime deployment management is unavailable")
		return
	}
	ids, valid := requestedDeploymentIDs(r.URL.Query().Get("ids"), deployments.ListModelDeployments(r.Context()), 200)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_request", "ids must contain at most 200 known deployment IDs")
		return
	}
	checks := make([]provider.DeploymentHealthCheck, 0, len(ids))
	for _, id := range ids {
		history, err := health.ListModelDeploymentHealth(r.Context(), id, 1)
		if err != nil {
			writeDeploymentHealthError(w, err)
			return
		}
		if len(history) > 0 {
			checks = append(checks, history[0])
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": checks})
}

func (h Handler) TestModelDeployments(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	health, ok := h.deploymentHealthController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "deployment health checks are unavailable")
		return
	}
	deployments, ok := h.deploymentController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "runtime deployment management is unavailable")
		return
	}
	var input struct {
		DeploymentIDs []string `json:"deployment_ids"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid deployment health batch")
		return
	}
	all := deployments.ListModelDeployments(r.Context())
	if len(input.DeploymentIDs) == 0 {
		for _, deployment := range all {
			if deployment.Enabled {
				input.DeploymentIDs = append(input.DeploymentIDs, deployment.ID)
			}
		}
	}
	ids, valid := requestedDeploymentIDs(strings.Join(input.DeploymentIDs, ","), all, maxBatchDeploymentHealthChecks)
	if !valid || len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "deployment_ids must contain between 1 and 50 known deployment IDs")
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "model_deployment.test_batch", TargetType: "model_deployment", TargetID: strconv.Itoa(len(ids))}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	checks := make([]provider.DeploymentHealthCheck, len(ids))
	errorsByIndex := make([]deploymentHealthBatchError, len(ids))
	jobs := make(chan int)
	var workers sync.WaitGroup
	workerCount := min(4, len(ids))
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
				check, err := health.TestModelDeployment(ctx, ids[index])
				cancel()
				if err != nil {
					errorsByIndex[index] = deploymentHealthBatchError{DeploymentID: ids[index], Error: "health check unavailable"}
					continue
				}
				checks[index] = check
			}
		}()
	}
	for index := range ids {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	resultChecks := checks[:0]
	resultErrors := make([]deploymentHealthBatchError, 0)
	for index := range ids {
		if errorsByIndex[index].DeploymentID != "" {
			resultErrors = append(resultErrors, errorsByIndex[index])
		} else {
			resultChecks = append(resultChecks, checks[index])
		}
	}
	outcome := "succeeded"
	if len(resultErrors) > 0 {
		outcome = "failed"
	}
	h.auditOutcome(r.Context(), audit, event, outcome)
	writeJSON(w, http.StatusOK, map[string]any{"data": resultChecks, "errors": resultErrors})
}

func requestedDeploymentIDs(raw string, deployments []provider.ModelDeployment, limit int) ([]string, bool) {
	known := make(map[string]bool, len(deployments))
	for _, deployment := range deployments {
		known[deployment.ID] = true
	}
	if strings.TrimSpace(raw) == "" {
		ids := make([]string, 0, len(deployments))
		for _, deployment := range deployments {
			ids = append(ids, deployment.ID)
		}
		return ids, len(ids) <= limit
	}
	seen := map[string]bool{}
	ids := make([]string, 0)
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id == "" || len(id) > 128 || !known[id] || seen[id] {
			return nil, false
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, len(ids) <= limit
}

func writeDeploymentHealthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrDeploymentNotFound):
		writeError(w, http.StatusNotFound, "not_found", "model deployment not found")
	case errors.Is(err, provider.ErrInvalidDeployment):
		writeError(w, http.StatusBadRequest, "invalid_deployment", "model deployment cannot be tested")
	default:
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "deployment health check failed")
	}
}
