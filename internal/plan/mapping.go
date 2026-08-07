package plan

import (
	"fmt"
	"strconv"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/schema"
)

// Refs resolves entity display names to API resource names. Entities are the
// only resource the CLI must resolve itself: fact table and measurement
// references inside the request are sent as DISPLAY NAMES — ApplyMetricsSync
// resolves those display-name-first (covering resources created in the same
// request), falling back to resource names.
type Refs struct {
	EntityByDisplay map[string]string // "user" -> "entities/abc"
}

// RefsFromEntities builds the entity index from the account's entities.
func RefsFromEntities(entities []confidence.Entity) Refs {
	r := Refs{EntityByDisplay: map[string]string{}}
	for _, e := range entities {
		r.EntityByDisplay[e.DisplayName] = e.Name
	}
	return r
}

var aggregationTypes = map[string]string{
	"sum":            "AGGREGATION_TYPE_SUM",
	"avg":            "AGGREGATION_TYPE_AVG",
	"max":            "AGGREGATION_TYPE_MAX",
	"min":            "AGGREGATION_TYPE_MIN",
	"count":          "AGGREGATION_TYPE_COUNT",
	"count_distinct": "AGGREGATION_TYPE_COUNT_DISTINCT",
}

// thresholdDirections maps YAML directions to the API enum. NOTE: the API
// has no EQ — the YAML schema currently allows "eq" and must be fixed;
// mapping it is an error until then.
var thresholdDirections = map[string]string{
	"gt":  "AGGREGATION_THRESHOLD_DIRECTION_GT",
	"gte": "AGGREGATION_THRESHOLD_DIRECTION_GTE",
	"lt":  "AGGREGATION_THRESHOLD_DIRECTION_LT",
	"lte": "AGGREGATION_THRESHOLD_DIRECTION_LTE",
}

var preferredDirections = map[string]string{
	"increase": "INCREASE",
	"decrease": "DECREASE",
}

// wireFactTable maps a desired fact table to its API shape.
func wireFactTable(d DesiredFactTable, refs Refs) (*confidence.FactTable, error) {
	ft := &confidence.FactTable{
		SQL:             d.SQL,
		DisplayName:     d.Def.DisplayName,
		Description:     string(d.Def.Description),
		Owner:           d.Def.Owner,
		TimestampColumn: &confidence.Column{Name: d.Def.TimestampColumn},
	}
	for _, e := range d.Def.Entities {
		ref, ok := refs.EntityByDisplay[e.Entity]
		if !ok {
			return nil, fmt.Errorf("entity %q does not exist in the Confidence account", e.Entity)
		}
		ft.Entities = append(ft.Entities, confidence.EntityColumnMapping{
			Column: &confidence.Column{Name: e.Column},
			Entity: ref,
		})
	}
	for _, m := range d.Def.Measures {
		wm := confidence.Measure{
			Column:      &confidence.Column{Name: measureColumnName(d.Def, m)},
			DisplayName: m.DisplayName,
		}
		if m.Unit != nil {
			wm.Unit = wireMeasureUnit(m.Unit)
		}
		if m.Type != "" {
			wm.DeclaredType = wireDeclaredType(m.Type)
		}
		ft.Measures = append(ft.Measures, wm)
	}
	for _, dim := range d.Def.Dimensions {
		ft.Dimensions = append(ft.Dimensions, confidence.Column{Name: dim.Column})
	}
	ft.Labels = d.Def.Labels
	return ft, nil
}

