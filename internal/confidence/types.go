package confidence

// Wire types for the metrics service, mirroring the proto messages in
// protojson encoding (camelCase names, enums as strings, durations as "300s"
// strings, Decimal as {"value": "0.01"}). Only the fields the CLI reads or
// writes are declared; unknown response fields are ignored by design.

// Column names a column in a fact table's query output.
type Column struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"` // OUTPUT_ONLY: "COLUMN_TYPE_STRING", etc.
}

// EntityColumnMapping maps an entity (resource name, e.g. "entities/abc") to
// the column holding its identifier.
type EntityColumnMapping struct {
	Column *Column `json:"column,omitempty"`
	Entity string  `json:"entity,omitempty"`
}

// Unit describes the physical unit of a measure.
type Unit struct {
	BaseUnitMultiplier float64 `json:"baseUnitMultiplier,omitempty"`
	BaseUnit           string  `json:"baseUnit,omitempty"`
	CurrencyCode       string  `json:"currencyCode,omitempty"`
	CustomUnit         string  `json:"customUnit,omitempty"`
}

// Measure is a named, aggregatable column on a fact table.
type Measure struct {
	Column       *Column `json:"column,omitempty"`
	DisplayName  string  `json:"displayName,omitempty"`
	Unit         *Unit   `json:"unit,omitempty"`
	DeclaredType string  `json:"declaredType,omitempty"`
}

// FactTable is a queryable set of facts.
type FactTable struct {
	Name            string                `json:"name,omitempty"` // server-assigned, e.g. "factTables/xyz"
	SQL             string                `json:"sql,omitempty"`
	DisplayName     string                `json:"displayName,omitempty"`
	Description     string                `json:"description,omitempty"`
	TimestampColumn *Column               `json:"timestampColumn,omitempty"`
	Entities        []EntityColumnMapping `json:"entities,omitempty"`
	Measures        []Measure             `json:"measures,omitempty"`
	Dimensions      []Column              `json:"dimensions,omitempty"`
	State           string                `json:"state,omitempty"` // OUTPUT_ONLY TableState
	Error           *OperationError       `json:"error,omitempty"` // OUTPUT_ONLY, set when State is FAILED
	Owner           string                `json:"owner,omitempty"`
	DataWarehouse   string                `json:"dataWarehouse,omitempty"`
	Labels          map[string]string     `json:"labels,omitempty"`
	Source          *MetricSource         `json:"source,omitempty"`        // OUTPUT_ONLY (set by ApplyMetricsSync)
	SystemCreated   bool                  `json:"systemCreated,omitempty"` // OUTPUT_ONLY
	DeleteTime      string                `json:"deleteTime,omitempty"`    // OUTPUT_ONLY
}

// OperationError describes why a table operation failed.
type OperationError struct {
	Message string   `json:"message,omitempty"`
	Details []string `json:"details,omitempty"`
}

// Fact table states relevant to post-apply polling.
const (
	TableStateCreating = "TABLE_STATE_CREATING"
	TableStateUpdating = "TABLE_STATE_UPDATING"
	TableStateActive   = "TABLE_STATE_ACTIVE"
	TableStateFailed   = "TABLE_STATE_FAILED"
)

// Decimal mirrors google.type.Decimal.
type Decimal struct {
	Value string `json:"value,omitempty"`
}

// ValueCap constrains aggregated values to a min/max range.
type ValueCap struct {
	Min *Decimal `json:"min,omitempty"`
	Max *Decimal `json:"max,omitempty"`
}

// Aggregation is a within-entity aggregation.
type Aggregation struct {
	Type      string                `json:"type,omitempty"` // AGGREGATION_TYPE_*
	Threshold *AggregationThreshold `json:"threshold,omitempty"`
	Cap       *ValueCap             `json:"cap,omitempty"`
}

// AggregationThreshold converts an aggregate into a boolean via comparison.
type AggregationThreshold struct {
	Threshold *Decimal `json:"threshold,omitempty"`
	Direction string   `json:"direction,omitempty"`
}

