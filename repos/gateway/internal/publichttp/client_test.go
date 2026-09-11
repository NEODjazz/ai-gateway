package publichttp

import (
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestPublicIPRejectsNonPublicRanges(t *testing.T) {
	tests := []struct {
		address string
		public  bool
	}{
		{"8.8.8.8", true}, {"2001:4860:4860::8888", true},
		{"0.0.0.0", false}, {"127.0.0.1", false}, {"10.0.0.1", false}, {"172.16.0.1", false}, {"192.168.0.1", false},
		{"100.64.0.1", false}, {"169.254.169.254", false}, {"192.0.2.1", false}, {"198.18.0.1", false}, {"198.51.100.1", false}, {"203.0.113.1", false}, {"240.0.0.1", false}, {"224.0.0.1", false},
		{"::", false}, {"::1", false}, {"100::1", false}, {"2001:2::1", false}, {"2001:db8::1", false}, {"fc00::1", false}, {"fe80::1", false}, {"ff02::1", false},
	}
	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			if got := PublicIP(net.ParseIP(test.address)); got != test.public {
				t.Fatalf("PublicIP(%q)=%t, want %t", test.address, got, test.public)
			}
		})
	}
	if PublicIP(nil) {
		t.Fatal("nil IP accepted")
	}
}

func TestNewClientDisablesProxyRedirectsAndOldTLS(t *testing.T) {
	client := NewClient(3 * time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.DialContext == nil || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 || client.Timeout != 3*time.Second {
		t.Fatalf("unsafe client configuration: client=%+v transport=%+v", client, transport)
	}
	request, err := http.NewRequest(http.MethodGet, "https://example.test/next", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); err != http.ErrUseLastResponse {
		t.Fatalf("redirect policy error=%v", err)
	}
}
