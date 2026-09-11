package openai

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestResponseFileAttachmentsValidateInlinePDF(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\ncontent"))
	input := []any{map[string]any{"type": "message", "content": []any{map[string]any{
		"type": "input_file", "file_data": "data:application/pdf;base64," + encoded, "filename": "report.pdf",
	}}}}
	attachments, err := ResponseFileAttachments(input)
	if err != nil || len(attachments) != 1 || attachments[0].Data != encoded || attachments[0].Filename != "report.pdf" {
		t.Fatalf("attachments=%+v err=%v", attachments, err)
	}
	if !HasResponseFiles(ResponseRequest{Input: input}) {
		t.Fatal("valid PDF input was not detected")
	}
}

func TestResponseFileAttachmentsRejectUnsafeFormsAndLimits(t *testing.T) {
	validData := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\ncontent"))
	invalid := []any{
		map[string]any{"type": "input_file", "file_id": "file_external"},
		map[string]any{"type": "input_file", "file_data": validData, "filename": "../report.pdf"},
		map[string]any{"type": "input_file", "file_data": validData, "filename": "report.txt"},
		map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("not-pdf")), "filename": "report.pdf"},
		map[string]any{"type": "input_file", "file_data": validData, "filename": "report.pdf", "unexpected": true},
	}
	for _, input := range invalid {
		if _, err := ResponseFileAttachments([]any{input}); err == nil {
			t.Fatalf("accepted invalid input: %#v", input)
		}
	}
	tooMany := make([]any, MaxResponseFileAttachments+1)
	for index := range tooMany {
		tooMany[index] = map[string]any{"type": "input_file", "file_data": validData, "filename": "report.pdf"}
	}
	if _, err := ResponseFileAttachments(tooMany); err == nil {
		t.Fatal("accepted too many files")
	}
	oversized := map[string]any{"type": "input_file", "file_data": "data:application/pdf;base64," + base64.StdEncoding.EncodeToString([]byte("%PDF-"+strings.Repeat("x", MaxResponseFileBytes))), "filename": "report.pdf"}
	if _, err := ResponseFileAttachments([]any{oversized}); err == nil {
		t.Fatal("accepted oversized file")
	}
}
