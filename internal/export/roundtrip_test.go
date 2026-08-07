package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/parser"
	"github.com/spotify/confidence-metrics-sync/internal/plan"
	"github.com/spotify/confidence-metrics-sync/internal/report"
	"github.com/spotify/confidence-metrics-sync/internal/validate"
)

// TestRoundTrip is the export invariant, offline edition: take the wire
// resources the CLI itself produces from the fixture corpus (as the server
// would store them), export them to YAML, run that YAML through the full
// parse+validate+map pipeline, and require the resulting wire request to be
// IDENTICAL to the original. This is what "freshly exported files plan as a
// no-op" means without needing a live server.
func TestRoundTrip(t *testing.T) {
	entities := []confidence.Entity{{Name: "entities/u1", DisplayName: "user"}}
	refs := plan.RefsFromEntities(entities)

	// Original wire state: build it from the fixture corpus via the forward
	// mapping — exactly what a sync would have persisted.
	files, _, pdiags, err := parser.LoadDir("../validate/testdata/valid")
	if err != nil || len(pdiags) > 0 {
		t.Fatalf("fixture load: %v %v", err, pdiags)
	}
	original, bdiags := plan.BuildSyncResources(plan.Normalize(files), refs)
	if len(bdiags) > 0 {
		t.Fatalf("fixture mapping: %v", bdiags)
	}

	// Give the "stored" resources server-assigned names so references work.
	var factTables []confidence.FactTable
	var measurements []confidence.Measurement
	var metrics []confidence.Metric
	ftNames := map[string]string{}
	for i, r := range original {
		switch {
		case r.FactTable != nil:
			ft := *r.FactTable
			ft.Name = "factTables/ft" + strconv.Itoa(i)
			ft.State = "TABLE_STATE_ACTIVE"
			ftNames[ft.DisplayName] = ft.Name
			factTables = append(factTables, ft)
		}
	}
	msNames := map[string]string{}
	for i, r := range original {
		if r.Measurement == nil {
			continue
		}
		m := *r.Measurement
		m.Name = "measurements/ms" + strconv.Itoa(i)
		m.FactTable = ftNames[m.FactTable] // server stores resource names
		msNames[m.DisplayName] = m.Name
		measurements = append(measurements, m)
	}
	for i, r := range original {
		if r.Metric == nil {
			continue
		}
		m := *r.Metric
		m.Name = "metrics/m" + strconv.Itoa(i)
		m.Measurement = msNames[m.Measurement]
		m.State = "ACTIVE"
		metrics = append(metrics, m)
	}

	// Export.
	file, warnings, _, err := Build(entities, factTables, measurements, metrics, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected export warnings: %v", warnings)
	}
	data, err := yaml.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}

	// Re-import: parse, validate, map.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "exported.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	reFiles, _, reDiags, err := parser.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	vdiags, err := validate.Files(reFiles)
	if err != nil {
		t.Fatal(err)
	}
	reDiags = append(reDiags, vdiags...)
	if report.HasErrors(reDiags) {
		t.Fatalf("exported YAML fails validation:\n%v\n--- yaml ---\n%s", reDiags, data)
	}
	roundTripped, mdiags := plan.BuildSyncResources(plan.Normalize(reFiles), refs)
	if len(mdiags) > 0 {
		t.Fatalf("exported YAML fails mapping: %v", mdiags)
	}

	// The round-tripped wire request must be identical to the original.
	if len(roundTripped) != len(original) {
		t.Fatalf("resource count changed: %d -> %d", len(original), len(roundTripped))
	}
	want := canonical(t, original)
	got := canonical(t, roundTripped)
	if want != got {
		t.Errorf("round trip is not a no-op\n--- original ---\n%s\n--- round-tripped ---\n%s", want, got)
	}
}

// canonical renders resources as sorted, indented JSON for comparison.
func canonical(t *testing.T, resources []confidence.SyncResource) string {
	t.Helper()
	// Sort deterministically: kind, then display name.
	key := func(r confidence.SyncResource) string {
		switch {
		case r.FactTable != nil:
			return "1:" + r.FactTable.DisplayName
		case r.Measurement != nil:
			return "2:" + r.Measurement.DisplayName
		default:
			return "3:" + r.Metric.DisplayName
		}
	}
	sorted := append([]confidence.SyncResource{}, resources...)
	for i := range sorted {
		for j := i + 1; j < len(sorted); j++ {
			if key(sorted[j]) < key(sorted[i]) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	b, err := json.MarshalIndent(sorted, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
