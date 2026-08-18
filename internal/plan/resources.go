package plan

import (
	"fmt"
	"sort"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/report"
)

// ExternalFactTableNames returns fact tables referenced by repository-defined
// measurements but not declared in the same desired state.
func ExternalFactTableNames(desired Desired) []string {
	local := map[string]bool{}
	for _, d := range desired.FactTables {
		local[d.Def.Name] = true
	}
	external := map[string]bool{}
	for _, d := range desired.Measurements {
		if !local[d.Def.FactTable] {
			external[d.Def.FactTable] = true
		}
	}
	names := make([]string, 0, len(external))
	for name := range external {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// BuildSyncResources maps the desired state to an ApplyMetricsSync resource
// list, in dependency order (fact tables, measurements, metrics). Mapping
// failures (unresolvable entities, unsupported enums) come back as
// positioned diagnostics; the server does the rest (matching, ownership,
// diff, reconciliation).
func BuildSyncResources(desired Desired, refs Refs, externalFactTables ...confidence.FactTable) ([]confidence.SyncResource, []report.Diagnostic) {
	var resources []confidence.SyncResource
	var diags []report.Diagnostic
	factTablesByName := map[string]confidence.FactTable{}
	for _, ft := range externalFactTables {
		factTablesByName[ft.Name] = ft
	}

	for _, d := range desired.FactTables {
		wire, err := wireFactTable(d, refs)
		if err != nil {
			diags = append(diags, d.Loc.diag(report.Error, "mapping", err.Error()))
			continue
		}
		factTablesByName[wire.Name] = *wire
		resources = append(resources, confidence.SyncResource{FactTable: wire})
	}

	for _, d := range desired.Measurements {
		factTable, ok := factTablesByName[d.Def.FactTable]
		if !ok {
			diags = append(diags, d.Loc.diag(report.Error, "mapping",
				fmt.Sprintf("measurement %q references fact table %q, which could not be loaded", d.Def.DisplayName, d.Def.FactTable)))
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
