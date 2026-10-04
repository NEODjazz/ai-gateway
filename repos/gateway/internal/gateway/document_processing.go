package gateway

import (
	"context"
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/documentprocessing"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

func (h Handler) documentProcessingEnabled() bool {
	preparer, ok := h.provider.(provider.DocumentInputPreparer)
	return ok && preparer.DocumentProcessingEnabled()
}

func (h Handler) prepareDocuments(w http.ResponseWriter, r *http.Request, req *modules.RequestContext) bool {
	if !h.documentProcessingEnabled() {
		return true
	}
	if !h.prepareAccessGroups(w, req) || !h.authorizeModel(w, *req, req.Request.Model) {
		return false
	}
	prepared, err := h.provider.(provider.DocumentInputPreparer).PrepareDocumentInput(r.Context(), *req)
	if err != nil {
		status, code := http.StatusBadGateway, "document_processing_failed"
		switch {
		case errors.Is(err, documentprocessing.ErrBusy):
			status, code = http.StatusServiceUnavailable, "document_queue_full"
		case errors.Is(err, documentprocessing.ErrUnavailable):
			status, code = http.StatusServiceUnavailable, "document_processing_unavailable"
		case errors.Is(err, context.DeadlineExceeded):
			status, code = http.StatusGatewayTimeout, "document_processing_timeout"
		case errors.Is(err, context.Canceled):
			return false
		case errors.Is(err, documentprocessing.ErrTooLarge), errors.Is(err, provider.ErrDocumentContextLimit):
			status, code = http.StatusRequestEntityTooLarge, "document_context_too_large"
		case errors.Is(err, modules.ErrUnauthorized):
			status, code = http.StatusUnauthorized, "unauthorized"
		case errors.Is(err, modules.ErrContentRejected):
			status, code = http.StatusBadRequest, "content_rejected"
		}
		writeError(w, status, code, http.StatusText(status))
		return false
	}
	*req = prepared
	if req.ResponseRequest != nil {
		req.Request.Messages = responseMessages(*req.ResponseRequest)
	}
	return true
}
