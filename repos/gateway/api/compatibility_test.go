package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestReviewedCompatibilityFindingsAreScoped(t *testing.T) {
	for _, name := range []string{"err-ignore.txt", "warn-ignore.txt"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("compatibility/" + name)
			if err != nil {
				t.Fatal(err)
			}
			for _, pattern := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if !strings.HasPrefix(pattern, "^in API ") || !strings.HasSuffix(pattern, "$") {
					t.Fatalf("finding must be anchored to a concrete API operation: %q", pattern)
				}
				compiled, err := regexp.Compile(pattern)
				if err != nil {
					t.Fatalf("invalid finding pattern %q: %v", pattern, err)
				}
				if compiled.MatchString("in API GET /unreviewed the `content_stored` response property const value `false` was removed for the status `200`") {
					t.Fatalf("finding accepts an unrelated endpoint: %q", pattern)
				}
			}
		})
	}
}

func TestAgentCompatibilityDoesNotAcceptUnreviewedChanges(t *testing.T) {
	data, err := os.ReadFile("compatibility/err-ignore.txt")
	if err != nil {
		t.Fatal(err)
	}
	accepted := "in API GET /admin/v1/agent-profiles the `content_stored` response property const value `false` was removed for the status `200`"
	cases := []struct {
		finding string
		allowed bool
	}{
		{accepted, true},
		{strings.Replace(accepted, "content_stored", "other_field", 1), false},
		{strings.Replace(accepted, "false", "true", 1), false},
		{strings.Replace(accepted, "200", "201", 1), false},
		{strings.Replace(accepted, "/admin/v1/agent-profiles", "/admin/v1/agent-profiles/other", 1), false},
		{accepted + " additional change", false},
	}
	for _, tc := range cases {
		allowed := false
		for _, pattern := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				t.Fatal(err)
			}
			allowed = allowed || compiled.MatchString(tc.finding)
		}
		if allowed != tc.allowed {
			t.Errorf("finding %q: allowed=%v, want %v", tc.finding, allowed, tc.allowed)
		}
	}
}
