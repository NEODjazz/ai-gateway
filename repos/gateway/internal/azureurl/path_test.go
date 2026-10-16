package azureurl

import (
	"net/url"
	"testing"
)

func TestValidPath(t *testing.T) {
	for _, test := range []struct {
		path  string
		valid bool
	}{
		{"/tenant/api/projects/project-a/openai/v1", true},
		{"/tenant%20a/api/projects/project-a", true},
		{"/tenant/../api/projects/project-a", false},
		{"/tenant/%2e%2e/api/projects/project-a", false},
		{"/tenant/api/projects/project-a%2fother", false},
		{"/tenant/api/projects/project-a%5cother", false},
		{"/tenant/api/projects/project-a%252fother", false},
		{"/tenant//api/projects/project-a", false},
	} {
		t.Run(test.path, func(t *testing.T) {
			parsed, err := url.Parse("https://proxy.example.test" + test.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := ValidPath(parsed); got != test.valid {
				t.Fatalf("ValidPath(%q) = %t, want %t", test.path, got, test.valid)
			}
		})
	}
}

func TestProjectPathRejectsUnsafeSegments(t *testing.T) {
	for _, path := range []string{
		"/tenant/api/projects/..",
		"/tenant/../api/projects/project-a",
		"/tenant/api/projects/project-a/extra",
	} {
		if project, ok := ProjectPath(path); ok {
			t.Fatalf("unsafe project path %q accepted as %q", path, project)
		}
	}
}
