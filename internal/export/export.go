// Package export reverse-maps live Confidence resources into the canonical
// YAML schema. The invariant that keeps it honest: syncing freshly exported
// YAML must plan as a no-op (for resources owned by the exporting
// reference).
package export

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/schema"
)

// Options selects what to export. The CLI always exports metrics (selected
// by Patterns) plus dependencies when asked; the per-kind booleans remain
// for internal use (the round-trip test exports everything standalone).
type Options struct {
	Metrics      bool
	Measurements bool
	FactTables   bool
	Patterns     []string // display-name patterns, any-of; empty = all
	Reference    string   // only resources owned by this source.reference; "" = all

	// WithDependencies also exports what the selection references: an
	// exported metric pulls its measurement, an exported measurement pulls
	// its fact table. Dependencies bypass Patterns/Reference and type flags.
	WithDependencies bool
}

// Types reports whether any type selector is set; when none are, all types
// are exported.
func (o Options) normalized() Options {
	if !o.Metrics && !o.Measurements && !o.FactTables {
		o.Metrics, o.Measurements, o.FactTables = true, true, true
	}
	return o
}

// Stats describes what Build saw and why things were or weren't emitted —
// printed by the command so "nothing matched" is diagnosable.
type Stats struct {
	Fetched     map[string]int // per kind: total from the API
	NotAlive    map[string]int // archived / deleted / system-created
	FilteredOut map[string]int // didn't match --filter / --source-reference
	Skipped     map[string]int // matched but not expressible (see warnings)
	Exported    map[string]int
	Deps        map[string]int // exported only because something exported references them
}

func newStats() *Stats {
	m := func() map[string]int { return map[string]int{} }
	return &Stats{Fetched: m(), NotAlive: m(), FilteredOut: m(), Skipped: m(), Exported: m(), Deps: m()}
}