// TypeSpec mirrors Metric.MetricTypeSpec (oneof: exactly one member set).
type TypeSpec struct {
	AverageMetricSpec  *AverageMetricSpec  `json:"averageMetricSpec,omitempty"`
	RatioMetricSpec    *RatioMetricSpec    `json:"ratioMetricSpec,omitempty"`
	QuantileMetricSpec *QuantileMetricSpec `json:"quantileMetricSpec,omitempty"`
}

// AverageMetricSpec aggregates one measure column.
type AverageMetricSpec struct {
	Measurement *Column      `json:"measurement,omitempty"`
	Aggregation *Aggregation `json:"aggregation,omitempty"`
}

// QuantileMetricSpec aggregates one measure column at a given quantile level.
type QuantileMetricSpec struct {
	Measurement   *Column      `json:"measurement,omitempty"`
	Aggregation   *Aggregation `json:"aggregation,omitempty"`
	QuantileLevel float64      `json:"quantileLevel,omitempty"`
}

// RatioMetricSpec aggregates a numerator and denominator separately.
type RatioMetricSpec struct {
	Numerator              *Column      `json:"numerator,omitempty"`
	NumeratorAggregation   *Aggregation `json:"numeratorAggregation,omitempty"`
	Denominator            *Column      `json:"denominator,omitempty"`
	DenominatorAggregation *Aggregation `json:"denominatorAggregation,omitempty"`
	NumeratorFilter        *Filter      `json:"numeratorFilter,omitempty"`
	DenominatorFilter      *Filter      `json:"denominatorFilter,omitempty"`
}

// Filter is a criteria map plus a boolean expression over criterion refs.
type Filter struct {
	Criteria   map[string]FilterCriterion `json:"criteria,omitempty"`
	Expression *Expression                `json:"expression,omitempty"`
}

// FilterCriterion holds a single attribute rule.
type FilterCriterion struct {
	Attribute *AttributeCriterion `json:"attribute,omitempty"`
}

// AttributeCriterion applies one rule to one attribute (dimension column).
type AttributeCriterion struct {
	Attribute string     `json:"attribute,omitempty"`
	EqRule    *EqRule    `json:"eqRule,omitempty"`
	SetRule   *SetRule   `json:"setRule,omitempty"`
	RangeRule *RangeRule `json:"rangeRule,omitempty"`
	LikeRule  *LikeRule  `json:"likeRule,omitempty"`
}

// EqRule matches one value.
type EqRule struct {
	Value FilterValue `json:"value"`
}

// SetRule matches any of the values.
type SetRule struct {
	Values []FilterValue `json:"values,omitempty"`
}

// LikeRule matches values by a pattern.
type LikeRule struct {
	Pattern string `json:"pattern,omitempty"`
}

// RangeRule matches values within a range.
type RangeRule struct {
	StartInclusive *FilterValue `json:"startInclusive,omitempty"`
	StartExclusive *FilterValue `json:"startExclusive,omitempty"`
	EndInclusive   *FilterValue `json:"endInclusive,omitempty"`
	EndExclusive   *FilterValue `json:"endExclusive,omitempty"`
}

// FilterValue is a oneof; the CLI only writes strings. The other members
// are declared so export can refuse them loudly instead of emitting "".
type FilterValue struct {
	StringValue    *string   `json:"stringValue,omitempty"`
	BoolValue      *bool     `json:"boolValue,omitempty"`
	NumberValue    *float64  `json:"numberValue,omitempty"`
	TimestampValue *string   `json:"timestampValue,omitempty"`
	NullValue      *struct{} `json:"nullValue,omitempty"`
}

// Expression is a boolean expression over criterion references.
type Expression struct {
	Ref string      `json:"ref,omitempty"`
	Not *Expression `json:"not,omitempty"`
	And *Operands   `json:"and,omitempty"`
	Or  *Operands   `json:"or,omitempty"`
}

// Operands are the children of an and/or expression.
type Operands struct {
	Operands []Expression `json:"operands,omitempty"`
}

