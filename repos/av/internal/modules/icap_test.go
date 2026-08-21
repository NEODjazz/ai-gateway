package modules

import (
	"bytes"
	"strings"
	"testing"
)

func TestICAPRequestPreservesBinaryPayloadAndMediaType(t *testing.T) {
	payload := []byte{0x00, 0xff, 0x10, 0x20}
	request := NewICAPClient("icap.example", "1344", "/av").requestBytes("av", "image/png", payload)
	if !bytes.Contains(request, []byte("Content-Type: image/png\r\n")) {
		t.Fatalf("image media type missing from ICAP request: %q", request)
	}
	if !bytes.Contains(request, payload) {
		t.Fatal("binary payload missing from ICAP request")
	}
	if !strings.Contains(string(request), "4\r\n") {
		t.Fatalf("binary chunk length missing: %q", request)
	}
}