// wireMeasurement maps a desired measurement to its API shape.
func wireMeasurement(d DesiredMeasurement, factTable schema.FactTable, refs Refs) (*confidence.Measurement, error) {
	entityRef, ok := refs.EntityByDisplay[d.Def.Entity]
	if !ok {
		return nil, fmt.Errorf("entity %q does not exist in the Confidence account", d.Def.Entity)
	}
	m := &confidence.Measurement{
		DisplayName: d.Def.DisplayName,
		Description: string(d.Def.Description),
		Entity:      entityRef,
		FactTable:   d.Def.FactTable, // display name; ApplyMetricsSync resolves
		Owner:       d.Def.Owner,
	}
	m.Labels = d.Def.Labels
	if d.Def.NullHandling != nil {
		m.NullHandling = &confidence.NullHandlingConfig{
			ReplaceMeasureNullWithZero: d.Def.NullHandling.ReplaceMeasureNullWithZero,
			ReplaceEntityNullWithZero:  d.Def.NullHandling.ReplaceEntityNullWithZero,
		}
	}

	// Measurement-level filters map to Measurement.filter (proto field 8).
	filter, err := wireFilter(d.Def.Filters)
	if err != nil {
		return nil, err
	}
	m.Filter = filter

	if d.Def.Operation != "" {
		agg, err := wireAggregation(d.Def.Operation, d.Def.AggregationThreshold, d.Def.Cap)
		if err != nil {
			return nil, err
		}
		spec := &confidence.AverageMetricSpec{Aggregation: agg}
		// COUNT counts facts and needs no measure column; every other
		// operation aggregates one (the schema enforces this too).
		if d.Def.Measure == "" && d.Def.Operation != "count" {
			return nil, fmt.Errorf("measurement %q: operation %q requires a measure", d.Def.DisplayName, d.Def.Operation)
		}
		if d.Def.Measure != "" {
			col, err := measureColumn(factTable, d.Def.Measure)
			if err != nil {
				return nil, err
			}
			spec.Measurement = &confidence.Column{Name: col}
		}
		if d.Def.QuantileLevel != nil {
			m.TypeSpec = &confidence.TypeSpec{QuantileMetricSpec: &confidence.QuantileMetricSpec{
				Measurement:   spec.Measurement,
				Aggregation:   spec.Aggregation,
				QuantileLevel: *d.Def.QuantileLevel,
			}}
		} else {
			m.TypeSpec = &confidence.TypeSpec{AverageMetricSpec: spec}
		}
	} else {
		var numCap *schema.ValueCap
		if d.Def.Numerator != nil {
			numCap = d.Def.Numerator.Cap
		}
		var denCap *schema.ValueCap
		if d.Def.Denominator != nil {
			denCap = d.Def.Denominator.Cap
		}
		numAgg, err := wireAggregation(d.Def.Numerator.Operation, d.Def.AggregationThreshold, numCap)
		if err != nil {
			return nil, err
		}
		denAgg, err := wireAggregation(d.Def.Denominator.Operation, nil, denCap)
		if err != nil {
			return nil, err
		}
		numFilter, err := wireFilter(d.Def.Numerator.Filters)
		if err != nil {
			return nil, err
		}
		denFilter, err := wireFilter(d.Def.Denominator.Filters)
		if err != nil {
			return nil, err
		}
		numCol, err := measureColumn(factTable, d.Def.Numerator.Measure)
		if err != nil {
			return nil, err
		}
		denCol, err := measureColumn(factTable, d.Def.Denominator.Measure)
		if err != nil {
			return nil, err
		}
		m.TypeSpec = &confidence.TypeSpec{
			RatioMetricSpec: &confidence.RatioMetricSpec{
				Numerator:              &confidence.Column{Name: numCol},
				NumeratorAggregation:   numAgg,
				Denominator:            &confidence.Column{Name: denCol},
				DenominatorAggregation: denAgg,
				NumeratorFilter:        numFilter,
				DenominatorFilter:      denFilter,
			},
		}
	}
	return m, nil
}

