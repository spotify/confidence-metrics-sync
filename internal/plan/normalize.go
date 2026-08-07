// Package plan computes what a sync would do: it normalizes parsed YAML into
// a desired state, fetches the actual state from Confidence, and diffs the
// two into create/update/delete/unchanged buckets with guardrail findings.
package plan

import (
	"fmt"

	"github.com/spotify/confidence-metrics-sync/internal/parser"
	"github.com/spotify/confidence-metrics-sync/internal/report"
	"github.com/spotify/confidence-metrics-sync/internal/schema"
)

// Desired is the normalized definition set across all files. Both metric
// flavors (nested and flat) are folded into one metric list.
type Desired struct {
	FactTables   []DesiredFactTable
	Measurements []DesiredMeasurement
	Metrics      []DesiredMetric
}

// Loc points a desired object back at its YAML source for diagnostics.
type Loc struct {
	File    *parser.File
	Pointer string
}

// DesiredFactTable is a fact table with its SQL resolved (generated for
// table-type sources).
type DesiredFactTable struct {
	Def schema.FactTable
	SQL string // resolved: Def.SQL, or generated from Def.Table
	Loc Loc
}

// DesiredMeasurement is a measurement definition.
type DesiredMeasurement struct {
	Def schema.Measurement
	Loc Loc
}

// DesiredMetric is a metric with measurement/entity resolved regardless of
// authoring flavor.
type DesiredMetric struct {
	DisplayName        string
	Entity             string
	Measurement        string // measurement display name
	Description        string
	Owner              string
	PreferredDirection string
	DefaultEffectSize  *float64
	Window             *schema.MeasurementWindow
	Filters            []schema.Filter
	Labels             map[string]string
	VarianceReduction  *schema.VarianceReduction
	Loc                Loc
}

// Normalize folds parsed files into a Desired set. It assumes files already
// passed validation (references resolve, no duplicates).
func Normalize(files []*parser.File) Desired {
	var d Desired
	for _, f := range files {
		for i, ft := range f.Def.FactTables {
			d.FactTables = append(d.FactTables, DesiredFactTable{
				Def: ft,
				SQL: resolveSQL(ft),
				Loc: Loc{f, fmt.Sprintf("/fact_tables/%d", i)},
			})
		}
		for i, m := range f.Def.Measurements {
			d.Measurements = append(d.Measurements, DesiredMeasurement{
				Def: m,
				Loc: Loc{f, fmt.Sprintf("/measurements/%d", i)},
			})
			for j, mt := range m.Metrics {
				d.Metrics = append(d.Metrics, DesiredMetric{
					DisplayName:        mt.DisplayName,
					Entity:             m.Entity,      // inherited
					Measurement:        m.DisplayName, // inherited
					Description:        string(mt.Description),
					Owner:              mt.Owner,
					PreferredDirection: mt.PreferredDirection,
					DefaultEffectSize:  mt.DefaultEffectSize,
					Window:             mt.MeasurementWindow,
					Filters:            mt.Filters,
					Labels:             mt.Labels,
					VarianceReduction:  mt.VarianceReduction,
					Loc:                Loc{f, fmt.Sprintf("/measurements/%d/metrics/%d", i, j)},
				})
			}
		}
		for i, mt := range f.Def.Metrics {
			d.Metrics = append(d.Metrics, DesiredMetric{
				DisplayName:        mt.DisplayName,
				Entity:             mt.Entity,
				Measurement:        mt.Measurement,
				Description:        string(mt.Description),
				Owner:              mt.Owner,
				PreferredDirection: mt.PreferredDirection,
				DefaultEffectSize:  mt.DefaultEffectSize,
				Window:             mt.MeasurementWindow,
				Filters:            mt.Filters,
				Labels:             mt.Labels,
				VarianceReduction:  mt.VarianceReduction,
				Loc:                Loc{f, fmt.Sprintf("/metrics/%d", i)},
			})
		}
	}
	return d
}

// resolveSQL returns the fact table's SQL, generating a SELECT for
// table-type sources. Measures are aliased to their YAML display name so the wire
// Column reference is deterministic; entity/timestamp/dimension columns are
// selected as-is.
func resolveSQL(ft schema.FactTable) string {
	if ft.SQL != "" {
		return string(ft.SQL)
	}
	cols := make([]string, 0, 2+len(ft.Entities)+len(ft.Measures)+len(ft.Dimensions))
	for _, e := range ft.Entities {
		cols = append(cols, e.Column)
	}
	cols = append(cols, ft.TimestampColumn)
	for _, m := range ft.Measures {
		if m.Column == m.DisplayName {
			cols = append(cols, m.Column)
		} else {
			cols = append(cols, fmt.Sprintf("(%s) AS %s", m.Column, m.DisplayName))
		}
	}
	for _, d := range ft.Dimensions {
		cols = append(cols, d.Column)
	}
	sql := "SELECT "
	for i, c := range cols {
		if i > 0 {
			sql += ", "
		}
		sql += c
	}
	return sql + " FROM " + ft.Table
}

// measureColumnName is the query-output column a measure maps to: for
// table-type sources measures are aliased to their display name; for sql-type the
// declared column is expected in the query output. Unnamed measures
// always use the column directly.
func measureColumnName(ft schema.FactTable, m schema.Measure) string {
	if m.DisplayName != "" && ft.Table != "" && m.Column != m.DisplayName {
		return m.DisplayName
	}
	return m.Column
}

func (l Loc) diag(severity report.Severity, rule, msg string) report.Diagnostic {
	pos := l.File.Position(l.Pointer)
	return report.Diagnostic{
		File: l.File.Path, Line: pos.Line, Col: pos.Col,
		Severity: severity, Rule: rule, Message: msg,
	}
}
