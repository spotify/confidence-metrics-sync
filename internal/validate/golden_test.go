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

// TestValidFixtures is the drift test: every valid fixture must parse with
// strict decoding AND pass schema + semantic validation with zero
// diagnostics. It keeps the JSON Schema and the Go structs in sync.
func TestValidFixtures(t *testing.T) {
	files, _, diags, err := parser.LoadDir("testdata/valid")
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) > 0 {
		t.Fatalf("parse diagnostics on valid fixtures: %v", diags)
	}
	if len(files) == 0 {
		t.Fatal("no valid fixtures found")
	}
	vdiags, err := Files(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(vdiags) > 0 {
		var buf bytes.Buffer
		report.Text(&buf, vdiags, len(files))
		t.Fatalf("diagnostics on valid fixtures:\n%s", buf.String())
	}
}

// TestInvalidFixtures runs every case directory under testdata/invalid and
// compares the text report against its expected.txt golden file. Run with
// UPDATE_GOLDEN=1 to regenerate.
func TestInvalidFixtures(t *testing.T) {
	runGoldenCases(t, "testdata/invalid", true)
}

// TestWarningFixtures covers cases that must produce warnings but no errors
// (deliberate decision: "nothing defined" does not fail validation).
func TestWarningFixtures(t *testing.T) {
	runGoldenCases(t, "testdata/warnings", false)
}

func runGoldenCases(t *testing.T, root string, wantErrors bool) {
	cases, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if !c.IsDir() || strings.HasPrefix(c.Name(), ".") {
			continue
		}
		t.Run(c.Name(), func(t *testing.T) {
			dir := filepath.Join(root, c.Name())
			files, found, diags, err := parser.LoadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			vdiags, err := Files(files)
			if err != nil {
				t.Fatal(err)
			}
			diags = append(diags, vdiags...)
			report.Sort(diags)

			var buf bytes.Buffer
			report.Text(&buf, diags, found)
			got := buf.String()

			golden := filepath.Join(dir, "expected.txt")
			if os.Getenv("UPDATE_GOLDEN") != "" {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden file (run with UPDATE_GOLDEN=1 to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("output mismatch\n--- want ---\n%s\n--- got ---\n%s", want, got)
			}

			if wantErrors && !report.HasErrors(diags) {
				t.Error("invalid fixture produced no error-severity diagnostics")
			}
			if !wantErrors {
				if report.HasErrors(diags) {
					t.Error("warning fixture produced error-severity diagnostics")
				}
				if len(diags) == 0 {
					t.Error("warning fixture produced no diagnostics at all")
				}
			}
		})
	}
}
