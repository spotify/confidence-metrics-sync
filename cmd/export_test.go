package cmd

import (
	"strings"
	"testing"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
)

func exportFixtureBackend() *fakeBackend {
	return &fakeBackend{
		listFactTables: []confidence.FactTable{{
			Name: "factTables/ft1", DisplayName: "Hourly Stream", SQL: "SELECT 1",
			State:           "TABLE_STATE_ACTIVE",
			TimestampColumn: &confidence.Column{Name: "event_time"},
			Entities: []confidence.EntityColumnMapping{
				{Column: &confidence.Column{Name: "user_id"}, Entity: "entities/u1"},
			},
			Measures: []confidence.Measure{
				{Column: &confidence.Column{Name: "minutes_played"}, DisplayName: "minutes_played"},
			},
		}},
		listMeasurements: []confidence.Measurement{{
			Name: "measurements/ms1", DisplayName: "Hourly Minutes Played",
			Entity: "entities/u1", FactTable: "factTables/ft1", Owner: "identities/abc123",
			TypeSpec: &confidence.TypeSpec{AverageMetricSpec: &confidence.AverageMetricSpec{
				Measurement: &confidence.Column{Name: "minutes_played"},
				Aggregation: &confidence.Aggregation{Type: "AGGREGATION_TYPE_SUM"},
			}},
		}},
		listMetrics: []confidence.Metric{
			{
				Name: "metrics/m1", DisplayName: "Minutes Played - Day 1",
				Entity: "entities/u1", Measurement: "measurements/ms1",
				PreferredDirection: "INCREASE", State: "ACTIVE",
			},
			{
				Name: "metrics/m2", DisplayName: "Checkout Conversion - Day 7",
				Entity: "entities/u1", Measurement: "measurements/ms1",
				PreferredDirection: "INCREASE", State: "ACTIVE",
			},
			{
				Name: "metrics/m3", DisplayName: "Archived Metric",
				Entity: "entities/u1", Measurement: "measurements/ms1",
				State: "ARCHIVED",
			},
		},
	}
}

func TestExportDefaultIsAllMetrics(t *testing.T) {
	startFake(t, exportFixtureBackend())

	out, err := run(t, "export")
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Minutes Played - Day 1") ||
		!strings.Contains(out, "Checkout Conversion - Day 7") ||
		!strings.Contains(out, "measurement: measurements/ms1") {
		t.Errorf("all live metrics should export with resource-name references:\n%s", out)
	}
	if strings.Contains(out, "fact_tables:") || strings.Contains(out, "measurements:\n") {
		t.Errorf("without --with-dependencies only metrics are exported:\n%s", out)
	}
	if strings.Contains(out, "Archived Metric") {
		t.Errorf("archived metrics must not be exported:\n%s", out)
	}
}

func TestExportSelectsUnionOfPatterns(t *testing.T) {
	startFake(t, exportFixtureBackend())

	// Two exact names (spaces and all), deliberately miscased — both
	// metrics match, each via a different pattern.
	out, err := run(t, "export", "checkout conversion - day 7", "Minutes Played - DAY 1")
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Minutes Played - Day 1") ||
		!strings.Contains(out, "Checkout Conversion - Day 7") {
		t.Errorf("both patterns should select their metric:\n%s", out)
	}
}

func TestExportSinglePatternFiltersOthers(t *testing.T) {
	startFake(t, exportFixtureBackend())

	out, err := run(t, "export", "checkout conversion - day 7")
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Checkout Conversion - Day 7") {
		t.Errorf("exact name pattern should match:\n%s", out)
	}
	if strings.Contains(out, "Minutes Played - Day 1") {
		t.Errorf("non-matching metric must be filtered out:\n%s", out)
	}
}

func TestExportGlobPattern(t *testing.T) {
	startFake(t, exportFixtureBackend())

	// Plain text is an exact match — a partial name selects nothing...
	if _, err := run(t, "export", "conversion"); err == nil ||
		!strings.Contains(err.Error(), "no metrics matched") {
		t.Fatalf("partial plain pattern must not substring-match, got %v", err)
	}

	// ...partial selection is spelled explicitly with glob characters.
	out, err := run(t, "export", "minutes*")
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Minutes Played - Day 1") {
		t.Errorf("case-insensitive glob should match:\n%s", out)
	}
	if strings.Contains(out, "Checkout Conversion") {
		t.Errorf("glob must not match Checkout Conversion:\n%s", out)
	}
}

func TestExportGlobCrossesSlashInDisplayName(t *testing.T) {
	fb := exportFixtureBackend()
	fb.listMetrics = append(fb.listMetrics, confidence.Metric{
		Name: "metrics/m4", DisplayName: "Revenue / User - Day 7",
		Entity: "entities/u1", Measurement: "measurements/ms1", State: "ACTIVE",
	})
	startFake(t, fb)

	// path.Match would stop '*' at the '/', silently missing this metric
	// (review finding on PR #4).
	out, err := run(t, "export", "revenue*")
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Revenue / User - Day 7") {
		t.Errorf("glob must treat '/' as an ordinary character:\n%s", out)
	}
}

func TestExportWithDependencies(t *testing.T) {
	startFake(t, exportFixtureBackend())

	// Pattern matches only the metric — its measurement ("Hourly Minutes
	// Played") and that measurement's fact table ("Hourly Stream") must be
	// pulled in anyway, proving dependencies bypass the patterns.
	out, err := run(t, "export", "*conversion*", "--with-dependencies")
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, out)
	}
	for _, s := range []string{
		"Checkout Conversion - Day 7",
		"measurements:", "display_name: Hourly Minutes Played",
		"fact_tables:", "display_name: Hourly Stream",
		"(1 as dependencies)",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q:\n%s", s, out)
		}
	}
}

func TestExportRejectsMalformedPattern(t *testing.T) {
	startFake(t, exportFixtureBackend())

	// An unterminated character class used to silently match nothing.
	_, err := run(t, "export", "[conversion")
	if err == nil || !strings.Contains(err.Error(), "invalid pattern") {
		t.Fatalf("expected invalid-pattern error, got %v", err)
	}
}

func TestExportNothingMatched(t *testing.T) {
	startFake(t, exportFixtureBackend())

	out, err := run(t, "export", "no-such-metric")
	if err == nil || !strings.Contains(err.Error(), "no metrics matched") {
		t.Fatalf("expected no-metrics-matched error, got %v\n%s", err, out)
	}
}

func TestExportRoundTripsThroughFile(t *testing.T) {
	startFake(t, exportFixtureBackend())

	out, err := run(t, "export", "--with-dependencies", "--out", t.TempDir()+"/x.yaml")
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, out)
	}
}