// Build converts fetched resources into a schema.File. Inputs are the FULL
// account listings — indexes for reference resolution are built from
// everything, selection applies only to what is emitted. Returns the file,
// human-readable warnings for skipped resources, and selection stats.
func Build(
	entities []confidence.Entity,
	factTables []confidence.FactTable,
	measurements []confidence.Measurement,
	metrics []confidence.Metric,
	opts Options,
) (*schema.File, []string, *Stats, error) {
	opts = opts.normalized()
	var warnings []string
	stats := newStats()

	entityDisplay := map[string]string{} // "entities/x" -> "user"
	for _, e := range entities {
		entityDisplay[e.Name] = e.DisplayName
	}
	factTableByName := map[string]confidence.FactTable{} // resource name -> ft
	for _, ft := range factTables {
		factTableByName[ft.Name] = ft
	}
	measurementByName := map[string]confidence.Measurement{}
	for _, m := range measurements {
		measurementByName[m.Name] = m
	}

	file := &schema.File{}

	// Passes run metrics -> measurements -> fact tables: each pass records
	// the resource names its exports reference, so WithDependencies pulls in
	// exactly what the file needs — no more (deps of skipped resources), no
	// less. Dependencies bypass Filter/Reference and the type flags.
	depMeasurements := map[string]bool{} // referenced by exported metrics
	depFactTables := map[string]bool{}   // referenced by exported measurements

	if opts.Metrics {
		for _, m := range metrics {
			stats.Fetched["metric"]++
			if m.DeleteTime != "" || m.State == "ARCHIVED" || m.State == "DELETED" || m.SystemCreated {
				stats.NotAlive["metric"]++
				continue
			}
			if !selected(m.DisplayName, m.Source, opts) {
				stats.FilteredOut["metric"]++
				continue
			}
			out, err := exportMetric(m, entityDisplay, measurementByName)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("metric %q skipped: %v", m.DisplayName, err))
				stats.Skipped["metric"]++
				continue
			}
			stats.Exported["metric"]++
			file.Metrics = append(file.Metrics, *out)
			if opts.WithDependencies {
				depMeasurements[m.Measurement] = true
			}
		}
		sort.Slice(file.Metrics, func(i, j int) bool { return file.Metrics[i].DisplayName < file.Metrics[j].DisplayName })
	}

	if opts.Measurements || len(depMeasurements) > 0 {
		for _, m := range measurements {
			stats.Fetched["measurement"]++
			isDep := depMeasurements[m.Name]
			if m.DeleteTime != "" || m.SystemCreated {
				if isDep {
					warnings = append(warnings, fmt.Sprintf(
						"measurement %q is referenced by an exported metric but is deleted or system-created; not exported", m.DisplayName))
				}
				stats.NotAlive["measurement"]++
				continue
			}
			requested := opts.Measurements && selected(m.DisplayName, m.Source, opts)
			if !requested && !isDep {
				stats.FilteredOut["measurement"]++
				continue
			}
			out, err := exportMeasurement(m, entityDisplay, factTableByName)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("measurement %q skipped: %v", m.DisplayName, err))
				stats.Skipped["measurement"]++
				continue
			}
			stats.Exported["measurement"]++
			if !requested {
				stats.Deps["measurement"]++
			}
			file.Measurements = append(file.Measurements, *out)
			if opts.WithDependencies {
				depFactTables[m.FactTable] = true
			}
		}
		sort.Slice(file.Measurements, func(i, j int) bool {
			return file.Measurements[i].DisplayName < file.Measurements[j].DisplayName
		})
	}

	if opts.FactTables || len(depFactTables) > 0 {
		for _, ft := range factTables {
			stats.Fetched["fact table"]++
			isDep := depFactTables[ft.Name]
			if !alive(ft.DeleteTime, ft.State) || ft.SystemCreated {
				if isDep {
					warnings = append(warnings, fmt.Sprintf(
						"fact table %q is referenced by an exported measurement but is deleted or system-created; not exported", ft.DisplayName))
				}
				stats.NotAlive["fact table"]++
				continue
			}
			requested := opts.FactTables && selected(ft.DisplayName, ft.Source, opts)
			if !requested && !isDep {
				stats.FilteredOut["fact table"]++
				continue
			}
			out, err := exportFactTable(ft, entityDisplay)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("fact table %q skipped: %v", ft.DisplayName, err))
				stats.Skipped["fact table"]++
				continue
			}
			stats.Exported["fact table"]++
			if !requested {
				stats.Deps["fact table"]++
			}
			file.FactTables = append(file.FactTables, *out)
		}
		sort.Slice(file.FactTables, func(i, j int) bool {
			return file.FactTables[i].DisplayName < file.FactTables[j].DisplayName
		})
	}

	return file, warnings, stats, nil
}

func alive(deleteTime, state string) bool {
	return deleteTime == "" && state != "TABLE_STATE_DELETED"
}

func selected(displayName string, source *confidence.MetricSource, opts Options) bool {
	if !matchAny(opts.Patterns, displayName) {
		return false
	}
	if opts.Reference != "" {
		if source == nil || source.Reference != opts.Reference {
			return false
		}
	}
	return true
}

// matchAny reports whether the display name matches any pattern. Matching is
// case-insensitive (display names are display text; a case-sensitive match
// is a footgun — found on first real use). A pattern containing glob
// metacharacters matches as a glob; plain text must equal the full display
// name — fuzzier selection is spelled explicitly ("*conversion*").
// Empty = match all.
func matchAny(patterns []string, displayName string) bool {
	if len(patterns) == 0 {
		return true
	}
	name := strings.ToLower(displayName)
	for _, p := range patterns {
		p = strings.ToLower(p)
		if strings.ContainsAny(p, "*?[") {
			if ok, _ := path.Match(flatten(p), flatten(name)); ok {
				return true
			}
		} else if name == p {
			return true
		}
	}
	return false
}

// flatten substitutes a private-use rune for '/' so path.Match treats it as
// an ordinary character: display names are flat text, not paths — "revenue*"
// must match "Revenue / User". Applied to pattern and name alike, so '/'
// still matches itself (including inside character classes).
func flatten(s string) string {
	return strings.ReplaceAll(s, "/", "\uE000")
}

