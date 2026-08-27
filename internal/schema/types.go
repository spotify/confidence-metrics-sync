package schema

import (
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// File is the top-level structure of one metric definition YAML file.
type File struct {
	FactTables   []FactTable   `yaml:"fact_tables,omitempty"`
	Measurements []Measurement `yaml:"measurements,omitempty"`
	Metrics      []Metric      `yaml:"metrics,omitempty"`
}

// ExactText is a free-text string (SQL, descriptions) whose YAML form must
// round-trip byte-exactly. yaml.v3 mis-emits nested block scalars whose
// first line starts with whitespace: the indentation indicator it writes
// disagrees with its own parser and the leading whitespace of every line is
// silently lost — or, for exotic whitespace like U+2028, the output does not
// re-parse at all (reproduced with yaml.v3 v3.0.1; only below the document's
// top level). Values starting with any Unicode whitespace are emitted as
// double-quoted scalars instead — uglier, but exact; that is a superset of
// the broken set, so a few extra leading characters get quoted harmlessly.
// Everything else keeps the default block style.
type ExactText string

func (s ExactText) MarshalYAML() (interface{}, error) {
	if r, _ := utf8.DecodeRuneInString(string(s)); unicode.IsSpace(r) {
		return &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Style: yaml.DoubleQuotedStyle,
			Value: string(s),
		}, nil
	}
	return string(s), nil
}

// FactTable defines a data source with entity mappings, measures, and
// optional dimensions. Exactly one of SQL or Table must be set.
type FactTable struct {
	Name            string            `yaml:"name,omitempty"`
	DisplayName     string            `yaml:"display_name,omitempty"`
	SQL             ExactText         `yaml:"sql,omitempty"`
	Table           string            `yaml:"table,omitempty"`
	TimestampColumn string            `yaml:"timestamp_column,omitempty"`
	Entities        []EntityMapping   `yaml:"entities,omitempty"`
	Measures        []Measure         `yaml:"measures,omitempty"`
	Dimensions      []Dimension       `yaml:"dimensions,omitempty"`
	Owner           string            `yaml:"owner,omitempty"`
	Description     ExactText         `yaml:"description,omitempty"`
	Labels          map[string]string `yaml:"labels,omitempty"`
}

// EntityMapping maps an entity to the column containing its identifier.
type EntityMapping struct {
	Entity string `yaml:"entity,omitempty"`
	Column string `yaml:"column,omitempty"`
}

// MeasureUnit describes the physical unit of a measure.
type MeasureUnit struct {
	BaseUnit           string  `yaml:"base_unit,omitempty"`
	CurrencyCode       string  `yaml:"currency_code,omitempty"`
	CustomUnit         string  `yaml:"custom_unit,omitempty"`
	BaseUnitMultiplier float64 `yaml:"base_unit_multiplier,omitempty"`
}

// Measure is a column on a fact table that can be aggregated.
type Measure struct {
	DisplayName string       `yaml:"display_name,omitempty"`
	Column      string       `yaml:"column,omitempty"`
	Description ExactText    `yaml:"description,omitempty"`
	Type        string       `yaml:"type,omitempty"`
	Unit        *MeasureUnit `yaml:"unit,omitempty"`
}

// Dimension is a column used for filtering/slicing.
type Dimension struct {
	Name        string    `yaml:"name,omitempty"`
	Column      string    `yaml:"column,omitempty"`
	Description ExactText `yaml:"description,omitempty"`
}

