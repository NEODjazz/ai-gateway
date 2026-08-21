package modules

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

func TestICAPNon2xxVirusHeaderIsContentRejection(t *testing.T) {
	client := NewICAPClient("icap.test", "1344", "/av")
	client.Dial = avICAPTestDial("ICAP/1.0 403 Forbidden\r\nX-Virus-Found: true\r\nX-Virus-ID: EICAR-Test-File\r\n\r\n")
	result, err := client.Scan(context.Background(), "av", []byte("EICAR"))
	if !errors.Is(err, ErrContentRejected) || result.StatusCode != 403 || !strings.Contains(err.Error(), "EICAR-Test-File") {
		t.Fatalf("expected classified AV rejection, result=%+v err=%v", result, err)
	}
}

func TestICAPNon2xxWithoutVirusSignalRemainsUpstreamError(t *testing.T) {
	client := NewICAPClient("icap.test", "1344", "/av")
	client.Dial = avICAPTestDial("ICAP/1.0 500 Server Error\r\nISTag: test\r\n\r\n")
	_, err := client.Scan(context.Background(), "av", []byte("clean"))
	if err == nil || errors.Is(err, ErrContentRejected) {
		t.Fatalf("expected technical ICAP error, got %v", err)
	}
}

func avICAPTestDial(response string) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			reader := bufio.NewReader(server)
			for {
				line, err := reader.ReadString('\n')
				if err != nil || line == "0\r\n" {
					_, _ = reader.ReadString('\n')
					break
				}
			}
			_, _ = io.WriteString(server, response)
		}()
		return client, nil
	}
}

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