func exportFactTable(ft confidence.FactTable, entityDisplay map[string]string) (*schema.FactTable, error) {
	out := &schema.FactTable{
		DisplayName: ft.DisplayName,
		SQL:         schema.ExactText(ft.SQL),
		Owner:       ft.Owner,
		Description: schema.ExactText(ft.Description),
	}
	if ft.TimestampColumn != nil {
		out.TimestampColumn = ft.TimestampColumn.Name
	}
	for _, e := range ft.Entities {
		display, ok := entityDisplay[e.Entity]
		if !ok {
			return nil, fmt.Errorf("references unknown entity %q", e.Entity)
		}
		col := ""
		if e.Column != nil {
			col = e.Column.Name
		}
		out.Entities = append(out.Entities, schema.EntityMapping{Entity: display, Column: col})
	}
	for _, m := range ft.Measures {
		col := ""
		if m.Column != nil {
			col = m.Column.Name
		}
		sm := schema.Measure{DisplayName: m.DisplayName, Column: col}
		if m.Unit != nil {
			u, err := exportMeasureUnit(m.Unit)
			if err != nil {
				return nil, fmt.Errorf("measure %q: %w", col, err)
			}
			sm.Unit = u
		}
		if m.DeclaredType != "" {
			dt, err := exportDeclaredType(m.DeclaredType)
			if err != nil {
				return nil, fmt.Errorf("measure %q: %w", col, err)
			}
			sm.Type = dt
		}
		out.Measures = append(out.Measures, sm)
	}
	for _, d := range ft.Dimensions {
		out.Dimensions = append(out.Dimensions, schema.Dimension{Name: d.Name, Column: d.Name})
	}
	out.Labels = ft.Labels
	return out, nil
}

func exportMeasurement(m confidence.Measurement, entityDisplay map[string]string, factTableByName map[string]confidence.FactTable) (*schema.Measurement, error) {
	entity, ok := entityDisplay[m.Entity]
	if !ok {
		return nil, fmt.Errorf("references unknown entity %q", m.Entity)
	}
	ft, ok := factTableByName[m.FactTable]
	if !ok {
		return nil, fmt.Errorf("references unknown fact table %q", m.FactTable)
	}
	measureName := measureNameIndex(ft)

	out := &schema.Measurement{
		DisplayName: m.DisplayName,
		FactTable:   ft.DisplayName,
		Entity:      entity,
		Owner:       m.Owner,
		Description: schema.ExactText(m.Description),
	}
	if m.NullHandling != nil {
		out.NullHandling = &schema.NullHandling{
			ReplaceEntityNullWithZero:  m.NullHandling.ReplaceEntityNullWithZero,
			ReplaceMeasureNullWithZero: m.NullHandling.ReplaceMeasureNullWithZero,
		}
	}
	filters, err := exportFilter(m.Filter)
	if err != nil {
		return nil, err
	}
	out.Filters = filters

	out.Labels = m.Labels

	switch {
	case m.TypeSpec == nil:
		return nil, fmt.Errorf("has no type spec")
	case m.TypeSpec.AverageMetricSpec != nil:
		spec := m.TypeSpec.AverageMetricSpec
		if spec.Aggregation == nil {
			return nil, fmt.Errorf("incomplete average spec")
		}
		op, threshold, cap, err := exportAggregation(spec.Aggregation)
		if err != nil {
			return nil, err
		}
		// COUNT needs no measure column; anything else without one is broken.
		if spec.Measurement != nil {
			out.Measure = measureName(spec.Measurement.Name)
		} else if op != "count" {
			return nil, fmt.Errorf("operation %s has no measure column", op)
		}
		out.Operation = op
		out.AggregationThreshold = threshold
		out.Cap = cap
	case m.TypeSpec.QuantileMetricSpec != nil:
		spec := m.TypeSpec.QuantileMetricSpec
		if spec.Aggregation == nil {
			return nil, fmt.Errorf("incomplete quantile spec")
		}
		op, threshold, cap, err := exportAggregation(spec.Aggregation)
		if err != nil {
			return nil, err
		}
		if spec.Measurement != nil {
			out.Measure = measureName(spec.Measurement.Name)
		} else if op != "count" {
			return nil, fmt.Errorf("operation %s has no measure column", op)
		}
		out.Operation = op
		out.AggregationThreshold = threshold
		out.Cap = cap
		out.QuantileLevel = &spec.QuantileLevel
	case m.TypeSpec.RatioMetricSpec != nil:
		spec := m.TypeSpec.RatioMetricSpec
		num, err := exportAggregationSpec(spec.Numerator, spec.NumeratorAggregation, spec.NumeratorFilter, measureName)
		if err != nil {
			return nil, fmt.Errorf("numerator: %w", err)
		}
		den, err := exportAggregationSpec(spec.Denominator, spec.DenominatorAggregation, spec.DenominatorFilter, measureName)
		if err != nil {
			return nil, fmt.Errorf("denominator: %w", err)
		}
		// The schema has one aggregation_threshold, mapped to the numerator;
		// a denominator threshold would be silently dropped on round-trip.
		if spec.DenominatorAggregation != nil && spec.DenominatorAggregation.Threshold != nil {
			return nil, fmt.Errorf("denominator has an aggregation threshold, not expressible in the schema")
		}
		// Threshold lives on the numerator aggregation in the wire mapping.
		if spec.NumeratorAggregation != nil && spec.NumeratorAggregation.Threshold != nil {
			_, threshold, _, err := exportAggregation(spec.NumeratorAggregation)
			if err != nil {
				return nil, err
			}
			out.AggregationThreshold = threshold
		}
		out.Numerator, out.Denominator = num, den
	default:
		return nil, fmt.Errorf("unsupported type spec")
	}
	return out, nil
}

