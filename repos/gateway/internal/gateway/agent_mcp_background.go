package gateway

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/asyncstate"
	"ai-gateway-gateway/internal/modules"
)

const agentMCPJobLease = 3 * time.Minute

type agentMCPWorkerContextKey struct{}
type agentMCPWorkerIdentity struct{ Owner, Organization, Team string }

type agentMCPJobSecret struct {
	OrganizationID string `json:"organizationId,omitempty"`
	TeamID         string `json:"teamId,omitempty"`
	Version        int    `json:"version"`
	TaskID         string `json:"taskId"`
	RunID          string `json:"runId"`
	MessageID      string `json:"messageId"`
	Token          string `json:"token"`
	SessionID      string `json:"sessionId,omitempty"`
}

// WithAgentMCPBackground requires an atomic task/outbox store and encrypts
// short-lived execution credentials separately from public task state.
func (h Handler) WithAgentMCPBackground(jobs asyncstate.Store, key []byte) (Handler, error) {
	outbox, ok := h.a2aTasks.(a2astate.AtomicOutboxStore)
	if jobs == nil || !ok {
		return h, errors.New("agent background execution requires atomic outbox storage")
	}
	if len(key) < 16 {
		return h, errors.New("agent background encryption key is too short")
	}
	digest := sha256.Sum256(append([]byte("ai-gateway/agent-mcp-job/v1\x00"), key...))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		return h, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return h, err
	}
	h.agentMCPJobs, h.agentMCPOutbox, h.agentMCPJobAEAD = jobs, outbox, aead
	return h, nil
}

func agentMCPJobAAD(job asyncstate.Job) []byte {
	return []byte(job.Kind + "\x00" + job.OwnerKey + "\x00" + job.EndpointID + "\x00" + job.ResourceID + "\x00" + job.ExecutionID)
}

func (h Handler) queueAgentMCPTask(r *http.Request, run *agentMCPTaskRun, initial bool, identity modules.RequestContext) error {
	if h.agentMCPJobs == nil || h.agentMCPOutbox == nil || h.agentMCPJobAEAD == nil {
		return a2astate.ErrUnavailable
	}
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" || len(token) > 32<<10 || len(sessionID(r)) > 256 {
		return a2astate.ErrInvalid
	}
	executionID := newExecutionID()
	run.state.Version, run.state.BackgroundExecutionID, run.state.WorkerStarted = 2, executionID, false
	run.task.Status = agentMCPTaskStatus(run.task, "TASK_STATE_SUBMITTED", "Agent execution queued; no model or tool work has started.", nil)
	payload, err := encodeAgentMCPTask(run.task, run.state)
	if err != nil {
		return err
	}
	secret := agentMCPJobSecret{OrganizationID: identity.OrganizationID, TeamID: identity.TeamID, Version: 1, TaskID: run.task.ID, RunID: run.state.RunID, MessageID: run.state.MessageID, Token: token, SessionID: sessionID(r)}
	plain, err := json.Marshal(secret)
	if err != nil {
		return a2astate.ErrInvalid
	}
	job := asyncstate.Job{Kind: a2astate.AgentJobKind, ResourceID: run.task.ID + ":" + executionID, OwnerKey: run.stored.OwnerKey, EndpointID: run.stored.AgentID, ExecutionID: executionID}
	nonce := make([]byte, h.agentMCPJobAEAD.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return errors.New("agent background encryption failed")
	}
	job.Payload, err = json.Marshal(a2aPushJobPayload{Nonce: nonce, Ciphertext: h.agentMCPJobAEAD.Seal(nil, nonce, plain, agentMCPJobAAD(job))})
	if err != nil || !asyncstate.Valid(job) {
		return a2astate.ErrInvalid
	}
	updated := run.stored
	updated.State, updated.Payload = run.task.Status.State, payload
	if initial {
		updated, err = h.agentMCPOutbox.CreateA2ATaskWithJob(r.Context(), updated, h.a2aTaskConfig.OwnerQuota, h.a2aTaskConfig.TTL, job)
	} else {
		updated, err = h.agentMCPOutbox.UpdateA2ATaskWithJob(r.Context(), updated, run.stored.UpdatedAt, h.a2aTaskConfig.TTL, job)
	}
	if err == nil {
		run.stored = updated
	}
	return err
}

