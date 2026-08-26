package validate

import (
	"fmt"

	"github.com/spotify/confidence-metrics-sync/internal/parser"
	"github.com/spotify/confidence-metrics-sync/internal/report"
	"github.com/spotify/confidence-metrics-sync/internal/schema"
)

// located remembers where a named definition came from.
type located struct {
	file    *parser.File
	pointer string
}

// semantic runs cross-reference checks across ALL files. Resource names are
// identity; display names remain unique as a separate authoring rule.
// Definitions are expected to be self-contained: relationships must resolve
// to resources declared somewhere in the repository snapshot.
func semantic(files []*parser.File) []report.Diagnostic {
	var diags []report.Diagnostic

	factTables := map[string]located{}
	factTableDefs := map[string]schema.FactTable{}
	factTableDisplayNames := map[string]located{}
	measurements := map[string]located{}
	measurementDefs := map[string]schema.Measurement{}
	measurementDisplayNames := map[string]located{}
	metrics := map[string]located{}
	metricDisplayNames := map[string]located{}

	// Pass 1: collect names, flag duplicates.
	for _, f := range files {
		for i, ft := range f.Def.FactTables {
			diags = append(diags, checkColumnDuplicates(f, i, ft)...)
			namePtr := fmt.Sprintf("/fact_tables/%d/name", i)
			if prev, dup := factTables[ft.Name]; ft.Name != "" && dup {
				diags = append(diags, dupDiag(f, namePtr, "fact table", ft.Name, prev))
			} else if ft.Name != "" {
				factTables[ft.Name] = located{f, namePtr}
				factTableDefs[ft.Name] = ft
			}
			displayPtr := fmt.Sprintf("/fact_tables/%d/display_name", i)
			if prev, dup := factTableDisplayNames[ft.DisplayName]; dup {
				diags = append(diags, dupDisplayNameDiag(f, displayPtr, "fact table", ft.DisplayName, prev))
			} else {
				factTableDisplayNames[ft.DisplayName] = located{f, displayPtr}
			}
		}
		for i, m := range f.Def.Measurements {
			namePtr := fmt.Sprintf("/measurements/%d/name", i)
			if prev, dup := measurements[m.Name]; m.Name != "" && dup {
				diags = append(diags, dupDiag(f, namePtr, "measurement", m.Name, prev))
			} else if m.Name != "" {
				measurements[m.Name] = located{f, namePtr}
				measurementDefs[m.Name] = m
			}
			displayPtr := fmt.Sprintf("/measurements/%d/display_name", i)
			if prev, dup := measurementDisplayNames[m.DisplayName]; dup {
				diags = append(diags, dupDisplayNameDiag(f, displayPtr, "measurement", m.DisplayName, prev))
			} else {
				measurementDisplayNames[m.DisplayName] = located{f, displayPtr}
			}
		}
		eachMetric(f, func(ptr string, mt schema.Metric, _ *schema.Measurement) {
			if prev, dup := metrics[mt.Name]; mt.Name != "" && dup {
				diags = append(diags, dupDiag(f, ptr+"/name", "metric", mt.Name, prev))
			} else if mt.Name != "" {
				metrics[mt.Name] = located{f, ptr + "/name"}
			}
			if prev, dup := metricDisplayNames[mt.DisplayName]; dup {
				diags = append(diags, dupDisplayNameDiag(f, ptr+"/display_name", "metric", mt.DisplayName, prev))
			} else {
				metricDisplayNames[mt.DisplayName] = located{f, ptr + "/display_name"}
			}
		})
	}

	// Pass 2: resolve references.
	for _, f := range files {
		for i, m := range f.Def.Measurements {
			ptr := fmt.Sprintf("/measurements/%d", i)
			ft, ok := factTableDefs[m.FactTable]
			if m.FactTable != "" && !ok {
				diags = append(diags, diag(f, ptr+"/fact_table", "unknown-fact-table",
					fmt.Sprintf("measurement %q references fact table %q, which is not defined in this repository", m.DisplayName, m.FactTable)))
			}

			if ok && m.Entity != "" && !hasEntity(ft, m.Entity) {
				diags = append(diags, diag(f, ptr+"/entity", "unknown-entity",
					fmt.Sprintf("measurement %q uses entity %q, but fact table %q maps entities %v", m.DisplayName, m.Entity, ft.DisplayName, entityNames(ft))))
			}

			if m.Measure != "" && ok && !hasMeasure(ft, m.Measure) {
				diags = append(diags, diag(f, ptr+"/measure", "unknown-measure",
					fmt.Sprintf("measurement %q aggregates measure %q, which fact table %q does not define (measures: %v)", m.DisplayName, m.Measure, ft.DisplayName, measureNames(ft))))
			}
			for _, side := range []struct {
				name string
				spec *schema.AggregationSpec
			}{{"numerator", m.Numerator}, {"denominator", m.Denominator}} {
				if side.spec == nil {
					continue
				}
				if ok && !hasMeasure(ft, side.spec.Measure) {
					diags = append(diags, diag(f, ptr+"/"+side.name+"/measure", "unknown-measure",
						fmt.Sprintf("measurement %q %s aggregates measure %q, which fact table %q does not define (measures: %v)", m.DisplayName, side.name, side.spec.Measure, ft.DisplayName, measureNames(ft))))
				}
				diags = append(diags, checkFilters(f, ptr+"/"+side.name, m.DisplayName, side.spec.Filters, ft, ok)...)
			}
			diags = append(diags, checkFilters(f, ptr, m.DisplayName, m.Filters, ft, ok)...)
		}

		eachMetric(f, func(ptr string, mt schema.Metric, parent *schema.Measurement) {
			if parent != nil {
				return // nested metrics inherit measurement and entity
			}
			ms, ok := measurementDefs[mt.Measurement]
			if mt.Measurement != "" && !ok {
				diags = append(diags, diag(f, ptr+"/measurement", "unknown-measurement",
					fmt.Sprintf("metric %q references measurement %q, which is not defined in this repository", mt.DisplayName, mt.Measurement)))
				return
			}
			if ok && mt.Entity != "" && ms.Entity != "" && mt.Entity != ms.Entity {
				diags = append(diags, diag(f, ptr+"/entity", "entity-mismatch",
					fmt.Sprintf("metric %q is for entity %q but measurement %q is computed for entity %q", mt.DisplayName, mt.Entity, ms.DisplayName, ms.Entity)))
			}
		})
	}

	return diags
}