// NullHandlingConfig controls NULL treatment.
type NullHandlingConfig struct {
	ReplaceMeasureNullWithZero bool `json:"replaceMeasureNullWithZero,omitempty"`
	ReplaceEntityNullWithZero  bool `json:"replaceEntityNullWithZero,omitempty"`
}

// Measurement is a reusable aggregation spec.
type Measurement struct {
	Name          string              `json:"name,omitempty"` // server-assigned, e.g. "measurements/xyz"
	DisplayName   string              `json:"displayName,omitempty"`
	Description   string              `json:"description,omitempty"`
	Entity        string              `json:"entity,omitempty"`    // resource name
	FactTable     string              `json:"factTable,omitempty"` // display name in sync requests (server resolves); resource name in responses
	TypeSpec      *TypeSpec           `json:"typeSpec,omitempty"`
	Filter        *Filter             `json:"filter,omitempty"`
	NullHandling  *NullHandlingConfig `json:"nullHandling,omitempty"`
	Owner         string              `json:"owner,omitempty"`
	Labels        map[string]string   `json:"labels,omitempty"`
	Source        *MetricSource       `json:"source,omitempty"`        // OUTPUT_ONLY (set by ApplyMetricsSync)
	SystemCreated bool                `json:"systemCreated,omitempty"` // OUTPUT_ONLY
	DeleteTime    string              `json:"deleteTime,omitempty"`    // OUTPUT_ONLY
}

// MeasurementConfig mirrors Metric.MeasurementConfig (oneof).
type MeasurementConfig struct {
	ClosedWindow   *WindowConfig `json:"closedWindow,omitempty"`
	SemiOpenWindow *WindowConfig `json:"semiOpenWindow,omitempty"`
	OpenWindow     *struct{}     `json:"openWindow,omitempty"`
}

// WindowConfig is a fixed measurement window; durations like "86400s".
type WindowConfig struct {
	AggregationWindow string `json:"aggregationWindow,omitempty"`
	ExposureOffset    string `json:"exposureOffset,omitempty"`
}

// VarianceReductionConfig controls variance reduction on a metric.
type VarianceReductionConfig struct {
	Disabled                  bool   `json:"disabled,omitempty"`
	AggregationWindowOverride string `json:"aggregationWindowOverride,omitempty"`
}

// MetricSource records how a metric is authored (API vs REPOSITORY).
type MetricSource struct {
	Type      string `json:"type,omitempty"` // "API" | "REPOSITORY"
	Reference string `json:"reference,omitempty"`
}

// Metric is the experiment-facing metric resource.
type Metric struct {
	Name                    string                   `json:"name,omitempty"` // server-assigned, e.g. "metrics/xyz"
	DisplayName             string                   `json:"displayName,omitempty"`
	Description             string                   `json:"description,omitempty"`
	Entity                  string                   `json:"entity,omitempty"`      // resource name
	Measurement             string                   `json:"measurement,omitempty"` // resource name
	MeasurementConfig       *MeasurementConfig       `json:"measurementConfig,omitempty"`
	PreferredDirection      string                   `json:"preferredDirection,omitempty"` // "INCREASE" | "DECREASE"
	DefaultEffectSize       *Decimal                 `json:"defaultEffectSize,omitempty"`
	Owner                   string                   `json:"owner,omitempty"`
	Labels                  map[string]string        `json:"labels,omitempty"`
	Filter                  *Filter                  `json:"filter,omitempty"`
	FilterString            string                   `json:"filterString,omitempty"`
	VarianceReductionConfig *VarianceReductionConfig `json:"varianceReductionConfig,omitempty"`
	Source                  *MetricSource            `json:"source,omitempty"`
	State                   string                   `json:"state,omitempty"`         // OUTPUT_ONLY
	SystemCreated           bool                     `json:"systemCreated,omitempty"` // OUTPUT_ONLY
	DeleteTime              string                   `json:"deleteTime,omitempty"`    // OUTPUT_ONLY
	Etag                    string                   `json:"etag,omitempty"`
}

