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

func TestICAPReadinessUsesContentFreeOptions(t *testing.T) {
	requests := make(chan string, 1)
	client := NewICAPClient("icap.test", "1344", "/dlp")
	client.Dial = icapReadyTestDial("ICAP/1.0 204 No Content\r\nMethods: REQMOD\r\n\r\n", requests)
	if err := client.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	request := <-requests
	if !strings.HasPrefix(request, "OPTIONS icap://icap.test:1344/dlp ICAP/1.0\r\n") || strings.Contains(request, "REQMOD") {
		t.Fatalf("unexpected readiness request: %q", request)
	}

	client.Dial = icapReadyTestDial("ICAP/1.0 503 Unavailable\r\n\r\n", make(chan string, 1))
	if err := client.Ready(context.Background()); err == nil {
		t.Fatal("non-successful ICAP readiness response was accepted")
	}
}

func icapReadyTestDial(response string, requests chan<- string) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			reader := bufio.NewReader(server)
			var request strings.Builder
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				request.WriteString(line)
				if line == "\r\n" {
					break
				}
			}
			requests <- request.String()
			_, _ = io.WriteString(server, response)
		}()
		return client, nil
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
