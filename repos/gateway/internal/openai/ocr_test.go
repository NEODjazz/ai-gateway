package openai

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestOCRRequestValidationAndAccounting(t *testing.T) {
	request := OCRRequest{
		Model:                       "ocr-model",
		Document:                    OCRDocument{Type: "document_url", DocumentURL: "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\npage"))},
		Pages:                       "0, 2-4",
		TableFormat:                 "markdown",
		ConfidenceScoresGranularity: "page",
		DocumentAnnotationPrompt:    "extract invoice",
		DocumentAnnotationFormat:    &ResponseFormat{Type: "json_schema", JSONSchema: &JSONSchemaFormat{Name: "invoice", Schema: map[string]any{"type": "object"}}},
	}
	if message := request.Validate(); message != "" {
		t.Fatal(message)
	}
	if request.ReservePages() != 4 || request.InputTokens() == 0 {
		t.Fatalf("pages=%d tokens=%d", request.ReservePages(), request.InputTokens())
	}
	attachment, err := request.Document.Attachment()
	if err != nil || attachment == nil || attachment.MediaType != "application/pdf" {
		t.Fatalf("attachment=%+v err=%v", attachment, err)
	}

	image := OCRRequest{Model: "ocr-model", Document: OCRDocument{Type: "image_url", ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))}}
	if message := image.Validate(); message != "" || image.ReservePages() != 1 {
		t.Fatalf("message=%q pages=%d", message, image.ReservePages())
	}
	remote := OCRRequest{Model: "ocr-model", Document: OCRDocument{Type: "document_url", DocumentURL: "https://example.test/document.pdf"}}
	if message := remote.Validate(); message != "" || remote.ReservePages() != MaxOCRPages {
		t.Fatalf("message=%q pages=%d", message, remote.ReservePages())
	}
}

func TestOCRPageSelectionFromJSON(t *testing.T) {
	var request OCRRequest
	if err := json.Unmarshal([]byte(`{"model":"ocr","document":{"type":"document_url","document_url":"https://example.test/a.pdf"},"pages":[0,2,4]}`), &request); err != nil {
		t.Fatal(err)
	}
	pages, specified, err := OCRPageSelection(request.Pages)
	if err != nil || !specified || len(pages) != 3 || request.ReservePages() != 3 {
		t.Fatalf("pages=%v specified=%v err=%v", pages, specified, err)
	}
}

func TestOCRRequestRejectsInvalidInputs(t *testing.T) {
	valid := OCRRequest{Model: "ocr", Document: OCRDocument{Type: "document_url", DocumentURL: "https://example.test/a.pdf"}}
	tests := []OCRRequest{
		{},
		{Model: "ocr", Document: OCRDocument{Type: "file", DocumentURL: "https://example.test/a.pdf"}},
		{Model: "ocr", Document: OCRDocument{Type: "document_url", DocumentURL: "http://example.test/a.pdf"}},
		{Model: "ocr", Document: OCRDocument{Type: "document_url", DocumentURL: "https://user@example.test/a.pdf"}},
		{Model: "ocr", Document: OCRDocument{Type: "document_url", DocumentURL: "https://example.test/a.pdf#page=1"}},
		{Model: "ocr", Document: OCRDocument{Type: "image_url", DocumentURL: "https://example.test/a.png"}},
		{Model: "ocr", Document: OCRDocument{Type: "document_url", DocumentURL: "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("not-pdf"))}},
		func() OCRRequest { r := valid; r.Pages = "4-2"; return r }(),
		func() OCRRequest { r := valid; r.Pages = []int{1, 1}; return r }(),
		func() OCRRequest { r := valid; r.Pages = []int{-1}; return r }(),
		func() OCRRequest { r := valid; r.Pages = "0-1000"; return r }(),
		func() OCRRequest { r := valid; r.TableFormat = "csv"; return r }(),
		func() OCRRequest { r := valid; r.ConfidenceScoresGranularity = "sentence"; return r }(),
		func() OCRRequest { r := valid; r.DocumentAnnotationPrompt = "extract"; return r }(),
		func() OCRRequest {
			r := valid
			r.DocumentAnnotationFormat = &ResponseFormat{Type: "json_schema"}
			return r
		}(),
	}
	for index, request := range tests {
		if message := request.Validate(); message == "" {
			t.Fatalf("case %d accepted: %+v", index, request)
		}
	}
}

func TestOCRFileReferenceValidation(t *testing.T) {
	valid := OCRRequest{Model: "ocr", Document: OCRDocument{Type: "file", FileID: "file_abc-123"}}
	if message := valid.Validate(); message != "" {
		t.Fatalf("valid file reference rejected: %s", message)
	}
	if valid.ReservePages() != MaxOCRPages {
		t.Fatalf("file reserve=%d", valid.ReservePages())
	}
	for _, document := range []OCRDocument{
		{Type: "file"},
		{Type: "file", FileID: "other_abc"},
		{Type: "file", FileID: "file_bad/id"},
		{Type: "file", FileID: "file_ok", DocumentURL: "https://example.test/a.pdf"},
		{Type: "document_url", DocumentURL: "https://example.test/a.pdf", FileID: "file_extra"},
	} {
		request := valid
		request.Document = document
		if message := request.Validate(); message == "" {
			t.Fatalf("invalid document accepted: %+v", document)
		}
	}
	if _, err := valid.Document.Attachment(); err == nil {
		t.Fatal("unresolved file reference produced an attachment")
	}
}
