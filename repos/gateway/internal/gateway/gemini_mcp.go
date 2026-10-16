package gateway

import (
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

var (
	errGeminiMCPUnavailable = errors.New("Gemini MCP registry is unavailable")
	errGeminiMCPInvalid     = errors.New("Gemini MCP server is unavailable for provider execution")
)

func (h Handler) resolveGeminiMCPServers(request *modules.RequestContext) error {
	if request == nil || len(request.Request.GeminiMCPServerIDs) == 0 {
		return nil
	}
	if h.mcp == nil {
		return errGeminiMCPUnavailable
	}
	if !openai.ValidGeminiMCPServerIDs(request.Request.GeminiMCPServerIDs) {
		return errGeminiMCPInvalid
	}
	servers := make([]openai.GeminiMCPServer, 0, len(request.Request.GeminiMCPServerIDs))
	identifiers := make([]string, 0, len(request.Request.GeminiMCPServerIDs))
	for _, id := range request.Request.GeminiMCPServerIDs {
		server, bearerToken, found := h.mcp.ServerRuntime(id)
		if !found || !server.Enabled || !server.AllowProviderExecution || server.Transport != "streamable-http" {
			return errGeminiMCPInvalid
		}
		headers := map[string]string(nil)
		if bearerToken != "" {
			headers = map[string]string{"Authorization": "Bearer " + bearerToken}
		}
		servers = append(servers, openai.GeminiMCPServer{Name: id, StreamableHTTPTransport: openai.GeminiStreamableHTTPTransport{URL: server.ServerURL, Headers: headers, Timeout: "30s", SSEReadTimeout: "60s", TerminateOnClose: true}})
		identifiers = append(identifiers, "mcp:"+server.ID+"@"+server.ServerURL)
	}
	request.Request.GeminiMCPServers = servers
	request.Request.GeminiMCPConnectorIDs = identifiers
	return nil
}

func writeGeminiMCPError(w http.ResponseWriter, err error) {
	if errors.Is(err, errGeminiMCPUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "mcp_unavailable", err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request", errGeminiMCPInvalid.Error())
}