func (h Handler) authenticateAgentMCPRequest(ctx context.Context, req *modules.RequestContext) error {
	if err := h.pipeline.RunAuthentication(ctx, req); err != nil {
		return err
	}
	if owner, background := ctx.Value(agentMCPWorkerContextKey{}).(agentMCPWorkerIdentity); background && (owner.Owner == "" || fileOwnerKey(*req) != owner.Owner || req.OrganizationID != owner.Organization || req.TeamID != owner.Team) {
		return modules.ErrUnauthorized
	}
	return nil
}

func (h Handler) decodeAgentMCPJob(job asyncstate.Job) (agentMCPJobSecret, error) {
	var envelope a2aPushJobPayload
	if h.agentMCPJobAEAD == nil || !asyncstate.Valid(job) || job.Kind != a2astate.AgentJobKind || decodeStrictJSON(job.Payload, &envelope) != nil || len(envelope.Nonce) != h.agentMCPJobAEAD.NonceSize() {
		return agentMCPJobSecret{}, a2astate.ErrInvalid
	}
	plain, err := h.agentMCPJobAEAD.Open(nil, envelope.Nonce, envelope.Ciphertext, agentMCPJobAAD(job))
	var secret agentMCPJobSecret
	if err != nil || decodeStrictJSON(plain, &secret) != nil || secret.Version != 1 || !validFileToken(secret.TaskID, 128) || !validFileToken(secret.RunID, 128) || secret.MessageID == "" || len(secret.MessageID) > 128 || secret.Token == "" || len(secret.Token) > 32<<10 || len(secret.SessionID) > 256 || job.ResourceID != secret.TaskID+":"+job.ExecutionID {
		return agentMCPJobSecret{}, a2astate.ErrInvalid
	}
	return secret, nil
}

