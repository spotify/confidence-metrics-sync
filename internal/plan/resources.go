package plan

import (
	"fmt"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/report"
	"github.com/spotify/confidence-metrics-sync/internal/schema"
)

func findFactTableDef(desired Desired, displayName string) (schema.FactTable, bool) {
	for _, d := range desired.FactTables {
		if d.Def.DisplayName == displayName {
			return d.Def, true
		}
	}
	return schema.FactTable{}, false
}

// BuildSyncResources maps the desired state to an ApplyMetricsSync resource
// list, in dependency order (fact tables, measurements, metrics). Mapping
// failures (unresolvable entities, unsupported enums) come back as
// positioned diagnostics; the server does the rest (matching, ownership,
// diff, reconciliation).
func BuildSyncResources(desired Desired, refs Refs) ([]confidence.SyncResource, []report.Diagnostic) {
	var resources []confidence.SyncResource
	var diags []report.Diagnostic

	for _, d := range desired.FactTables {
		wire, err := wireFactTable(d, refs)
		if err != nil {
			diags = append(diags, d.Loc.diag(report.Error, "mapping", err.Error()))
			continue
		}
		resources = append(resources, confidence.SyncResource{FactTable: wire})
	}

	for _, d := range desired.Measurements {
		factTable, ok := findFactTableDef(desired, d.Def.FactTable)
		if !ok {
			diags = append(diags, d.Loc.diag(report.Error, "mapping",
				fmt.Sprintf("measurement %q references fact table %q, which is not defined in this repository", d.Def.DisplayName, d.Def.FactTable)))
			continue
		}
		wire, err := wireMeasurement(d, factTable, refs)
		if err != nil {
			diags = append(diags, d.Loc.diag(report.Error, "mapping", err.Error()))
			continue
		}
		resources = append(resources, confidence.SyncResource{Measurement: wire})
	}

	for _, d := range desired.Metrics {
		wire, err := wireMetric(d, refs)
		if err != nil {
			diags = append(diags, d.Loc.diag(report.Error, "mapping", err.Error()))
			continue
		}
		resources = append(resources, confidence.SyncResource{Metric: wire})
	}

	return resources, diags
}