// checkFilters verifies filter dimensions exist on the fact table.
func checkFilters(f *parser.File, basePtr, owner string, filters []schema.Filter, ft schema.FactTable, ftKnown bool) []report.Diagnostic {
	if !ftKnown {
		return nil
	}
	var diags []report.Diagnostic
	for i, fl := range filters {
		if fl.Dimension != "" && !hasDimension(ft, fl.Dimension) {
			diags = append(diags, diag(f, fmt.Sprintf("%s/filters/%d/dimension", basePtr, i), "unknown-dimension",
				fmt.Sprintf("measurement %q filters on dimension %q, which fact table %q does not define (dimensions: %v)", owner, fl.Dimension, ft.DisplayName, dimensionNames(ft))))
		}
	}
	return diags
}

// eachMetric visits every metric in a file — nested (with its parent
// measurement) and flat (parent nil) — with its JSON pointer.
func eachMetric(f *parser.File, visit func(pointer string, m schema.Metric, parent *schema.Measurement)) {
	for i := range f.Def.Measurements {
		ms := &f.Def.Measurements[i]
		for j, mt := range ms.Metrics {
			visit(fmt.Sprintf("/measurements/%d/metrics/%d", i, j), mt, ms)
		}
	}
	for i, mt := range f.Def.Metrics {
		visit(fmt.Sprintf("/metrics/%d", i), mt, nil)
	}
}

