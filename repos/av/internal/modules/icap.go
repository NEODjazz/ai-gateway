package modules

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

var ErrContentRejected = errors.New("content rejected")

type ICAPClient struct {
	Host    string
	Port    string
	Service string
	Timeout time.Duration
	Dial    func(ctx context.Context, network string, address string) (net.Conn, error)
}

type ICAPScanResult struct {
	StatusCode int
	Status     string
	Headers    textproto.MIMEHeader
}

func NewICAPClient(host string, port string, service string) ICAPClient {
	return ICAPClient{
		Host:    host,
		Port:    port,
		Service: service,
		Timeout: 5 * time.Second,
	}
}

func (c ICAPClient) Scan(ctx context.Context, moduleName string, payload []byte) (ICAPScanResult, error) {
	return c.ScanContent(ctx, moduleName, "text/plain; charset=utf-8", payload)
}

func (c ICAPClient) ScanContent(ctx context.Context, moduleName string, contentType string, payload []byte) (ICAPScanResult, error) {
	if strings.TrimSpace(c.Host) == "" {
		return ICAPScanResult{}, errors.New("ICAP_HOST is empty")
	}
	if strings.TrimSpace(c.Port) == "" {
		return ICAPScanResult{}, errors.New("ICAP_PORT is empty")
	}

	address := net.JoinHostPort(c.Host, c.Port)
	dialer := c.Dial
	if dialer == nil {
		netDialer := &net.Dialer{Timeout: c.timeout()}
		dialer = netDialer.DialContext
	}

	dialCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	conn, err := dialer(dialCtx, "tcp", address)
	if err != nil {
		return ICAPScanResult{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.timeout()))

	if _, err := conn.Write(c.requestBytes(moduleName, contentType, payload)); err != nil {
		return ICAPScanResult{}, err
	}

	reader := textproto.NewReader(bufio.NewReader(conn))
	line, err := reader.ReadLine()
	if err != nil {
		return ICAPScanResult{}, err
	}
	statusCode, err := parseICAPStatusCode(line)
	if err != nil {
		return ICAPScanResult{}, err
	}
	headers, err := reader.ReadMIMEHeader()
	if err != nil {
		return ICAPScanResult{}, err
	}

	result := ICAPScanResult{StatusCode: statusCode, Status: line, Headers: headers}
	if statusCode < 200 || statusCode >= 300 {
		return result, fmt.Errorf("icap service returned %s", line)
	}
	if result.Rejected() {
		return result, fmt.Errorf("%w: %s", ErrContentRejected, result.RejectionReason())
	}
	return result, nil
}

func (c ICAPClient) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 5 * time.Second
	}
	return c.Timeout
}

func (c ICAPClient) requestBytes(moduleName string, contentType string, payload []byte) []byte {
	service := strings.TrimSpace(c.Service)
	if service == "" {
		service = "/" + moduleName
	}
	if !strings.HasPrefix(service, "/") {
		service = "/" + service
	}
	serviceURL := "icap://" + net.JoinHostPort(c.Host, c.Port) + service
	httpHeader := fmt.Sprintf(
		"POST /ai-gateway/%s HTTP/1.1\r\nHost: ai-gateway\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n",
		moduleName,
		contentType,
		len(payload),
	)

	var request bytes.Buffer
	request.WriteString("REQMOD " + serviceURL + " ICAP/1.0\r\n")
	request.WriteString("Host: " + net.JoinHostPort(c.Host, c.Port) + "\r\n")
	request.WriteString("Allow: 204\r\n")
	request.WriteString("Encapsulated: req-hdr=0, req-body=" + strconv.Itoa(len(httpHeader)) + "\r\n")
	request.WriteString("\r\n")
	request.WriteString(httpHeader)
	if len(payload) > 0 {
		request.WriteString(strconv.FormatInt(int64(len(payload)), 16))
		request.WriteString("\r\n")
		request.Write(payload)
		request.WriteString("\r\n")
	}
	request.WriteString("0\r\n\r\n")
	return request.Bytes()
}

func parseICAPStatusCode(line string) (int, error) {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 || parts[0] != "ICAP/1.0" {
		return 0, fmt.Errorf("invalid icap status line %q", line)
	}
	return strconv.Atoi(parts[1])
}

func (r ICAPScanResult) Rejected() bool {
	if headerBool(r.Headers.Get("X-Infection-Found")) ||
		headerBool(r.Headers.Get("X-Virus-Found")) ||
		headerBool(r.Headers.Get("X-Blocked")) ||
		headerBool(r.Headers.Get("X-DLP-Blocked")) {
		return true
	}
	encapsulated := strings.ToLower(r.Headers.Get("Encapsulated"))
	return strings.Contains(encapsulated, "res-hdr=")
}

func (r ICAPScanResult) RejectionReason() string {
	for _, key := range []string{"X-Virus-ID", "X-Infection-Found", "X-DLP-Policy", "X-Blocked-Reason"} {
		if value := strings.TrimSpace(r.Headers.Get(key)); value != "" {
			return value
		}
	}
	return "icap service rejected request"
}

func headerBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "found", "blocked":
		return true
	default:
		return false
	}
}
