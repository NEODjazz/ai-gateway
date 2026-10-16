package gateway

import (
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var playgroundNumericHostname = regexp.MustCompile(`^(?:[0-9]+|0x[0-9a-f]*)$`)

var playgroundHostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// WithPlaygroundOrigins permits explicit test-key HTTP calls to exact browser
// origins. It does not enable CORS, trust browser SSO, or permit cross-origin
// Realtime tickets. WithAdminUI alone retains the same-origin policy.
func (h Handler) WithPlaygroundOrigins(origins []string) (Handler, error) {
	if len(origins) > 16 {
		return h, errors.New("ADMIN_UI_PLAYGROUND_ORIGINS must contain at most 16 exact origins")
	}
	validated := make([]string, 0, len(origins))
	seen := make(map[string]bool)
	for _, value := range origins {
		origin, err := normalizePlaygroundOrigin(value)
		if err != nil {
			// Never echo potentially credential-bearing invalid input in startup logs.
			return h, errors.New("ADMIN_UI_PLAYGROUND_ORIGINS requires exact HTTPS origins (HTTP is allowed only for loopback), without credentials, paths, wildcards, query or fragment")
		}
		if !seen[origin] {
			validated = append(validated, origin)
			seen[origin] = true
		}
	}
	h.playgroundOrigins = validated
	return h, nil
}

func normalizePlaygroundOrigin(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := url.Parse(trimmed)
	if strings.ContainsAny(trimmed, "?#\\") || err != nil || parsed == nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		return "", errors.New("invalid origin")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", errors.New("invalid scheme")
	}
	host := strings.ToLower(parsed.Hostname())
	loopback := host == "localhost"
	if address, err := netip.ParseAddr(host); err == nil {
		loopback = address.IsLoopback()
		if address.Zone() != "" || address.Is4In6() {
			return "", errors.New("invalid host")
		}
		host = address.String()
		if address.Is6() {
			host = "[" + host + "]"
		}
	} else {
		labels := strings.Split(host, ".")
		// Browsers interpret numeric/hexadecimal suffixes as IPv4 addresses.
		// Require their canonical IP form instead of accepting a different CSP host.
		if len(host) > 253 || strings.HasPrefix(parsed.Host, "[") || playgroundNumericHostname.MatchString(labels[len(labels)-1]) {
			return "", errors.New("invalid host")
		}
		for _, label := range labels {
			if !playgroundHostname.MatchString(label) {
				return "", errors.New("invalid host")
			}
		}
	}
	if parsed.Scheme == "http" && !loopback {
		return "", errors.New("insecure origin")
	}
	port := parsed.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", errors.New("invalid port")
		}
		port = strconv.Itoa(number)
		if parsed.Scheme == "https" && port == "443" || parsed.Scheme == "http" && port == "80" {
			port = ""
		}
	}
	if port != "" {
		host += ":" + port
	}
	return parsed.Scheme + "://" + host, nil
}
