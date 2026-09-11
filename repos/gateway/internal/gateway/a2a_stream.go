package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-gateway-gateway/internal/a2astate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type a2aStreamTransformer struct {
	h            Handler
	ctx          context.Context
	request      a2aRequest
	profile      AgentProfile
	stored       a2astate.Task
	existing     a2aTask
	continuation bool
	task         a2aTask
	updatedAt    time.Time
	ownerKey     string
	artifactID   string
	emittedText  bool
}

func newA2AStreamTransformer(h Handler, ctx context.Context, request a2aRequest, profile AgentProfile, stored a2astate.Task, existing a2aTask, continuation bool) *a2aStreamTransformer {
	return &a2aStreamTransformer{h: h, ctx: ctx, request: request, profile: profile, stored: stored, existing: existing, continuation: continuation}
}

func (t *a2aStreamTransformer) Transform(event, payload string, reqCtx modules.RequestContext) ([]responseStreamEvent, error) {
	var envelope struct {
		Type     string                  `json:"type"`
		Response openai.ResponseResponse `json:"response"`
		Delta    string                  `json:"delta"`
	}
	if event != "error" && (json.Unmarshal([]byte(payload), &envelope) != nil || envelope.Type != "" && envelope.Type != event) {
		return nil, errors.New("invalid Responses event for A2A stream")
	}
	events, err := t.ensureTask(reqCtx)
	if err != nil {
		return nil, err
	}
	switch event {
	case "response.output_text.delta", "response.refusal.delta":
		if envelope.Delta == "" {
			return events, nil
		}
		update := map[string]any{"taskId": t.task.ID, "contextId": t.task.ContextID, "append": t.emittedText, "lastChunk": false,
			"artifact": a2aArtifact{ArtifactID: t.artifactID, Parts: []a2aPart{{Text: &envelope.Delta}}}}
		t.emittedText = true
		return append(events, t.event(map[string]any{"artifactUpdate": update})), nil
	case "response.completed", "response.incomplete", "response.failed":
		return events, nil
	case "error":
		return t.finish(events, openai.ResponseResponse{Status: "failed"})
	default:
		return events, nil
	}
}

func (t *a2aStreamTransformer) Finalize(response openai.ResponseResponse, reqCtx modules.RequestContext) ([]responseStreamEvent, error) {
	events, err := t.ensureTask(reqCtx)
	if err != nil {
		return nil, err
	}
	return t.finish(events, response)
}

func (t *a2aStreamTransformer) ensureTask(reqCtx modules.RequestContext) ([]responseStreamEvent, error) {
	if t.task.ID != "" {
		return nil, nil
	}
	t.task = newPendingA2ATask(t.request, t.existing, t.continuation, "in_progress")
	t.artifactID = newA2AID("artifact")
	payload, err := encodeA2AStreamingTask(t.task)
	if err != nil {
		return nil, err
	}
	candidate := a2astate.Task{ID: t.task.ID, OwnerKey: fileOwnerKey(reqCtx), AgentID: t.profile.ID, Model: t.profile.Model, ContextID: t.task.ContextID, State: t.task.Status.State, Payload: payload}
	var saved a2astate.Task
	if t.continuation {
		candidate.Model = t.stored.Model
		saved, err = t.h.a2aTasks.UpdateA2ATask(t.ctx, candidate, t.stored.UpdatedAt, t.h.a2aTaskConfig.TTL)
	} else {
		saved, err = t.h.a2aTasks.CreateA2ATask(t.ctx, candidate, t.h.a2aTaskConfig.OwnerQuota, t.h.a2aTaskConfig.TTL)
	}
	if err != nil {
		return nil, err
	}
	t.updatedAt = saved.UpdatedAt
	t.ownerKey = candidate.OwnerKey
	return []responseStreamEvent{t.event(map[string]any{"task": t.task})}, nil
}

func (t *a2aStreamTransformer) finish(events []responseStreamEvent, response openai.ResponseResponse) ([]responseStreamEvent, error) {
	completed := materializeA2ABackgroundTask(t.task, response)
	if completed.Status.State == "TASK_STATE_COMPLETED" && len(completed.Artifacts) > 0 {
		completed.Artifacts[len(completed.Artifacts)-1].ArtifactID = t.artifactID
	}
	payload, err := encodeA2AStoredTask(completed, "")
	if err != nil {
		return nil, err
	}
	persistCtx := t.ctx
	cancel := func() {}
	if persistCtx.Err() != nil {
		persistCtx, cancel = context.WithTimeout(context.WithoutCancel(persistCtx), 5*time.Second)
	}
	defer cancel()
	_, err = t.h.a2aTasks.UpdateA2ATask(persistCtx, a2astate.Task{
		ID: completed.ID, OwnerKey: t.ownerKey, AgentID: t.profile.ID, Model: t.taskModel(), ContextID: completed.ContextID,
		State: completed.Status.State, Payload: payload,
	}, t.updatedAt, t.h.a2aTaskConfig.TTL)
	if err != nil {
		return nil, err
	}
	t.task = completed
	if completed.Status.State == "TASK_STATE_COMPLETED" {
		text := responseOutputText(response)
		if t.emittedText {
			empty := ""
			text = &empty
		}
		update := map[string]any{"taskId": completed.ID, "contextId": completed.ContextID, "append": t.emittedText, "lastChunk": true,
			"artifact": a2aArtifact{ArtifactID: t.artifactID, Parts: []a2aPart{{Text: text}}}}
		events = append(events, t.event(map[string]any{"artifactUpdate": update}))
	}
	status := map[string]any{"taskId": completed.ID, "contextId": completed.ContextID, "status": completed.Status}
	return append(events, t.event(map[string]any{"statusUpdate": status})), nil
}

func (t *a2aStreamTransformer) taskModel() string {
	if t.continuation {
		return t.stored.Model
	}
	return t.profile.Model
}

func (t *a2aStreamTransformer) event(result any) responseStreamEvent {
	payload, _ := json.Marshal(a2aRPCResponse{JSONRPC: "2.0", ID: t.request.ID, Result: result})
	return responseStreamEvent{Payload: string(payload)}
}