// Each replica claims at most one job. A durable task CAS occurs before effects;
// reclaiming a started task never replays unknown model or tool work.
func (h Handler) ProcessAgentMCPBackground(ctx context.Context) (int, error) {
	if h.agentMCPJobs == nil || h.agentMCPOutbox == nil || h.agentMCPJobAEAD == nil {
		return 0, a2astate.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	jobs, err := h.agentMCPJobs.ClaimAsyncJobs(ctx, a2astate.AgentJobKind, 1, agentMCPJobLease)
	if err != nil {
		return 0, err
	}
	for _, job := range jobs {
		if err := h.processAgentMCPJob(ctx, job); err != nil {
			return len(jobs), err
		}
	}
	return len(jobs), nil
}

func (h Handler) processAgentMCPJob(ctx context.Context, job asyncstate.Job) error {
	complete := func() error {
		return h.agentMCPJobs.CompleteAsyncJob(ctx, job.Kind, job.ResourceID, job.LeaseGeneration)
	}
	retry := func(cause error) error {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		return errors.Join(cause, h.agentMCPJobs.RetryAsyncJob(cleanup, job.Kind, job.ResourceID, job.LeaseGeneration, backgroundRetry(job.Attempts)))
	}
	if h.agents == nil || h.mcp == nil || h.mcpCalls == nil || h.mcpRuntime == nil || h.audit == nil {
		return retry(a2astate.ErrUnavailable)
	}
	secret, err := h.decodeAgentMCPJob(job)
	if err != nil {
		return errors.Join(a2astate.ErrInvalid, complete())
	}
	stored, err := h.a2aTasks.GetA2ATask(ctx, job.OwnerKey, job.EndpointID, secret.TaskID)
	if errors.Is(err, a2astate.ErrNotFound) {
		return complete()
	}
	if err != nil {
		return retry(err)
	}
	task, err := decodeA2ATask(stored.Payload)
	state, stateErr := decodeAgentMCPState(stored.Payload)
	if err != nil || stateErr != nil || state == nil || task.ID != stored.ID || task.ContextID != stored.ContextID || task.Status.State != stored.State || state.Profile.ID != stored.AgentID || state.Profile.Model != stored.Model {
		return retry(a2astate.ErrInvalid)
	}
	if state.BackgroundExecutionID != job.ExecutionID || state.RunID != secret.RunID || state.MessageID != secret.MessageID || !a2aTaskPending(task.Status.State) {
		return complete()
	}
	run := agentMCPTaskRun{stored: stored, task: task, state: state}
	fail := func(canceled bool) error {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if err := h.failAgentMCPTask(cleanup, &run, canceled); err != nil {
			return retry(err)
		}
		return h.agentMCPJobs.CompleteAsyncJob(cleanup, job.Kind, job.ResourceID, job.LeaseGeneration)
	}
	if state.CancelRequested || !time.Now().Before(state.Deadline) {
		return fail(state.CancelRequested)
	}
	if state.WorkerStarted {
		// A lease may have been reclaimed while its first worker still settles.
		// Wait for the durable deadline; never restart an already claimed step.
		return retry(errors.New("agent execution is already claimed"))
	}
	if task.Status.State != "TASK_STATE_SUBMITTED" {
		return fail(false)
	}
	requestCtx := context.WithValue(ctx, agentMCPWorkerContextKey{}, agentMCPWorkerIdentity{Owner: job.OwnerKey, Organization: secret.OrganizationID, Team: secret.TeamID})
	r, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "http://gateway.internal/a2a/"+job.EndpointID, nil)
	if err != nil {
		return fail(false)
	}
	r.Header.Set("Authorization", "Bearer "+secret.Token)
	r.Header.Set("X-Session-ID", secret.SessionID)
	identity := modules.RequestContext{APIKey: secret.Token, RequestID: job.ExecutionID, Metadata: map[string]string{"gateway.api_type": "a2a"}}
	if err := h.authenticateAgentMCPRequest(requestCtx, &identity); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			return fail(false)
		}
		return retry(errors.New("agent background authorization is unavailable"))
	}
	check := newA2AResponseCapture()
	if !h.prepareAccessGroups(check, &identity) || !h.authorizeA2ATaskModel(check, nil, identity, stored.Model) {
		if check.status >= 500 {
			return retry(errors.New("agent background policy is unavailable"))
		}
		return fail(false)
	}
	profile, found := h.agents.AgentProfile(job.EndpointID)
	if !found || agentMCPConfiguration(profile) != state.Configuration {
		return fail(false)
	}
	state.Profile.Instructions = profile.Instructions
	state.WorkerStarted, state.Deadline = true, time.Now().UTC().Add(2*time.Minute)
	run.task.Status = agentMCPTaskStatus(task, "TASK_STATE_WORKING", "Agent background execution started.", nil)
	if err := h.checkpointAgentMCPTask(ctx, &run); err != nil {
		return retry(err)
	}
	executionCtx, cancel := context.WithDeadline(requestCtx, state.Deadline)
	defer cancel()
	capture := newA2AResponseCapture()
	h.runAgentMCPTask(agentMCPCapture{capture}, r.WithContext(executionCtx), a2aRequest{}, &run)
	if capture.status != http.StatusOK && a2aTaskPending(run.task.Status.State) {
		return fail(errors.Is(executionCtx.Err(), context.Canceled))
	}
	// Task transitions and provider/tool settlement happen before job deletion.
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	return h.agentMCPJobs.CompleteAsyncJob(cleanup, job.Kind, job.ResourceID, job.LeaseGeneration)
}

func RunAgentMCPBackgroundWorker(ctx context.Context, handler Handler) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := handler.ProcessAgentMCPBackground(ctx); err != nil && ctx.Err() == nil {
			log.Print("agent background processing failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
