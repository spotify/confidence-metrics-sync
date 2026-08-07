package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/parser"
	"github.com/spotify/confidence-metrics-sync/internal/plan"
	"github.com/spotify/confidence-metrics-sync/internal/report"
	"github.com/spotify/confidence-metrics-sync/internal/validate"
)

// loadValidated runs tier 1: parse everything under path and validate
// offline (schema + semantic checks).
func loadValidated(path string) (files []*parser.File, found int, diags []report.Diagnostic, err error) {
	files, found, diags, err = parser.LoadDir(path)
	if err != nil {
		return nil, 0, nil, err
	}
	if found == 0 {
		return nil, 0, nil, nil
	}
	vdiags, err := validate.Files(files)
	if err != nil {
		return nil, 0, nil, err
	}
	return files, found, append(diags, vdiags...), nil
}

// buildSyncRequest maps the parsed files to an ApplyMetricsSync request.
// Entity references are resolved against the account; everything else is
// sent by display name for the server to resolve.
func buildSyncRequest(ctx context.Context, client *confidence.Client, files []*parser.File, sourceReference string, dryRun bool, adoptFrom []string) (*confidence.ApplyMetricsSyncRequest, []report.Diagnostic, error) {
	entities, err := client.ListEntities(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("listing entities: %w", err)
	}
	desired := plan.Normalize(files)

	// Resolve friendly owner names (display name / email): never auto-
	// substitutes, always fails with a paste-ready pinned replacement.
	rdiags, err := plan.ResolveOwners(ctx, desired, client.LookupIdentitiesByFriendlyName)
	if err != nil {
		return nil, nil, err
	}

	resources, diags := plan.BuildSyncResources(desired, plan.RefsFromEntities(entities))
	diags = append(diags, rdiags...)

	// Owners are stored after a syntax parse only, so a well-formed but wrong
	// identity is accepted silently — check existence here, in the one path
	// both validate and sync go through.
	odiags, err := plan.CheckOwners(ctx, desired, client.LookupIdentities)
	if err != nil {
		return nil, nil, err
	}
	diags = append(diags, odiags...)

	return &confidence.ApplyMetricsSyncRequest{
		Reference: sourceReference,
		Resources: resources,
		DryRun:    dryRun,
		AdoptFrom: adoptFrom,
	}, diags, nil
}

// checkAdoptFrom rejects the two flag combinations that cannot mean anything,
// before spending a round trip on them.
func checkAdoptFrom(sourceReference string, adoptFrom []string) error {
	if sourceReference == confidence.ReferenceAPI {
		return fmt.Errorf("--source-reference must not be %q: that value is reserved for --adopt-from",
			confidence.ReferenceAPI)
	}
	for _, from := range adoptFrom {
		if from == sourceReference {
			return fmt.Errorf("--adopt-from %q is this repository's own --source-reference; resources it already owns are updated, not adopted", from)
		}
	}
	return nil
}

// unusedAdoptFrom warns about --adopt-from entries that took nothing over.
// Adoption is a one-time migration step, so an entry that adopted nothing has
// either already done its job or names the wrong source — and a flag left
// standing in CI is what turns a future display-name collision into a silent
// takeover. Skipped when any resource errored: nothing was adopted because
// nothing was applied, which says nothing about the flag.
func unusedAdoptFrom(adoptFrom []string, items []report.OutcomeItem) []report.Diagnostic {
	if len(adoptFrom) == 0 || report.HasOutcomeErrors(items) {
		return nil
	}
	used := map[string]bool{}
	for _, it := range items {
		if it.Action == confidence.ActionAdopt {
			used[it.PreviousReference] = true
		}
	}
	var unused []string
	for _, from := range adoptFrom {
		if !used[from] {
			unused = append(unused, from)
		}
	}
	if len(unused) == 0 {
		return nil
	}
	return []report.Diagnostic{{
		Severity: report.Warning, Rule: "adopt-from",
		Message: fmt.Sprintf("--adopt-from %s adopted nothing — drop the flag once the migration has landed, and never leave it standing in CI: it would silently take over a resource that later collides on display name",
			strings.Join(quoted(unused), ", ")),
	}}
}

