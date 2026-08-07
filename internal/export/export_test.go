package export

import (
	"strings"
	"testing"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
)

func strp(s string) *string { return &s }

func fixtureWires() ([]confidence.Entity, []confidence.FactTable, []confidence.Measurement) {
	entities := []confidence.Entity{{Name: "entities/u1", DisplayName: "user"}}
	factTables := []confidence.FactTable{{
		Name: "factTables/ft1", DisplayName: "Events", SQL: "SELECT 1",
		State:           "TABLE_STATE_ACTIVE",
		TimestampColumn: &confidence.Column{Name: "t"},
		Entities: []confidence.EntityColumnMapping{
			{Column: &confidence.Column{Name: "user_id"}, Entity: "entities/u1"},
		},
		Measures: []confidence.Measure{
			{Column: &confidence.Column{Name: "started"}, DisplayName: "started"},
			{Column: &confidence.Column{Name: "completed"}, DisplayName: "completed"},
		},
	}}
	measurement := confidence.Measurement{
		Name: "measurements/ms1", DisplayName: "Conversion",
		Entity: "entities/u1", FactTable: "factTables/ft1",
	}
	return entities, factTables, []confidence.Measurement{measurement}
}

// A denominator aggregation threshold has no schema home (the single
// aggregation_threshold maps to the numerator) — exporting it silently
// would produce YAML that syncs as an UPDATE removing the threshold.
func TestDenominatorThresholdIsNotExpressible(t *testing.T) {
	entities, factTables, measurements := fixtureWires()
	measurements[0].TypeSpec = &confidence.TypeSpec{RatioMetricSpec: &confidence.RatioMetricSpec{
		Numerator:            &confidence.Column{Name: "completed"},
		NumeratorAggregation: &confidence.Aggregation{Type: "AGGREGATION_TYPE_SUM"},
		Denominator:          &confidence.Column{Name: "started"},
		DenominatorAggregation: &confidence.Aggregation{
			Type: "AGGREGATION_TYPE_SUM",
			Threshold: &confidence.AggregationThreshold{
				Threshold: &confidence.Decimal{Value: "1"},
				Direction: "AGGREGATION_THRESHOLD_DIRECTION_GT",
			},
		},
	}}

	file, warnings, stats, err := Build(entities, factTables, measurements, nil,
		Options{Measurements: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(file.Measurements) != 0 || stats.Skipped["measurement"] != 1 {
		t.Fatalf("measurement with denominator threshold must be skipped, got %+v", stats)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "denominator has an aggregation threshold") {
		t.Fatalf("expected a denominator-threshold warning, got %v", warnings)
	}
}

// Non-string filter values (bool/number/timestamp/null) have no schema
// representation; exporting them as "" would silently change semantics.
func TestNonStringFilterValueIsNotExpressible(t *testing.T) {
	entities, factTables, measurements := fixtureWires()
	b := true
	measurements[0].TypeSpec = &confidence.TypeSpec{AverageMetricSpec: &confidence.AverageMetricSpec{
		Measurement: &confidence.Column{Name: "completed"},
		Aggregation: &confidence.Aggregation{Type: "AGGREGATION_TYPE_SUM"},
	}}
	measurements[0].Filter = &confidence.Filter{
		Criteria: map[string]confidence.FilterCriterion{
			"c0": {Attribute: &confidence.AttributeCriterion{
				Attribute: "is_premium",
				EqRule:    &confidence.EqRule{Value: confidence.FilterValue{BoolValue: &b}},
			}},
		},
		Expression: &confidence.Expression{Ref: "c0"},
	}

	file, warnings, stats, err := Build(entities, factTables, measurements, nil,
		Options{Measurements: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(file.Measurements) != 0 || stats.Skipped["measurement"] != 1 {
		t.Fatalf("measurement with bool filter value must be skipped, got %+v", stats)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "non-string filter value") {
		t.Fatalf("expected a non-string-filter warning, got %v", warnings)
	}

	// String values still export.
	measurements[0].Filter.Criteria["c0"] = confidence.FilterCriterion{
		Attribute: &confidence.AttributeCriterion{
			Attribute: "platform",
			EqRule:    &confidence.EqRule{Value: confidence.FilterValue{StringValue: strp("ios")}},
		},
	}
	file, _, _, err = Build(entities, factTables, measurements, nil, Options{Measurements: true})
	if err != nil || len(file.Measurements) != 1 {
		t.Fatalf("string filter value must export: %v", err)
	}
}

func TestValueCapOnAverageExports(t *testing.T) {
	entities, factTables, measurements := fixtureWires()
	measurements[0].TypeSpec = &confidence.TypeSpec{AverageMetricSpec: &confidence.AverageMetricSpec{
		Measurement: &confidence.Column{Name: "completed"},
		Aggregation: &confidence.Aggregation{
			Type: "AGGREGATION_TYPE_SUM",
			Cap:  &confidence.ValueCap{Max: &confidence.Decimal{Value: "100"}},
		},
	}}

	file, warnings, stats, err := Build(entities, factTables, measurements, nil,
		Options{Measurements: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if stats.Exported["measurement"] != 1 {
		t.Fatalf("capped measurement must be exported, got %+v", stats)
	}
	if file.Measurements[0].Cap == nil || file.Measurements[0].Cap.Max == nil || *file.Measurements[0].Cap.Max != 100 {
		t.Fatalf("expected cap max=100, got %+v", file.Measurements[0].Cap)
	}
}

func TestValueCapOnRatioExports(t *testing.T) {
	entities, factTables, measurements := fixtureWires()
	measurements[0].TypeSpec = &confidence.TypeSpec{RatioMetricSpec: &confidence.RatioMetricSpec{
		Numerator:            &confidence.Column{Name: "completed"},
		NumeratorAggregation: &confidence.Aggregation{Type: "AGGREGATION_TYPE_SUM"},
		Denominator:          &confidence.Column{Name: "started"},
		DenominatorAggregation: &confidence.Aggregation{
			Type: "AGGREGATION_TYPE_SUM",
			Cap:  &confidence.ValueCap{Min: &confidence.Decimal{Value: "0"}},
		},
	}}

	file, warnings, stats, err := Build(entities, factTables, measurements, nil,
		Options{Measurements: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if stats.Exported["measurement"] != 1 {
		t.Fatalf("capped ratio must be exported, got %+v", stats)
	}
	if file.Measurements[0].Denominator == nil || file.Measurements[0].Denominator.Cap == nil || file.Measurements[0].Denominator.Cap.Min == nil || *file.Measurements[0].Denominator.Cap.Min != 0 {
		t.Fatalf("expected denominator cap min=0, got %+v", file.Measurements[0].Denominator)
	}
}

func TestDistinctMetricFilterExports(t *testing.T) {
	entities, factTables, measurements := fixtureWires()
	measurements[0].TypeSpec = &confidence.TypeSpec{AverageMetricSpec: &confidence.AverageMetricSpec{
		Measurement: &confidence.Column{Name: "completed"},
		Aggregation: &confidence.Aggregation{Type: "AGGREGATION_TYPE_SUM"},
	}}
	metrics := []confidence.Metric{{
		Name: "metrics/m1", DisplayName: "Filtered",
		Entity: "entities/u1", Measurement: "measurements/ms1",
		Filter: &confidence.Filter{
			Criteria: map[string]confidence.FilterCriterion{
				"c0": {Attribute: &confidence.AttributeCriterion{
					Attribute: "platform", EqRule: &confidence.EqRule{
						Value: confidence.FilterValue{StringValue: strp("ios")},
					},
				}},
			},
			Expression: &confidence.Expression{Ref: "c0"},
		},
	}}

	file, warnings, stats, err := Build(entities, factTables, measurements, metrics,
		Options{Metrics: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if stats.Exported["metric"] != 1 {
		t.Fatalf("metric with distinct filter must be exported, got %+v", stats)
	}
	if len(file.Metrics) != 1 || len(file.Metrics[0].Filters) != 1 {
		t.Fatalf("expected 1 metric with 1 filter, got %d metrics", len(file.Metrics))
	}
	f := file.Metrics[0].Filters[0]
	if f.Dimension != "platform" || f.Operation != "equals" || len(f.Values) != 1 || f.Values[0] != "ios" {
		t.Fatalf("unexpected filter: %+v", f)
	}
}

func TestMaterializedFilterIsNotRefused(t *testing.T) {
	entities, factTables, measurements := fixtureWires()
	sharedFilter := &confidence.Filter{
		Criteria: map[string]confidence.FilterCriterion{
			"c0": {Attribute: &confidence.AttributeCriterion{
				Attribute: "platform", EqRule: &confidence.EqRule{
					Value: confidence.FilterValue{StringValue: strp("ios")},
				},
			}},
		},
		Expression: &confidence.Expression{Ref: "c0"},
	}
	measurements[0].TypeSpec = &confidence.TypeSpec{AverageMetricSpec: &confidence.AverageMetricSpec{
		Measurement: &confidence.Column{Name: "completed"},
		Aggregation: &confidence.Aggregation{Type: "AGGREGATION_TYPE_SUM"},
	}}
	measurements[0].Filter = sharedFilter
	metrics := []confidence.Metric{{
		Name: "metrics/m1", DisplayName: "Materialized Filter",
		Entity: "entities/u1", Measurement: "measurements/ms1",
		Filter: sharedFilter,
		MeasurementConfig: &confidence.MeasurementConfig{
			ClosedWindow: &confidence.WindowConfig{AggregationWindow: "86400s"},
		},
	}}

	file, warnings, _, err := Build(entities, factTables, measurements, metrics,
		Options{Metrics: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(file.Metrics) != 1 {
		t.Fatalf("metric with materialized filter must export, got %d metrics, warnings: %v", len(file.Metrics), warnings)
	}
}

func TestLikeFilterExports(t *testing.T) {
	entities, factTables, measurements := fixtureWires()
	measurements[0].TypeSpec = &confidence.TypeSpec{AverageMetricSpec: &confidence.AverageMetricSpec{
		Measurement: &confidence.Column{Name: "completed"},
		Aggregation: &confidence.Aggregation{Type: "AGGREGATION_TYPE_SUM"},
	}}
	measurements[0].Filter = &confidence.Filter{
		Criteria: map[string]confidence.FilterCriterion{
			"c0": {Attribute: &confidence.AttributeCriterion{
				Attribute: "platform",
				LikeRule:  &confidence.LikeRule{Pattern: "ios%"},
			}},
		},
		Expression: &confidence.Expression{Ref: "c0"},
	}

	file, warnings, _, err := Build(entities, factTables, measurements, nil,
		Options{Measurements: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(file.Measurements) != 1 || len(file.Measurements[0].Filters) != 1 {
		t.Fatalf("expected 1 measurement with 1 filter, got %d", len(file.Measurements))
	}
	f := file.Measurements[0].Filters[0]
	if f.Dimension != "platform" || f.Operation != "like" || f.Values[0] != "ios%" {
		t.Fatalf("unexpected filter: %+v", f)
	}
}

func TestRangeFilterExports(t *testing.T) {
	entities, factTables, measurements := fixtureWires()
	measurements[0].TypeSpec = &confidence.TypeSpec{AverageMetricSpec: &confidence.AverageMetricSpec{
		Measurement: &confidence.Column{Name: "completed"},
		Aggregation: &confidence.Aggregation{Type: "AGGREGATION_TYPE_SUM"},
	}}

	// Test gt
	measurements[0].Filter = &confidence.Filter{
		Criteria: map[string]confidence.FilterCriterion{
			"c0": {Attribute: &confidence.AttributeCriterion{
				Attribute: "age",
				RangeRule: &confidence.RangeRule{StartExclusive: &confidence.FilterValue{StringValue: strp("18")}},
			}},
		},
		Expression: &confidence.Expression{Ref: "c0"},
	}
	file, warnings, _, err := Build(entities, factTables, measurements, nil, Options{Measurements: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	f := file.Measurements[0].Filters[0]
	if f.Operation != "gt" || f.Values[0] != "18" {
		t.Fatalf("gt filter mismatch: %+v", f)
	}

	// Test between
	measurements[0].Filter = &confidence.Filter{
		Criteria: map[string]confidence.FilterCriterion{
			"c0": {Attribute: &confidence.AttributeCriterion{
				Attribute: "age",
				RangeRule: &confidence.RangeRule{
					StartInclusive: &confidence.FilterValue{StringValue: strp("18")},
					EndInclusive:   &confidence.FilterValue{StringValue: strp("65")},
				},
			}},
		},
		Expression: &confidence.Expression{Ref: "c0"},
	}
	file, warnings, _, err = Build(entities, factTables, measurements, nil, Options{Measurements: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	f = file.Measurements[0].Filters[0]
	if f.Operation != "between" || f.Values[0] != "18" || f.Values[1] != "65" {
		t.Fatalf("between filter mismatch: %+v", f)
	}
}

func TestNegatedRangeFilterIsNotExpressible(t *testing.T) {
	entities, factTables, measurements := fixtureWires()
	measurements[0].TypeSpec = &confidence.TypeSpec{AverageMetricSpec: &confidence.AverageMetricSpec{
		Measurement: &confidence.Column{Name: "completed"},
		Aggregation: &confidence.Aggregation{Type: "AGGREGATION_TYPE_SUM"},
	}}
	measurements[0].Filter = &confidence.Filter{
		Criteria: map[string]confidence.FilterCriterion{
			"c0": {Attribute: &confidence.AttributeCriterion{
				Attribute: "age",
				RangeRule: &confidence.RangeRule{StartExclusive: &confidence.FilterValue{StringValue: strp("18")}},
			}},
		},
		Expression: &confidence.Expression{Not: &confidence.Expression{Ref: "c0"}},
	}

	_, warnings, stats, err := Build(entities, factTables, measurements, nil,
		Options{Measurements: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if stats.Skipped["measurement"] != 1 {
		t.Fatalf("negated range must be skipped, got %+v", stats)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "negated range filter") {
		t.Fatalf("expected negated-range warning, got %v", warnings)
	}
}

func TestUnknownBaseUnitIsNotExpressible(t *testing.T) {
	entities, factTables, _ := fixtureWires()
	factTables[0].Measures[0].Unit = &confidence.Unit{BaseUnit: "UNKNOWN_UNIT"}

	_, warnings, stats, err := Build(entities, factTables, nil, nil,
		Options{FactTables: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if stats.Skipped["fact table"] != 1 {
		t.Fatalf("fact table with unknown unit must be skipped, got %+v", stats)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown base unit") {
		t.Fatalf("expected unknown-unit warning, got %v", warnings)
	}
}

func TestUnknownDeclaredTypeIsNotExpressible(t *testing.T) {
	entities, factTables, _ := fixtureWires()
	factTables[0].Measures[0].DeclaredType = "COLUMN_TYPE_FUTURE"

	_, warnings, stats, err := Build(entities, factTables, nil, nil,
		Options{FactTables: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if stats.Skipped["fact table"] != 1 {
		t.Fatalf("fact table with unknown declared type must be skipped, got %+v", stats)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown declared type") {
		t.Fatalf("expected unknown-declared-type warning, got %v", warnings)
	}
}

func TestUnspecifiedDeclaredTypeExportsWithoutType(t *testing.T) {
	entities, factTables, _ := fixtureWires()
	// The API serializes the enum zero value explicitly; it means "not declared".
	factTables[0].Measures[0].DeclaredType = "COLUMN_TYPE_UNSPECIFIED"

	file, warnings, stats, err := Build(entities, factTables, nil, nil,
		Options{FactTables: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if stats.Skipped["fact table"] != 0 || len(file.FactTables) != len(factTables) {
		t.Fatalf("unspecified declared type must not skip the fact table: %+v, warnings: %v",
			stats, warnings)
	}
	if file.FactTables[0].Measures[0].Type != "" {
		t.Fatalf("unspecified declared type must export as no type, got %q",
			file.FactTables[0].Measures[0].Type)
	}
}

func TestUnspecifiedBaseUnitExportsWithoutUnit(t *testing.T) {
	entities, factTables, _ := fixtureWires()
	factTables[0].Measures[0].Unit = &confidence.Unit{BaseUnit: "BASE_UNIT_UNSPECIFIED"}

	file, warnings, stats, err := Build(entities, factTables, nil, nil,
		Options{FactTables: true})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if stats.Skipped["fact table"] != 0 || len(file.FactTables) != len(factTables) {
		t.Fatalf("unspecified base unit must not skip the fact table: %+v, warnings: %v",
			stats, warnings)
	}
	if file.FactTables[0].Measures[0].Unit != nil {
		t.Fatalf("empty unit must export as no unit, got %+v",
			file.FactTables[0].Measures[0].Unit)
	}
}
