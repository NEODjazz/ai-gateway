package gateway

import (
	"encoding/json"
	"net/http"
)

func startAgentMCPStream(w http.ResponseWriter, id json.RawMessage, task a2aTask) error {
	writeStreamHeaders(w)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	return writeA2AStreamResult(w, id, map[string]any{"task": task})
}

func finishAgentMCPStream(w http.ResponseWriter, id json.RawMessage, task a2aTask) error {
	for _, artifact := range task.Artifacts {
		if err := writeA2AStreamResult(w, id, map[string]any{"artifactUpdate": map[string]any{
			"taskId": task.ID, "contextId": task.ContextID, "artifact": artifact, "append": false, "lastChunk": true,
		}}); err != nil {
			return err
		}
	}
	return writeA2AStreamResult(w, id, map[string]any{"statusUpdate": map[string]any{
		"taskId": task.ID, "contextId": task.ContextID, "status": task.Status,
	}})
}

func writeAgentMCPReply(w http.ResponseWriter, request a2aRequest, task a2aTask) {
	if request.Method != "SendStreamingMessage" {
		writeAgentMCPTask(w, request.ID, task)
		return
	}
	if err := startAgentMCPStream(w, request.ID, task); err != nil {
		return
	}
	_ = finishAgentMCPStream(w, request.ID, task)
}