// wireMetric maps a desired metric to its API shape. The measurement
// reference is the DISPLAY NAME (ApplyMetricsSync resolves it, including for
// measurements created in the same request); `source` is set by the server
// from the request reference — never by the client.
func wireMetric(d DesiredMetric, refs Refs) (*confidence.Metric, error) {
	entityRef, ok := refs.EntityByDisplay[d.Entity]
	if !ok {
		return nil, fmt.Errorf("entity %q does not exist in the Confidence account", d.Entity)
	}
	m := &confidence.Metric{
		DisplayName: d.DisplayName,
		Description: d.Description,
		Entity:      entityRef,
		Measurement: d.Measurement, // display name; server resolves
		Owner:       d.Owner,
		Labels:      d.Labels,
	}
	// Metric-level filters.
	filter, err := wireFilter(d.Filters)
	if err != nil {
		return nil, err
	}
	m.Filter = filter

	if d.VarianceReduction != nil {
		vrc := &confidence.VarianceReductionConfig{}
		if d.VarianceReduction.Enabled != nil && !*d.VarianceReduction.Enabled {
			vrc.Disabled = true
		}
		if d.VarianceReduction.PreExposureWindow != "" {
			vrc.AggregationWindowOverride = d.VarianceReduction.PreExposureWindow
		}
		m.VarianceReductionConfig = vrc
	}
	if d.PreferredDirection != "" {
		m.PreferredDirection = preferredDirections[d.PreferredDirection]
	}
	if d.DefaultEffectSize != nil {
		m.DefaultEffectSize = &confidence.Decimal{
			Value: strconv.FormatFloat(*d.DefaultEffectSize, 'f', -1, 64),
		}
	}
	if d.Window != nil {
		switch d.Window.Type {
		case "", "closed":
			m.MeasurementConfig = &confidence.MeasurementConfig{
				ClosedWindow: &confidence.WindowConfig{
					AggregationWindow: d.Window.AggregationWindow,
					ExposureOffset:    orDefault(d.Window.ExposureOffset, "0s"),
				},
			}
		case "semi_open":
			wc := &confidence.WindowConfig{
				AggregationWindow: d.Window.AggregationWindow,
			}
			if d.Window.ExposureOffset != "" {
				wc.ExposureOffset = d.Window.ExposureOffset
			}
			m.MeasurementConfig = &confidence.MeasurementConfig{SemiOpenWindow: wc}
		case "open":
			m.MeasurementConfig = &confidence.MeasurementConfig{OpenWindow: &struct{}{}}
		}
	}
	return m, nil
}

func wireAggregation(operation string, threshold *schema.AggregationThreshold, cap *schema.ValueCap) (*confidence.Aggregation, error) {
	aggType, ok := aggregationTypes[operation]
	if !ok {
		return nil, fmt.Errorf("unsupported aggregation operation %q", operation)
	}
	agg := &confidence.Aggregation{Type: aggType}
	if threshold != nil {
		dir, ok := thresholdDirections[threshold.Direction]
		if !ok {
			return nil, fmt.Errorf("aggregation threshold direction %q is not supported by the Confidence API (supported: gt, gte, lt, lte)", threshold.Direction)
		}
		agg.Threshold = &confidence.AggregationThreshold{
			Threshold: &confidence.Decimal{Value: strconv.FormatFloat(threshold.Value, 'f', -1, 64)},
			Direction: dir,
		}
	}
	if cap != nil {
		agg.Cap = &confidence.ValueCap{}
		if cap.Min != nil {
			agg.Cap.Min = &confidence.Decimal{Value: strconv.FormatFloat(*cap.Min, 'f', -1, 64)}
		}
		if cap.Max != nil {
			agg.Cap.Max = &confidence.Decimal{Value: strconv.FormatFloat(*cap.Max, 'f', -1, 64)}
		}
	}
	return agg, nil
}

