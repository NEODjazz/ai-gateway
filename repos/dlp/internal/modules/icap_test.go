package modules

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

func TestICAPNon2xxBlockHeaderIsContentRejection(t *testing.T) {
	client := NewICAPClient("icap.test", "1344", "/dlp")
	client.Dial = icapTestDial("ICAP/1.0 403 Forbidden\r\nX-DLP-Blocked: true\r\nX-DLP-Policy: restricted-data\r\n\r\n")
	result, err := client.Scan(context.Background(), "dlp", []byte("secret"))
	if !errors.Is(err, ErrContentRejected) || result.StatusCode != 403 || !strings.Contains(err.Error(), "restricted-data") {
		t.Fatalf("expected classified DLP rejection, result=%+v err=%v", result, err)
	}
}

func TestICAPNon2xxWithoutBlockSignalRemainsUpstreamError(t *testing.T) {
	client := NewICAPClient("icap.test", "1344", "/dlp")
	client.Dial = icapTestDial("ICAP/1.0 403 Forbidden\r\nISTag: test\r\n\r\n")
	_, err := client.Scan(context.Background(), "dlp", []byte("secret"))
	if err == nil || errors.Is(err, ErrContentRejected) {
		t.Fatalf("expected technical ICAP error, got %v", err)
	}
}

func icapTestDial(response string) func(context.Context, string, string) (net.Conn, error) {
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