// checkColumnDuplicates flags measure and dimension names defined more than
// once within a single fact table — a measurement referencing the duplicated
// name would resolve ambiguously.
func checkColumnDuplicates(f *parser.File, ftIndex int, ft schema.FactTable) []report.Diagnostic {
	var diags []report.Diagnostic
	seenMeasures := map[string]bool{}
	namedMeasures := map[string]bool{}  // names of named measures
	unnamedColumns := map[string]bool{} // columns of unnamed measures
	for j, m := range ft.Measures {
		ref := m.DisplayName
		if ref == "" {
			ref = m.Column
		}
		if seenMeasures[ref] {
			ptr := fmt.Sprintf("/fact_tables/%d/measures/%d/column", ftIndex, j)
			if m.DisplayName != "" {
				ptr = fmt.Sprintf("/fact_tables/%d/measures/%d/display_name", ftIndex, j)
			}
			diags = append(diags, diag(f, ptr, "duplicate-name",
				fmt.Sprintf("fact table %q defines measure %q more than once", ft.DisplayName, ref)))
		}
		seenMeasures[ref] = true
		if m.DisplayName != "" {
			namedMeasures[m.DisplayName] = true
		} else {
			unnamedColumns[m.Column] = true
		}
	}
	for j, m := range ft.Measures {
		if m.DisplayName != "" && unnamedColumns[m.DisplayName] {
			diags = append(diags, diag(f, fmt.Sprintf("/fact_tables/%d/measures/%d/display_name", ftIndex, j), "ambiguous-measure",
				fmt.Sprintf("fact table %q: named measure %q shadows an unnamed measure with column %q — references are ambiguous", ft.DisplayName, m.DisplayName, m.DisplayName)))
		}
		if m.DisplayName == "" && namedMeasures[m.Column] {
			diags = append(diags, diag(f, fmt.Sprintf("/fact_tables/%d/measures/%d/column", ftIndex, j), "ambiguous-measure",
				fmt.Sprintf("fact table %q: unnamed measure with column %q is shadowed by a named measure %q — references are ambiguous", ft.DisplayName, m.Column, m.Column)))
		}
	}
	seenDimensions := map[string]bool{}
	for j, d := range ft.Dimensions {
		if seenDimensions[d.Name] {
			diags = append(diags, diag(f, fmt.Sprintf("/fact_tables/%d/dimensions/%d/name", ftIndex, j), "duplicate-name",
				fmt.Sprintf("fact table %q defines dimension %q more than once", ft.DisplayName, d.Name)))
		}
		seenDimensions[d.Name] = true
	}
	return diags
}

func dupDiag(f *parser.File, ptr, kind, name string, prev located) report.Diagnostic {
	prevPos := prev.file.Position(prev.pointer)
	return diag(f, ptr, "duplicate-name",
		fmt.Sprintf("duplicate %s name %q — already defined at %s:%d", kind, name, prev.file.Path, prevPos.Line))
}

func dupDisplayNameDiag(f *parser.File, ptr, kind, name string, prev located) report.Diagnostic {
	prevPos := prev.file.Position(prev.pointer)
	return diag(f, ptr, "duplicate-display-name",
		fmt.Sprintf("duplicate %s display name %q — already defined at %s:%d", kind, name, prev.file.Path, prevPos.Line))
}

func hasEntity(ft schema.FactTable, entity string) bool {
	for _, e := range ft.Entities {
		if e.Entity == entity {
			return true
		}
	}
	return false
}

func hasMeasure(ft schema.FactTable, ref string) bool {
	for _, m := range ft.Measures {
		if m.DisplayName != "" && m.DisplayName == ref {
			return true
		}
		if m.DisplayName == "" && m.Column == ref {
			return true
		}
	}
	return false
}

func hasDimension(ft schema.FactTable, name string) bool {
	for _, d := range ft.Dimensions {
		if d.Name == name {
			return true
		}
	}
	return false
}

func entityNames(ft schema.FactTable) []string {
	names := make([]string, 0, len(ft.Entities))
	for _, e := range ft.Entities {
		names = append(names, e.Entity)
	}
	return names
}

func measureNames(ft schema.FactTable) []string {
	names := make([]string, 0, len(ft.Measures))
	for _, m := range ft.Measures {
		if m.DisplayName != "" {
			names = append(names, m.DisplayName)
		} else {
			names = append(names, m.Column)
		}
	}
	return names
}

func dimensionNames(ft schema.FactTable) []string {
	names := make([]string, 0, len(ft.Dimensions))
	for _, d := range ft.Dimensions {
		names = append(names, d.Name)
	}
	return names
}
