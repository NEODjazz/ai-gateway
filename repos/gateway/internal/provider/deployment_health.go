package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-gateway-gateway/internal/openai"
)

const deploymentHealthHistoryLimit = 50

type DeploymentHealthCheck struct {
	DeploymentID string    `json:"deployment_id"`
	ProviderID   string    `json:"provider_id"`
	Model        string    `json:"model"`
	Status       string    `json:"status"`
	FailureClass string    `json:"failure_class,omitempty"`
	HTTPStatus   int       `json:"http_status,omitempty"`
	UpstreamCode string    `json:"upstream_code,omitempty"`
	Param        string    `json:"param,omitempty"`
	LatencyMS    int64     `json:"latency_ms"`
	CheckedAt    time.Time `json:"checked_at"`
}

type DeploymentHealthController interface {
	TestModelDeployment(context.Context, string) (DeploymentHealthCheck, error)
	ListModelDeploymentHealth(context.Context, string, int) ([]DeploymentHealthCheck, error)
}

type deploymentHealthRegistry struct {
	mu      sync.RWMutex
	history map[string][]DeploymentHealthCheck
}

func newDeploymentHealthRegistry() *deploymentHealthRegistry {
	return &deploymentHealthRegistry{history: map[string][]DeploymentHealthCheck{}}
}

func (r *Router) TestModelDeployment(ctx context.Context, id string) (DeploymentHealthCheck, error) {
	deployment, endpoint, err := r.healthCheckTarget(ctx, id)
	if err != nil {
		return DeploymentHealthCheck{}, err
	}
	model := strings.TrimSpace(deployment.UpstreamModel)
	if model == "" {
		model = deployment.Models[0]
	}
	check := DeploymentHealthCheck{
		DeploymentID: deployment.ID,
		ProviderID:   deployment.ProviderID,
		Model:        model,
		Status:       "available",
		CheckedAt:    time.Now().UTC(),
	}
	started := time.Now()
	_, probeErr := endpoint.Provider.ChatCompletions(ctx, openai.ChatCompletionRequest{
		Model:    model,
		Messages: []openai.Message{{Role: "user", Content: "Reply with OK."}},
	})
	check.LatencyMS = time.Since(started).Milliseconds()
	if probeErr != nil {
		check.Status = "unavailable"
		check.FailureClass = deploymentProbeFailureClass(probeErr)
		var providerErr *Error
		if errors.As(probeErr, &providerErr) {
			check.HTTPStatus = providerErr.StatusCode
			check.UpstreamCode = providerErr.UpstreamCode
			check.Param = providerErr.Param
		}
	}
	r.deploymentHealth.append(check)
	return check, nil
}

func (r *Router) ListModelDeploymentHealth(ctx context.Context, id string, limit int) ([]DeploymentHealthCheck, error) {
	if _, _, err := r.healthCheckTarget(ctx, id); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > deploymentHealthHistoryLimit {
		limit = deploymentHealthHistoryLimit
	}
	return r.deploymentHealth.list(strings.TrimSpace(id), limit), nil
}

func (r *Router) healthCheckTarget(ctx context.Context, id string) (ModelDeployment, Endpoint, error) {
	if err := r.refreshControlPlane(ctx); err != nil {
		return ModelDeployment{}, Endpoint{}, err
	}
	id = strings.TrimSpace(id)
	if r == nil || r.deployments == nil || r.deployments.current.Load() == nil {
		return ModelDeployment{}, Endpoint{}, ErrDeploymentNotFound
	}
	deployment, found := (*r.deployments.current.Load())[id]
	if !found {
		return ModelDeployment{}, Endpoint{}, ErrDeploymentNotFound
	}
	endpoint, err := r.endpointForDeployment(deployment)
	if err != nil {
		return ModelDeployment{}, Endpoint{}, ErrInvalidDeployment
	}
	return deployment, endpoint, nil
}

func deploymentProbeFailureClass(err error) string {
	var providerErr *Error
	if errors.As(err, &providerErr) {
		return string(providerErr.Class)
	}
	var unknownAuthority x509.UnknownAuthorityError
	var certificateInvalid x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	if errors.As(err, &unknownAuthority) || errors.As(err, &certificateInvalid) || errors.As(err, &recordHeader) {
		return "tls"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if errors.Is(urlErr.Err, context.DeadlineExceeded) {
			return string(FailureTimeout)
		}
		return "network"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return string(FailureTimeout)
		}
		return "network"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return string(FailureTimeout)
	}
	return string(FailureUnknown)
}

func (r *deploymentHealthRegistry) append(check DeploymentHealthCheck) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	history := append([]DeploymentHealthCheck{check}, r.history[check.DeploymentID]...)
	if len(history) > deploymentHealthHistoryLimit {
		history = history[:deploymentHealthHistoryLimit]
	}
	r.history[check.DeploymentID] = history
}

func (r *deploymentHealthRegistry) list(id string, limit int) []DeploymentHealthCheck {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	result := append([]DeploymentHealthCheck(nil), r.history[id]...)
	r.mu.RUnlock()
	if len(result) > limit {
		result = result[:limit]
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].CheckedAt.After(result[j].CheckedAt) })
	return result
}

func (r *deploymentHealthRegistry) delete(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.history, id)
	r.mu.Unlock()
}