// wireFilter maps YAML dimension filters to the API's criteria+expression
// shape: equals/one value -> eqRule, equals/many -> setRule,
// not_equals -> not(ref); range/like filters use rangeRule/likeRule.
// Multiple filters are AND-ed.
func wireFilter(filters []schema.Filter) (*confidence.Filter, error) {
	if len(filters) == 0 {
		return nil, nil
	}
	f := &confidence.Filter{Criteria: map[string]confidence.FilterCriterion{}}
	var exprs []confidence.Expression
	for i, fl := range filters {
		key := "c" + strconv.Itoa(i)
		crit := &confidence.AttributeCriterion{Attribute: fl.Dimension}
		// The schema enforces minItems: 1, but the mapping must not depend on
		// upstream validation having run — an empty SetRule would be a
		// nonsense filter on the wire.
		if len(fl.Values) == 0 {
			return nil, fmt.Errorf("filter on dimension %q has no values", fl.Dimension)
		}

		ref := confidence.Expression{Ref: key}
		switch fl.Operation {
		case "equals", "not_equals":
			if len(fl.Values) == 1 {
				crit.EqRule = &confidence.EqRule{Value: confidence.FilterValue{StringValue: &fl.Values[0]}}
			} else {
				vals := make([]confidence.FilterValue, 0, len(fl.Values))
				for _, v := range fl.Values {
					v := v
					vals = append(vals, confidence.FilterValue{StringValue: &v})
				}
				crit.SetRule = &confidence.SetRule{Values: vals}
			}
			if fl.Operation == "equals" {
				exprs = append(exprs, ref)
			} else {
				exprs = append(exprs, confidence.Expression{Not: &ref})
			}
		case "like", "gt", "gte", "lt", "lte":
			if len(fl.Values) != 1 {
				return nil, fmt.Errorf("filter on dimension %q with operation %q requires exactly 1 value", fl.Dimension, fl.Operation)
			}
			switch fl.Operation {
			case "like":
				crit.LikeRule = &confidence.LikeRule{Pattern: fl.Values[0]}
			case "gt":
				crit.RangeRule = &confidence.RangeRule{StartExclusive: &confidence.FilterValue{StringValue: &fl.Values[0]}}
			case "gte":
				crit.RangeRule = &confidence.RangeRule{StartInclusive: &confidence.FilterValue{StringValue: &fl.Values[0]}}
			case "lt":
				crit.RangeRule = &confidence.RangeRule{EndExclusive: &confidence.FilterValue{StringValue: &fl.Values[0]}}
			case "lte":
				crit.RangeRule = &confidence.RangeRule{EndInclusive: &confidence.FilterValue{StringValue: &fl.Values[0]}}
			}
			exprs = append(exprs, ref)
		case "between":
			if len(fl.Values) != 2 {
				return nil, fmt.Errorf("filter on dimension %q with operation \"between\" requires exactly 2 values", fl.Dimension)
			}
			v0 := fl.Values[0]
			v1 := fl.Values[1]
			crit.RangeRule = &confidence.RangeRule{
				StartInclusive: &confidence.FilterValue{StringValue: &v0},
				EndInclusive:   &confidence.FilterValue{StringValue: &v1},
			}
			exprs = append(exprs, ref)
		default:
			return nil, fmt.Errorf("unsupported filter operation %q", fl.Operation)
		}
		f.Criteria[key] = confidence.FilterCriterion{Attribute: crit}
	}
	if len(exprs) == 1 {
		f.Expression = &exprs[0]
	} else {
		f.Expression = &confidence.Expression{And: &confidence.Operands{Operands: exprs}}
	}
	return f, nil
}

// measureColumn resolves a measure reference to the query-output column.
// Matches by display name first; for unnamed measures, falls back to column name.
func measureColumn(ft schema.FactTable, measureRef string) (string, error) {
	for _, m := range ft.Measures {
		if m.DisplayName != "" && m.DisplayName == measureRef {
			return measureColumnName(ft, m), nil
		}
	}
	for _, m := range ft.Measures {
		if m.DisplayName == "" && m.Column == measureRef {
			return measureColumnName(ft, m), nil
		}
	}
	return "", fmt.Errorf("measure %q is not defined on fact table %q", measureRef, ft.DisplayName)
}

var baseUnits = map[string]string{
	"none":    "NONE",
	"percent": "PERCENT",
	"second":  "SECOND",
	"byte":    "BYTE",
	"bit":     "BIT",
}

var declaredTypes = map[string]string{
	"hll_sketch": "COLUMN_TYPE_HLL_SKETCH",
}

func wireMeasureUnit(u *schema.MeasureUnit) *confidence.Unit {
	wu := &confidence.Unit{BaseUnitMultiplier: u.BaseUnitMultiplier}
	switch {
	case u.BaseUnit != "":
		wu.BaseUnit = baseUnits[u.BaseUnit]
	case u.CurrencyCode != "":
		wu.CurrencyCode = u.CurrencyCode
	case u.CustomUnit != "":
		wu.CustomUnit = u.CustomUnit
	}
	return wu
}

func wireDeclaredType(t string) string {
	if v, ok := declaredTypes[t]; ok {
		return v
	}
	return ""
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
