package api_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)[:space:]]+)`)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve documentation test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
}

func repositoryMarkdown(t *testing.T, root string) []string {
	t.Helper()
	files := []string{filepath.Join(root, "README.md")}
	for _, directory := range []string{filepath.Join(root, "docs"), filepath.Join(root, "repos")} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && (entry.Name() == "node_modules" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			isDocsMarkdown := strings.HasPrefix(path, filepath.Join(root, "docs")+string(filepath.Separator)) && strings.HasSuffix(entry.Name(), ".md")
			if !entry.IsDir() && (strings.EqualFold(entry.Name(), "README.md") || isDocsMarkdown) {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk documentation in %s: %v", directory, err)
		}
	}
	sort.Strings(files)
	return files
}

func TestRepositoryMarkdownLinksResolve(t *testing.T) {
	root := repositoryRoot(t)
	for _, document := range repositoryMarkdown(t, root) {
		contents, err := os.ReadFile(document)
		if err != nil {
			t.Errorf("read %s: %v", document, err)
			continue
		}
		for _, match := range markdownLink.FindAllStringSubmatch(string(contents), -1) {
			target := strings.Trim(match[1], "<>")
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") || filepath.IsAbs(target) {
				continue
			}
			if index := strings.IndexByte(target, '#'); index >= 0 {
				target = target[:index]
			}
			if target == "" {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(document), filepath.FromSlash(target)))
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s links to missing %q", document, target)
			}
		}
	}
}

func TestDocumentationHasNoKnownStaleContracts(t *testing.T) {
	root := repositoryRoot(t)
	for _, document := range repositoryMarkdown(t, root) {
		contents, err := os.ReadFile(document)
		if err != nil {
			t.Fatalf("read %s: %v", document, err)
		}
		for _, stale := range []string{
			"DLP_ICAP_SERVICE=/reqmod",
			"AV_ICAP_SERVICE=/avscan",
			"service: /reqmod",
			"service: /avscan",
			"Deanonymization не применяется",
		} {
			if strings.Contains(string(contents), stale) {
				t.Errorf("%s contains stale contract %q", document, stale)
			}
		}
	}

	contents, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(contents), "\n") + 1; lines > 200 {
		t.Fatalf("root README has %d lines; move detailed material into docs", lines)
	}
}

func TestHelmMigrationCopiesMatchSources(t *testing.T) {
	root := repositoryRoot(t)
	postgresChart := filepath.Join(root, "charts", "postgres", "templates", "migrations-configmap.yaml")
	clickHouseChart := filepath.Join(root, "charts", "clickhouse", "templates", "migrations-configmap.yaml")
	pairs := map[string]string{
		filepath.Join(root, "migrations", "postgres", "007_gateway_control_plane.sql"):         postgresChart,
		filepath.Join(root, "migrations", "postgres", "034_gateway_cached_contents.sql"):       postgresChart,
		filepath.Join(root, "migrations", "postgres", "035_gateway_cached_content_policy.sql"): postgresChart,
	}
	for _, item := range []struct {
		directory string
		chart     string
	}{
		{filepath.Join(root, "repos", "auth", "migrations", "postgres"), postgresChart},
		{filepath.Join(root, "repos", "billing", "migrations", "postgres"), postgresChart},
		{filepath.Join(root, "repos", "billing", "migrations", "clickhouse"), clickHouseChart},
	} {
		matches, err := filepath.Glob(filepath.Join(item.directory, "*.sql"))
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range matches {
			pairs[source] = item.chart
		}
	}
	for source, chart := range pairs {
		sourceSQL, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read migration %s: %v", source, err)
		}
		chartYAML, err := os.ReadFile(chart)
		if err != nil {
			t.Fatalf("read chart %s: %v", chart, err)
		}
		embedded, ok := embeddedMigration(string(chartYAML), filepath.Base(source))
		if !ok {
			t.Errorf("%s does not embed %s", chart, filepath.Base(source))
			continue
		}
		if normalizeSQL(string(sourceSQL)) != normalizeSQL(embedded) {
			t.Errorf("Helm copy of %s differs from source", source)
		}
	}
}

func embeddedMigration(chart, name string) (string, bool) {
	pattern := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + `: \|\n((?:    [^\n]*(?:\n|$)|\n)*)`)
	match := pattern.FindStringSubmatch(chart)
	if len(match) != 2 {
		return "", false
	}
	lines := strings.Split(match[1], "\n")
	for index, line := range lines {
		lines[index] = strings.TrimPrefix(line, "    ")
	}
	return strings.Join(lines, "\n"), true
}

func normalizeSQL(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