// Entity is a unit of analysis (user, session, ...).
type Entity struct {
	Name        string `json:"name,omitempty"` // e.g. "entities/abc"
	DisplayName string `json:"displayName,omitempty"`
}

// ValidationError is a single finding from a Validate* RPC.
type ValidationError struct {
	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
}

// SyncResource is one resource in an ApplyMetricsSync request; exactly one
// member is set (proto oneof).
type SyncResource struct {
	Metric      *Metric      `json:"metric,omitempty"`
	Measurement *Measurement `json:"measurement,omitempty"`
	FactTable   *FactTable   `json:"factTable,omitempty"`
}

// ApplyMetricsSyncRequest is the single-door request for repo-managed writes.
type ApplyMetricsSyncRequest struct {
	Reference string         `json:"reference"`
	Resources []SyncResource `json:"resources,omitempty"`
	DryRun    bool           `json:"dryRun,omitempty"`
	// AdoptFrom lists the sources this sync may take ownership from: another
	// repository's reference, or ReferenceAPI for resources no repository
	// manages. A matched resource whose current source is not listed stays an
	// ownership error. There is no wildcard — adoption always names what it
	// takes over from.
	AdoptFrom []string `json:"adoptFrom,omitempty"`
}

// ReferenceAPI is the reserved AdoptFrom entry standing for "no repository
// manages this" — resources created through the API or console, and legacy
// ones with no recorded source. The server rejects it as a Reference so the
// two name spaces cannot collide.
const ReferenceAPI = "api"

// Outcome actions, mirroring ResourceOutcome.Action.
const (
	ActionCreate    = "CREATE"
	ActionUpdate    = "UPDATE"
	ActionUnchanged = "UNCHANGED"
	ActionArchive   = "ARCHIVE"
	ActionError     = "ERROR"
	ActionAdopt     = "ADOPT"
)

// ResourceOutcome reports what happened (or would happen) to one resource.
type ResourceOutcome struct {
	Name          string   `json:"name,omitempty"` // resource name, e.g. "metrics/abc"
	DisplayName   string   `json:"displayName,omitempty"`
	Action        string   `json:"action,omitempty"`
	ChangedFields []string `json:"changedFields,omitempty"` // populated on UPDATE and ADOPT
	Errors        []string `json:"errors,omitempty"`
	// PreviousReference is the source an ADOPT took the resource over from:
	// the previous repository's reference, or ReferenceAPI.
	PreviousReference string `json:"previousReference,omitempty"`
}

// ApplyMetricsSyncResponse carries per-resource outcomes; Archived lists
// resources owned by the reference that were absent from the request.
type ApplyMetricsSyncResponse struct {
	Outcomes []ResourceOutcome `json:"outcomes,omitempty"`
	Archived []ResourceOutcome `json:"archived,omitempty"`
}

// ValidateMetricResponse is the response of metrics:validateMetric.
type ValidateMetricResponse struct {
	Errors []ValidationError `json:"errors,omitempty"`
}

// ValidateFactTableResponse is the response of factTables:validateFactTable.
type ValidateFactTableResponse struct {
	Errors        []ValidationError `json:"errors,omitempty"`
	SchemaChecked bool              `json:"schemaChecked,omitempty"`
}

// Identity is an IAM identity — the owner of a metric, measurement or fact
// table. A deactivated identity still exists (a departed employee, a
// disabled client) but is no longer a meaningful owner.
type Identity struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	User        string `json:"user,omitempty"`      // oneof identity_kind
	Group       string `json:"group,omitempty"`     // oneof identity_kind
	ApiClient   string `json:"apiClient,omitempty"` // oneof identity_kind
	Service     string `json:"service,omitempty"`   // oneof identity_kind
	Agent       string `json:"agent,omitempty"`     // oneof identity_kind
	Everyone    bool   `json:"everyone,omitempty"`  // oneof identity_kind
	Deactivated bool   `json:"deactivated,omitempty"`
}

// IsUserOrGroup reports whether this identity is a user or group —
// the only kinds valid as metric owners.
func (id Identity) IsUserOrGroup() bool {
	return id.User != "" || id.Group != ""
}
