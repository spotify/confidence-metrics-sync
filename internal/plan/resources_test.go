package plan

import (
	"strings"
	"testing"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/parser"
	"github.com/spotify/confidence-metrics-sync/internal/report"
)

func desiredFixture(t *testing.T) Desired {
	t.Helper()
	files, _, diags, err := parser.LoadDir("../validate/testdata/valid")
	if err != nil || len(diags) > 0 {
		t.Fatalf("fixture load failed: %v %v", err, diags)
	}
	return Normalize(files)
}

func userRefs() Refs {
	return RefsFromEntities([]confidence.Entity{{Name: "entities/user1", DisplayName: "user"}})
}

func TestNormalizeMergesBothFlavors(t *testing.T) {
	d := desiredFixture(t)
	if len(d.FactTables) != 6 || len(d.Measurements) != 11 || len(d.Metrics) != 10 {
		t.Fatalf("unexpected counts: %d fact tables, %d measurements, %d metrics",
			len(d.FactTables), len(d.Measurements), len(d.Metrics))
	}
	for _, m := range d.Metrics {
		if m.Entity == "" || m.Measurement == "" {
			t.Errorf("metric %q missing inherited entity/measurement", m.DisplayName)
		}
	}
}

func TestGeneratedSQLForTableSource(t *testing.T) {
	d := desiredFixture(t)
	var streaming *DesiredFactTable
	for i := range d.FactTables {
		if d.FactTables[i].Def.DisplayName == "Hourly Stream" {
			streaming = &d.FactTables[i]
		}
	}
	if streaming == nil {
		t.Fatal("fixture fact table not found")
	}
	want := "SELECT user_id, event_time, (ms_played / 60000) AS minutes_played, (stream_count > 0) AS had_stream, platform, content_type FROM analytics.prod.hourly_stream"
	if streaming.SQL != want {
		t.Fatalf("generated SQL mismatch:\nwant %s\ngot  %s", want, streaming.SQL)
	}
}

func TestBuildSyncResources(t *testing.T) {
	resources, diags := BuildSyncResources(desiredFixture(t), userRefs())
	if report.HasErrors(diags) {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(resources) != 27 { // 6 fact tables + 11 measurements + 10 metrics
		t.Fatalf("expected 27 resources, got %d", len(resources))
	}

	// Dependency order: fact tables, then measurements, then metrics.
	kinds := ""
	for _, r := range resources {
		switch {
		case r.FactTable != nil:
			if !strings.HasPrefix(r.FactTable.Name, "factTables/") {
				t.Errorf("fact table name not propagated: %q", r.FactTable.Name)
			}
			kinds += "F"
		case r.Measurement != nil:
			kinds += "S"
		case r.Metric != nil:
			kinds += "M"
		}
	}
	if kinds != "FFFFFFSSSSSSSSSSSMMMMMMMMMM" {
		t.Fatalf("unexpected resource order: %s", kinds)
	}

	for _, r := range resources {
		switch {
		case r.FactTable != nil:
			for _, e := range r.FactTable.Entities {
				if e.Entity != "entities/user1" {
					t.Errorf("fact table entity not resolved to resource name: %q", e.Entity)
				}
			}
			// Owner is optional; when set in YAML it must survive the mapping.
			if r.FactTable.DisplayName == "Revenue Events" && r.FactTable.Owner != "identities/revenue-team" {
				t.Errorf("fact table %q lost its owner: %q", r.FactTable.DisplayName, r.FactTable.Owner)
			}
		case r.Measurement != nil:
			if !strings.HasPrefix(r.Measurement.Name, "measurements/") {
				t.Errorf("measurement name not propagated: %q", r.Measurement.Name)
			}
			if !strings.HasPrefix(r.Measurement.FactTable, "factTables/") {
				t.Errorf("measurement fact table should be a resource name, got %q", r.Measurement.FactTable)
			}
			if r.Measurement.Entity != "entities/user1" {
				t.Errorf("measurement entity not resolved: %q", r.Measurement.Entity)
			}
			// Owner is optional (server defaults to the syncing client);
			// when set in YAML it must survive the mapping.
			if r.Measurement.DisplayName == "Hourly Minutes Played" && r.Measurement.Owner != "identities/abc123" {
				t.Errorf("measurement %q lost its owner: %q", r.Measurement.DisplayName, r.Measurement.Owner)
			}
		case r.Metric != nil:
			if !strings.HasPrefix(r.Metric.Name, "metrics/") {
				t.Errorf("metric name not propagated: %q", r.Metric.Name)
			}
			if !strings.HasPrefix(r.Metric.Measurement, "measurements/") {
				t.Errorf("metric measurement should be a resource name, got %q", r.Metric.Measurement)
			}
			if r.Metric.Source != nil {
				t.Error("client must never set source — the server derives it from the reference")
			}
		}
	}
}

func TestBuildSyncResourcesMissingEntity(t *testing.T) {
	resources, diags := BuildSyncResources(desiredFixture(t), RefsFromEntities(nil))
	if !report.HasErrors(diags) {
		t.Fatal("expected errors for unresolvable entity")
	}
	for _, r := range resources {
		if r.FactTable != nil {
			t.Error("fact tables with unresolvable entities must not be emitted")
		}
	}
}
