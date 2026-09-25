package azureurl

import (
	"net/url"
	"strings"
)

// ValidPath rejects path forms that upstream proxies may normalize differently.
func ValidPath(parsed *url.URL) bool {
	if parsed == nil || !validDecodedPath(parsed.Path) {
		return false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	return !strings.Contains(escaped, "%2f") && !strings.Contains(escaped, "%5c")
}

func validDecodedPath(path string) bool {
	if strings.ContainsAny(path, "\\\x00") || strings.Contains(path, "//") || strings.Contains(path, "%") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// ProjectPath returns the full project path, including a reverse-proxy prefix.
func ProjectPath(path string) (string, bool) {
	if !validDecodedPath(path) {
		return "", false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	projectIndex := len(parts) - 3
	if len(parts) >= 5 && parts[len(parts)-2] == "openai" && parts[len(parts)-1] == "v1" {
		projectIndex = len(parts) - 5
	}
	if projectIndex < 0 || parts[projectIndex] != "api" || parts[projectIndex+1] != "projects" || parts[projectIndex+2] == "" {
		return "", false
	}
	if projectIndex+3 != len(parts) && (projectIndex+5 != len(parts) || parts[projectIndex+3] != "openai" || parts[projectIndex+4] != "v1") {
		return "", false
	}
	return "/" + strings.Join(parts[:projectIndex+3], "/"), true
}
