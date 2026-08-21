package main

import "testing"

func TestValidImageSignature(t *testing.T) {
	if !validImageSignature("image/png", []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("valid PNG signature rejected")
	}
	if validImageSignature("image/png", []byte("not-a-png")) {
		t.Fatal("spoofed PNG accepted")
	}
	if validImageSignature("application/octet-stream", []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("unsupported media type accepted")
	}
}