// Measurement bundles a fact table, measure(s), aggregation, and null
// handling. Either Measure+Operation (simple) or Numerator+Denominator
// (ratio) is set, never both.
type Measurement struct {
	Name                 string                `yaml:"name,omitempty"`
	DisplayName          string                `yaml:"display_name,omitempty"`
	FactTable            string                `yaml:"fact_table,omitempty"`
	Entity               string                `yaml:"entity,omitempty"`
	Owner                string                `yaml:"owner,omitempty"`
	Measure              string                `yaml:"measure,omitempty"`
	Operation            string                `yaml:"operation,omitempty"`
	Numerator            *AggregationSpec      `yaml:"numerator,omitempty"`
	Denominator          *AggregationSpec      `yaml:"denominator,omitempty"`
	Description          ExactText             `yaml:"description,omitempty"`
	Filters              []Filter              `yaml:"filters,omitempty"`
	AggregationThreshold *AggregationThreshold `yaml:"aggregation_threshold,omitempty"`
	Cap                  *ValueCap             `yaml:"cap,omitempty"`
	QuantileLevel        *float64              `yaml:"quantile_level,omitempty"`
	NullHandling         *NullHandling         `yaml:"null_handling,omitempty"`
	Labels               map[string]string     `yaml:"labels,omitempty"`
	Metrics              []Metric              `yaml:"metrics,omitempty"`
}

// AggregationSpec is the numerator or denominator of a ratio measurement.
type AggregationSpec struct {
	Measure   string    `yaml:"measure,omitempty"`
	Operation string    `yaml:"operation,omitempty"`
	Filters   []Filter  `yaml:"filters,omitempty"`
	Cap       *ValueCap `yaml:"cap,omitempty"`
}

// Metric is a metric definition. In the flat flavor (top-level metrics list)
// Entity and Measurement are required; in the nested flavor (under a
// measurement) they are inherited from the parent and must be absent — the
// JSON Schema enforces the distinction, the struct is a superset of both.
// VarianceReduction controls variance reduction on a metric.
type VarianceReduction struct {
	Enabled           *bool  `yaml:"enabled,omitempty"`
	PreExposureWindow string `yaml:"pre_exposure_window,omitempty"`
}

type Metric struct {
	Name               string             `yaml:"name,omitempty"`
	DisplayName        string             `yaml:"display_name,omitempty"`
	Entity             string             `yaml:"entity,omitempty"`
	Measurement        string             `yaml:"measurement,omitempty"`
	Description        ExactText          `yaml:"description,omitempty"`
	Owner              string             `yaml:"owner,omitempty"`
	PreferredDirection string             `yaml:"preferred_direction,omitempty"`
	DefaultEffectSize  *float64           `yaml:"default_effect_size,omitempty"`
	MeasurementWindow  *MeasurementWindow `yaml:"measurement_window,omitempty"`
	Filters            []Filter           `yaml:"filters,omitempty"`
	Labels             map[string]string  `yaml:"labels,omitempty"`
	VarianceReduction  *VarianceReduction `yaml:"variance_reduction,omitempty"`
}

// MeasurementWindow is the collection window relative to exposure.
// Durations are protojson-style strings, e.g. "86400s".
type MeasurementWindow struct {
	Type              string `yaml:"type,omitempty"`
	AggregationWindow string `yaml:"aggregation_window,omitempty"`
	ExposureOffset    string `yaml:"exposure_offset,omitempty"`
}

// NullHandling controls how NULLs are treated in metric computation.
type NullHandling struct {
	ReplaceEntityNullWithZero  bool `yaml:"replace_entity_null_with_zero,omitempty"`
	ReplaceMeasureNullWithZero bool `yaml:"replace_measure_null_with_zero,omitempty"`
}

// ValueCap constrains aggregated values to a min/max range.
type ValueCap struct {
	Min *float64 `yaml:"min,omitempty"`
	Max *float64 `yaml:"max,omitempty"`
}

// AggregationThreshold turns an aggregate into a boolean via comparison.
type AggregationThreshold struct {
	Direction string  `yaml:"direction,omitempty"`
	Value     float64 `yaml:"value"` // required; zero is a legitimate value, so no omitempty
}

// Filter restricts which rows are included, by dimension.
type Filter struct {
	Dimension string   `yaml:"dimension,omitempty"`
	Operation string   `yaml:"operation,omitempty"`
	Values    []string `yaml:"values,omitempty"`
}
