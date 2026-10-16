package gateway

import (
	"io"
	"log"
	"net/http"
	"strconv"

	"ai-gateway-gateway/internal/provider"
)

const maxResponseArtifactBytes = 32 << 20

func (h Handler) GetResponseContainerFileContent(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.provider.(provider.ResponseContainerFileProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "response_lifecycle_unsupported", "response file content is not supported")
		return
	}
	if r.URL.RawQuery != "" || !validLifecycleToken(r.PathValue("id")) || !validLifecycleToken(r.PathValue("container_id")) || !validLifecycleToken(r.PathValue("file_id")) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid response file content request")
		return
	}
	req, ok := h.authorizeResponseResource(w, r, runtime, r.PathValue("id"))
	if !ok || !h.authorizeAgentResponseTools(w, r, req, []string{"code_interpreter"}, true) {
		return
	}
	content, err := runtime.DownloadResponseContainerFile(r.Context(), req, r.PathValue("id"), r.PathValue("container_id"), r.PathValue("file_id"))
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	if content.Body == nil {
		writeError(w, http.StatusBadGateway, "provider_failed", "response file content is unavailable")
		return
	}
	defer func() {
		if err := content.Body.Close(); err != nil {
			log.Print("response file content close failed")
		}
	}()
	if content.ContentLength > maxResponseArtifactBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "response_too_large", "response file exceeds 32 MiB")
		return
	}
	// Validate unknown-length bodies before sending a successful status. A
	// truncated chunked download must not be presented as a complete file.
	payload, err := io.ReadAll(io.LimitReader(content.Body, maxResponseArtifactBytes+1))
	if err != nil {
		writeError(w, http.StatusBadGateway, "provider_failed", "response file content could not be read")
		return
	}
	if len(payload) > maxResponseArtifactBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "response_too_large", "response file exceeds 32 MiB")
		return
	}
	w.Header().Set("Content-Type", content.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	if _, err := w.Write(payload); err != nil {
		log.Print("response file content stream failed")
	}
}