func exportMetric(m confidence.Metric, entityDisplay map[string]string, measurementByName map[string]confidence.Measurement) (*schema.Metric, error) {
	entity, ok := entityDisplay[m.Entity]
	if !ok {
		return nil, fmt.Errorf("references unknown entity %q", m.Entity)
	}
	meas, ok := measurementByName[m.Measurement]
	if !ok {
		return nil, fmt.Errorf("references unknown measurement %q", m.Measurement)
	}

	out := &schema.Metric{
		DisplayName: m.DisplayName,
		Entity:      entity,
		Measurement: meas.DisplayName,
		Description: schema.ExactText(m.Description),
		Owner:       m.Owner,
		Labels:      m.Labels,
	}

	// Emit metric-level filters when they are distinct from the measurement's
	// (materialized copies are omitted — they round-trip via the measurement).
	if hasDistinctFilter(m, meas) {
		filters, err := exportFilter(m.Filter)
		if err != nil {
			return nil, err
		}
		out.Filters = filters
	}

	switch m.PreferredDirection {
	case "INCREASE":
		out.PreferredDirection = "increase"
	case "DECREASE":
		out.PreferredDirection = "decrease"
	case "", "PREFERRED_DIRECTION_UNSPECIFIED":
	default:
		return nil, fmt.Errorf("unknown preferred direction %q", m.PreferredDirection)
	}
	if m.DefaultEffectSize != nil && m.DefaultEffectSize.Value != "" {
		v, err := strconv.ParseFloat(m.DefaultEffectSize.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("bad default effect size %q", m.DefaultEffectSize.Value)
		}
		out.DefaultEffectSize = &v
	}
	if vrc := m.VarianceReductionConfig; vrc != nil {
		if vrc.Disabled || vrc.AggregationWindowOverride != "" {
			vr := &schema.VarianceReduction{}
			if vrc.Disabled {
				f := false
				vr.Enabled = &f
			}
			if vrc.AggregationWindowOverride != "" {
				vr.PreExposureWindow = vrc.AggregationWindowOverride
			}
			out.VarianceReduction = vr
		}
	}
	if mc := m.MeasurementConfig; mc != nil {
		switch {
		case mc.ClosedWindow != nil:
			w := &schema.MeasurementWindow{AggregationWindow: mc.ClosedWindow.AggregationWindow}
			if mc.ClosedWindow.ExposureOffset != "" && mc.ClosedWindow.ExposureOffset != "0s" {
				w.ExposureOffset = mc.ClosedWindow.ExposureOffset
			}
			out.MeasurementWindow = w
		case mc.SemiOpenWindow != nil:
			w := &schema.MeasurementWindow{
				Type:              "semi_open",
				AggregationWindow: mc.SemiOpenWindow.AggregationWindow,
			}
			if mc.SemiOpenWindow.ExposureOffset != "" {
				w.ExposureOffset = mc.SemiOpenWindow.ExposureOffset
			}
			out.MeasurementWindow = w
		case mc.OpenWindow != nil:
			out.MeasurementWindow = &schema.MeasurementWindow{Type: "open"}
		default:
			return nil, fmt.Errorf("window type not expressible in the schema")
		}
	}
	return out, nil
}

