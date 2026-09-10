package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"mime"
	"net/http"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/openai"
)

func (h Handler) resolveOCRFile(ctx context.Context, owner, id string) (openai.OCRDocument, error) {
	if h.files == nil {
		return openai.OCRDocument{}, filestate.ErrUnavailable
	}
	file, err := h.files.Get(ctx, owner, id, true)
	if err != nil {
		return openai.OCRDocument{}, err
	}
	if file.Bytes <= 0 || file.Bytes > openai.MaxOCRDocumentBytes || file.Bytes != int64(len(file.Content)) {
		return openai.OCRDocument{}, filestate.ErrInvalid
	}
	mediaType, _, err := mime.ParseMediaType(file.ContentType)
	if err != nil {
		return openai.OCRDocument{}, filestate.ErrInvalid
	}
	encoded := base64.StdEncoding.EncodeToString(file.Content)
	switch mediaType {
	case "application/pdf":
		document := openai.OCRDocument{Type: "document_url", DocumentURL: "data:" + mediaType + ";base64," + encoded}
		if _, err := document.Attachment(); err != nil {
			return openai.OCRDocument{}, filestate.ErrInvalid
		}
		return document, nil
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		document := openai.OCRDocument{Type: "image_url", ImageURL: "data:" + mediaType + ";base64," + encoded}
		if _, err := document.Attachment(); err != nil {
			return openai.OCRDocument{}, filestate.ErrInvalid
		}
		return document, nil
	default:
		return openai.OCRDocument{}, filestate.ErrInvalid
	}
}

func writeOCRFileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, filestate.ErrNotFound):
		writeError(w, http.StatusNotFound, "file_not_found", "file not found")
	case errors.Is(err, filestate.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_file", "file is not a supported OCR document")
	default:
		writeError(w, http.StatusServiceUnavailable, "file_storage_unavailable", "file storage is unavailable")
	}
}
