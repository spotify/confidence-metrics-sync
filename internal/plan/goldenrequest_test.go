package plan

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
)

// TestGoldenWireRequest pins the exact ApplyMetricsSync request the CLI
// builds from the fixture corpus. testdata/golden-request.json is the
// CONTRACT ARTIFACT: the epx-metrics side should replay it against
// ApplyMetricsSync (dry_run) in a test and assert every resource plans as
// CREATE — that is what catches "server rejects CLI-shaped input" bugs
// (e.g. requiring column types the CLI never sends, since Column.type is
// OUTPUT_ONLY and skip_sql_preview is never set).
//
// Regenerate with UPDATE_GOLDEN=1 after deliberate mapping changes — and
// notify epx-metrics when it changes.
func TestGoldenWireRequest(t *testing.T) {
	resources, diags := BuildSyncResources(desiredFixture(t), userRefs())
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	req := confidence.ApplyMetricsSyncRequest{
		Reference: "golden-contract-test",
		Resources: resources,
		DryRun:    true,
	}
	got, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	const golden = "testdata/golden-request.json"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden file (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("wire request changed — if deliberate, regenerate with UPDATE_GOLDEN=1 and notify epx-metrics (contract artifact)\n--- got ---\n%s", got)
	}
}