// measureNameIndex maps a fact table's query columns back to measure
// references. Named measures use their display name; unnamed measures
// use the column name directly (the measurement's measure: field
// references them by column).
func measureNameIndex(ft confidence.FactTable) func(column string) string {
	byColumn := map[string]string{}
	for _, m := range ft.Measures {
		if m.Column != nil {
			if m.DisplayName != "" {
				byColumn[m.Column.Name] = m.DisplayName
			} else {
				byColumn[m.Column.Name] = m.Column.Name
			}
		}
	}
	return func(column string) string {
		if name, ok := byColumn[column]; ok {
			return name
		}
		return column
	}
}

var reverseAggregation = map[string]string{
	"AGGREGATION_TYPE_SUM":            "sum",
	"AGGREGATION_TYPE_AVG":            "avg",
	"AGGREGATION_TYPE_MAX":            "max",
	"AGGREGATION_TYPE_MIN":            "min",
	"AGGREGATION_TYPE_COUNT":          "count",
	"AGGREGATION_TYPE_COUNT_DISTINCT": "count_distinct",
}

var reverseThresholdDirection = map[string]string{
	"AGGREGATION_THRESHOLD_DIRECTION_GT":  "gt",
	"AGGREGATION_THRESHOLD_DIRECTION_GTE": "gte",
	"AGGREGATION_THRESHOLD_DIRECTION_LT":  "lt",
	"AGGREGATION_THRESHOLD_DIRECTION_LTE": "lte",
}

func exportAggregation(agg *confidence.Aggregation) (string, *schema.AggregationThreshold, *schema.ValueCap, error) {
	op, ok := reverseAggregation[agg.Type]
	if !ok {
		return "", nil, nil, fmt.Errorf("unsupported aggregation type %q", agg.Type)
	}
	var threshold *schema.AggregationThreshold
	if agg.Threshold != nil {
		dir, ok := reverseThresholdDirection[agg.Threshold.Direction]
		if !ok {
			return "", nil, nil, fmt.Errorf("unsupported threshold direction %q", agg.Threshold.Direction)
		}
		val := 0.0
		if agg.Threshold.Threshold != nil {
			v, err := strconv.ParseFloat(agg.Threshold.Threshold.Value, 64)
			if err != nil {
				return "", nil, nil, fmt.Errorf("bad threshold value %q", agg.Threshold.Threshold.Value)
			}
			val = v
		}
		threshold = &schema.AggregationThreshold{Direction: dir, Value: val}
	}
	var vc *schema.ValueCap
	if agg.Cap != nil {
		vc = &schema.ValueCap{}
		if agg.Cap.Min != nil && agg.Cap.Min.Value != "" {
			v, err := strconv.ParseFloat(agg.Cap.Min.Value, 64)
			if err != nil {
				return "", nil, nil, fmt.Errorf("bad cap min value %q", agg.Cap.Min.Value)
			}
			vc.Min = &v
		}
		if agg.Cap.Max != nil && agg.Cap.Max.Value != "" {
			v, err := strconv.ParseFloat(agg.Cap.Max.Value, 64)
			if err != nil {
				return "", nil, nil, fmt.Errorf("bad cap max value %q", agg.Cap.Max.Value)
			}
			vc.Max = &v
		}
		if vc.Min == nil && vc.Max == nil {
			vc = nil
		}
	}
	return op, threshold, vc, nil
}