// massPruneWarning calls attention to unusually large reconciliation plans
// without blocking them. Repository state remains authoritative: an
// intentional removal is valid, and reverting it restores the resources.
//
// CREATE and ADOPT are excluded from ownedBefore because this reference did
// not own those resources before the sync. Skip errored plans because the
// atomic apply persists none of their changes.
func massPruneWarning(reference string, items []report.OutcomeItem) []report.Diagnostic {
	if report.HasOutcomeErrors(items) {
		return nil
	}

	archived, ownedBefore, created := 0, 0, 0
	for _, item := range items {
		switch item.Action {
		case confidence.ActionArchive:
			archived++
			ownedBefore++
		case confidence.ActionUpdate, confidence.ActionUnchanged:
			ownedBefore++
		case confidence.ActionCreate:
			created++
		}
	}
	if archived < 5 || archived*2 < ownedBefore {
		return nil
	}

	message := fmt.Sprintf(
		"large prune plan: %d of %d resources currently owned by reference %q are in the archive/delete set — review the removals",
		archived, ownedBefore, reference)
	if archived == ownedBefore && created > 0 {
		message = fmt.Sprintf(
			"replacement-shaped prune plan: all %d resources currently owned by reference %q are in the archive/delete set while %d new resources are in the create set — verify that --source-reference is correct",
			archived, reference, created)
	}

	return []report.Diagnostic{{
		Severity: report.Warning,
		Rule:     "mass-prune",
		Message:  message,
	}}
}

func quoted(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, fmt.Sprintf("%q", v))
	}
	return out
}

// outcomeItems flattens a sync response (outcomes + archived) into the
// renderer's shape, deriving the resource kind from the resource name.
// ERROR outcomes for resources that were never matched or created carry no
// resource name; for those the kind falls back to what the request submitted
// under that display name.
func outcomeItems(req *confidence.ApplyMetricsSyncRequest, resp *confidence.ApplyMetricsSyncResponse) []report.OutcomeItem {
	requested := requestedKinds(req)
	items := make([]report.OutcomeItem, 0, len(resp.Outcomes)+len(resp.Archived))
	for _, o := range append(append([]confidence.ResourceOutcome{}, resp.Outcomes...), resp.Archived...) {
		kind := kindFromResourceName(o.Name)
		if kind == "" {
			kind = requested[o.DisplayName]
		}
		items = append(items, report.OutcomeItem{
			Kind:              kind,
			Name:              o.Name,
			DisplayName:       o.DisplayName,
			Action:            o.Action,
			ChangedFields:     o.ChangedFields,
			Errors:            o.Errors,
			PreviousReference: o.PreviousReference,
		})
	}
	return items
}

// requestedKinds maps display names to the kind submitted in the request. A
// display name submitted under more than one kind maps to "" — ambiguous.
func requestedKinds(req *confidence.ApplyMetricsSyncRequest) map[string]string {
	kinds := map[string]string{}
	add := func(displayName, kind string) {
		if displayName == "" {
			return
		}
		if existing, ok := kinds[displayName]; ok && existing != kind {
			kinds[displayName] = ""
			return
		}
		kinds[displayName] = kind
	}
	for _, r := range req.Resources {
		switch {
		case r.Metric != nil:
			add(r.Metric.DisplayName, "metric")
		case r.Measurement != nil:
			add(r.Measurement.DisplayName, "measurement")
		case r.FactTable != nil:
			add(r.FactTable.DisplayName, "fact table")
		}
	}
	return kinds
}

func kindFromResourceName(name string) string {
	switch {
	case strings.HasPrefix(name, "metrics/"):
		return "metric"
	case strings.HasPrefix(name, "measurements/"):
		return "measurement"
	case strings.HasPrefix(name, "factTables/"):
		return "fact table"
	default:
		return ""
	}
}
