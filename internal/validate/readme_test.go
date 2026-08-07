package validate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spotify/confidence-metrics-sync/internal/parser"
	"github.com/spotify/confidence-metrics-sync/internal/report"
)

// TestReadmeQuickStartValidates guards the README's Quick Start example: it
// must pass the tool's own validation. Found the hard way in PR #1 review —
// the example was missing required owner fields.
func TestReadmeQuickStartValidates(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	example := extractFirstYAMLBlock(t, string(readme))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "quickstart.yaml"), []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}

	files, _, diags, err := parser.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	vdiags, err := Files(files)
	if err != nil {
		t.Fatal(err)
	}
	diags = append(diags, vdiags...)
	if report.HasErrors(diags) {
		var buf bytes.Buffer
		report.Text(&buf, diags, 1)
		t.Fatalf("the README Quick Start example fails validation:\n%s", buf.String())
	}
}

// extractFirstYAMLBlock returns the contents of the first ```yaml fence.
func extractFirstYAMLBlock(t *testing.T, markdown string) string {
	t.Helper()
	const fence = "```yaml"
	start := strings.Index(markdown, fence)
	if start < 0 {
		t.Fatal("no ```yaml block found in README")
	}
	rest := markdown[start+len(fence):]
	end := strings.Index(rest, "```")
	if end < 0 {
		t.Fatal("unterminated ```yaml block in README")
	}
	return rest[:end]
}