func exportAggregationSpec(col *confidence.Column, agg *confidence.Aggregation, filter *confidence.Filter, measureName func(string) string) (*schema.AggregationSpec, error) {
	if col == nil || agg == nil {
		return nil, fmt.Errorf("incomplete aggregation spec")
	}
	op, _, cap, err := exportAggregation(agg)
	if err != nil {
		return nil, err
	}
	filters, err := exportFilter(filter)
	if err != nil {
		return nil, err
	}
	return &schema.AggregationSpec{
		Measure:   measureName(col.Name),
		Operation: op,
		Filters:   filters,
		Cap:       cap,
	}, nil
}

// stringFilterValue unwraps a filter value oneof; only strings are
// expressible in the schema. Fails closed: an unset or unrecognized member
// must not round-trip as "".
func stringFilterValue(v confidence.FilterValue) (string, error) {
	if v.BoolValue != nil || v.NumberValue != nil || v.TimestampValue != nil || v.NullValue != nil {
		return "", fmt.Errorf("non-string filter value not expressible in the schema")
	}
	if v.StringValue == nil {
		return "", fmt.Errorf("filter value not expressible in the schema")
	}
	return *v.StringValue, nil
}

// exportFilter reverses the criteria+expression shape the CLI generates:
// a single ref, not(ref), or and(...) over refs/not(refs), with eq/set
// rules. Anything else is not expressible in the schema.
func exportFilter(f *confidence.Filter) ([]schema.Filter, error) {
	if f == nil {
		return nil, nil
	}
	type polarity struct {
		ref     string
		negated bool
	}
	var terms []polarity
	var walk func(e *confidence.Expression) error
	walk = func(e *confidence.Expression) error {
		switch {
		case e == nil:
			return fmt.Errorf("filter has no expression")
		case e.Ref != "":
			terms = append(terms, polarity{e.Ref, false})
		case e.Not != nil && e.Not.Ref != "":
			terms = append(terms, polarity{e.Not.Ref, true})
		case e.And != nil:
			for i := range e.And.Operands {
				if err := walk(&e.And.Operands[i]); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("filter expression shape not expressible in the schema")
		}
		return nil
	}
	if err := walk(f.Expression); err != nil {
		return nil, err
	}

	var out []schema.Filter
	for _, t := range terms {
		crit, ok := f.Criteria[t.ref]
		if !ok || crit.Attribute == nil {
			return nil, fmt.Errorf("filter references unknown criterion %q", t.ref)
		}
		attr := crit.Attribute
		var op string
		var values []string
		switch {
		case attr.EqRule != nil:
			if t.negated {
				op = "not_equals"
			} else {
				op = "equals"
			}
			s, err := stringFilterValue(attr.EqRule.Value)
			if err != nil {
				return nil, fmt.Errorf("filter on %q: %w", attr.Attribute, err)
			}
			values = []string{s}
		case attr.SetRule != nil:
			if t.negated {
				op = "not_equals"
			} else {
				op = "equals"
			}
			for _, v := range attr.SetRule.Values {
				s, err := stringFilterValue(v)
				if err != nil {
					return nil, fmt.Errorf("filter on %q: %w", attr.Attribute, err)
				}
				values = append(values, s)
			}
		case attr.LikeRule != nil:
			if t.negated {
				return nil, fmt.Errorf("negated like filter on %q not expressible in the schema", attr.Attribute)
			}
			op = "like"
			values = []string{attr.LikeRule.Pattern}
		case attr.RangeRule != nil:
			if t.negated {
				return nil, fmt.Errorf("negated range filter on %q not expressible in the schema", attr.Attribute)
			}
			r := attr.RangeRule
			switch {
			case r.StartExclusive != nil && r.EndInclusive == nil && r.EndExclusive == nil && r.StartInclusive == nil:
				op = "gt"
				s, err := stringFilterValue(*r.StartExclusive)
				if err != nil {
					return nil, fmt.Errorf("filter on %q: %w", attr.Attribute, err)
				}
				values = []string{s}
			case r.StartInclusive != nil && r.EndInclusive == nil && r.EndExclusive == nil && r.StartExclusive == nil:
				op = "gte"
				s, err := stringFilterValue(*r.StartInclusive)
				if err != nil {
					return nil, fmt.Errorf("filter on %q: %w", attr.Attribute, err)
				}
				values = []string{s}
			case r.EndExclusive != nil && r.StartInclusive == nil && r.StartExclusive == nil && r.EndInclusive == nil:
				op = "lt"
				s, err := stringFilterValue(*r.EndExclusive)
				if err != nil {
					return nil, fmt.Errorf("filter on %q: %w", attr.Attribute, err)
				}
				values = []string{s}
			case r.EndInclusive != nil && r.StartInclusive == nil && r.StartExclusive == nil && r.EndExclusive == nil:
				op = "lte"
				s, err := stringFilterValue(*r.EndInclusive)
				if err != nil {
					return nil, fmt.Errorf("filter on %q: %w", attr.Attribute, err)
				}
				values = []string{s}
			case r.StartInclusive != nil && r.EndInclusive != nil && r.StartExclusive == nil && r.EndExclusive == nil:
				op = "between"
				s1, err := stringFilterValue(*r.StartInclusive)
				if err != nil {
					return nil, fmt.Errorf("filter on %q: %w", attr.Attribute, err)
				}
				s2, err := stringFilterValue(*r.EndInclusive)
				if err != nil {
					return nil, fmt.Errorf("filter on %q: %w", attr.Attribute, err)
				}
				values = []string{s1, s2}
			default:
				return nil, fmt.Errorf("range filter on %q has unsupported bound combination", attr.Attribute)
			}
		default:
			return nil, fmt.Errorf("filter rule on %q not expressible in the schema", attr.Attribute)
		}
		out = append(out, schema.Filter{Dimension: attr.Attribute, Operation: op, Values: values})
	}
	return out, nil
}

var reverseBaseUnit = map[string]string{
	"NONE": "none", "PERCENT": "percent", "SECOND": "second",
	"BYTE": "byte", "BIT": "bit",
}

var reverseDeclaredType = map[string]string{
	"COLUMN_TYPE_HLL_SKETCH": "hll_sketch",
}

func exportMeasureUnit(u *confidence.Unit) (*schema.MeasureUnit, error) {
	su := &schema.MeasureUnit{BaseUnitMultiplier: u.BaseUnitMultiplier}
	switch {
	case u.BaseUnit != "" && u.BaseUnit != "BASE_UNIT_UNSPECIFIED":
		v, ok := reverseBaseUnit[u.BaseUnit]
		if !ok {
			return nil, fmt.Errorf("unknown base unit %q not expressible in the schema", u.BaseUnit)
		}
		su.BaseUnit = v
	case u.CurrencyCode != "":
		su.CurrencyCode = u.CurrencyCode
	case u.CustomUnit != "":
		su.CustomUnit = u.CustomUnit
	}
	if su.BaseUnit == "" && su.CurrencyCode == "" && su.CustomUnit == "" {
		// No unit content (e.g. only the unspecified sentinel) — the schema
		// requires one of base/currency/custom, so emit no unit at all.
		return nil, nil
	}
	return su, nil
}

func exportDeclaredType(t string) (string, error) {
	// The API serializes the enum zero value explicitly; it means "not
	// declared", not an unknown type.
	if t == "COLUMN_TYPE_UNSPECIFIED" {
		return "", nil
	}
	v, ok := reverseDeclaredType[t]
	if !ok {
		return "", fmt.Errorf("unknown declared type %q not expressible in the schema", t)
	}
	return v, nil
}

// hasDistinctFilter reports whether a metric carries its own filter that
// differs from its measurement's. Sync materializes the measurement's
// filter onto the metric, so an identical copy is expected and should
// not be refused.
func hasDistinctFilter(m confidence.Metric, meas confidence.Measurement) bool {
	if m.Filter == nil && m.FilterString == "" {
		return false
	}
	if meas.Filter == nil {
		return true
	}
	mf, err1 := json.Marshal(m.Filter)
	sf, err2 := json.Marshal(meas.Filter)
	if err1 != nil || err2 != nil {
		return true
	}
	return string(mf) != string(sf)
}
