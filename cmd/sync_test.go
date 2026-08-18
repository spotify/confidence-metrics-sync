package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/report"
)

const fixtures = "../internal/validate/testdata/valid"

// fakeBackend serves IAM tokens, entities, ApplyMetricsSync, and fact table
// polling for command-level tests.
type fakeBackend struct {
	t            *testing.T
	lastSync     atomic.Pointer[confidence.ApplyMetricsSyncRequest]
	syncResponse confidence.ApplyMetricsSyncResponse
	tableStates  map[string][]string // resource name -> successive states
	tableCalls   map[string]int

	// list endpoint payloads (for export tests)
	listMetrics      []confidence.Metric
	listMeasurements []confidence.Measurement
	listFactTables   []confidence.FactTable

	// identity lookups: every owner exists unless named here, mirroring an
	// account where the YAML's ids are real.
	missingIdentities     map[string]bool
	deactivatedIdentities map[string]bool
	identityCalls         map[string]int
	identityRequests      int
	identityMu            sync.Mutex

	// Friendly-name resolution: maps a friendly query (display name or
	// email) to the identities the IAM would return.
	friendlyIdentities map[string][]confidence.Identity
}

// friendlyFilterQuery extracts the quoted query from a displayName: or email:
// Lucene filter. E.g. `displayName:"Growth Team" AND ...` → "Growth Team".
func friendlyFilterQuery(filter string) string {
	_, after, ok := strings.Cut(filter, `"`)
	if !ok {
		return ""
	}
	query, _, ok := strings.Cut(after, `"`)
	if !ok {
		return ""
	}
	return query
}

// identityFilterNames extracts every queried resource name from the Lucene
// filter the client sends — `name:"identities/a"` for one, or
// `name:("identities/a" OR "identities/b")` for a batch.
func identityFilterNames(filter string) []string {
	var names []string
	rest := filter
	for {
		_, after, ok := strings.Cut(rest, `"`)
		if !ok {
			return names
		}
		name, tail, ok := strings.Cut(after, `"`)
		if !ok {
			return names
		}
		names = append(names, name)
		rest = tail
	}
}

func startFake(t *testing.T, fb *fakeBackend) {
	t.Helper()
	fb.t = t
	fb.tableCalls = map[string]int{}
	fb.identityCalls = map[string]int{}

	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/identities" {
			fmt.Fprint(w, `{"accessToken": "tok", "expiresIn": "3600"}`)
			return
		}
		filter := r.URL.Query().Get("filter")
		fb.identityMu.Lock()
		fb.identityRequests++

		var out []confidence.Identity
		if strings.HasPrefix(filter, "displayName:") || strings.HasPrefix(filter, "email:") {
			// Friendly-name lookup: extract the quoted query from the filter.
			query := friendlyFilterQuery(filter)
			if ids, ok := fb.friendlyIdentities[query]; ok {
				out = append(out, ids...)
			}
		} else {
			// Resource-name lookup (existing path).
			names := identityFilterNames(filter)
			for _, name := range names {
				fb.identityCalls[name]++
				if fb.missingIdentities[name] {
					continue
				}
				out = append(out, confidence.Identity{
					Name:        name,
					DisplayName: "Owning Team",
					Email:       "team@example.com",
					Deactivated: fb.deactivatedIdentities[name],
				})
			}
		}
		fb.identityMu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"identities": out})
	}))
	t.Cleanup(iam.Close)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/entities":
			fmt.Fprint(w, `{"entities": [{"name": "entities/u1", "displayName": "user"}]}`)
		case r.URL.Path == "/v1/metrics" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"metrics": fb.listMetrics})
		case r.URL.Path == "/v1/measurements" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"measurements": fb.listMeasurements})
		case r.URL.Path == "/v1/factTables" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"factTables": fb.listFactTables})
		case r.URL.Path == "/v1/factTables:batchGet" && r.Method == http.MethodGet:
			requested := map[string]bool{}
			for _, name := range r.URL.Query()["names"] {
				requested[name] = true
			}
			var factTables []confidence.FactTable
			for _, ft := range fb.listFactTables {
				if requested[ft.Name] {
					factTables = append(factTables, ft)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"factTables": factTables})
		case r.URL.Path == "/v1/metrics:applyMetricsSync":
			var req confidence.ApplyMetricsSyncRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("bad sync request: %v", err)
			}
			fb.lastSync.Store(&req)
			_ = json.NewEncoder(w).Encode(fb.syncResponse)
		case strings.HasPrefix(r.URL.Path, "/v1/factTables/"):
			name := strings.TrimPrefix(r.URL.Path, "/v1/")
			states := fb.tableStates[name]
			i := fb.tableCalls[name]
			fb.tableCalls[name]++
			if i >= len(states) {
				i = len(states) - 1
			}
			state := states[i]
			resp := map[string]any{"name": name, "state": state}
			if state == confidence.TableStateFailed {
				resp["error"] = map[string]any{"message": "column 'ms_playd' not found"}
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)

	t.Setenv(confidence.EnvClientID, "test-id")
	t.Setenv(confidence.EnvClientSecret, "test-secret")
	t.Setenv(confidence.EnvMetricsURL, api.URL)
	t.Setenv(confidence.EnvIAMURL, iam.URL)
}

func TestValidateDryRunAgainstAPI(t *testing.T) {
	fb := &fakeBackend{syncResponse: confidence.ApplyMetricsSyncResponse{
		Outcomes: []confidence.ResourceOutcome{
			{Name: "factTables/f1", DisplayName: "Hourly Stream", Action: "CREATE"},
			{Name: "metrics/m1", DisplayName: "Minutes Played - Day 1", Action: "UPDATE", ChangedFields: []string{"description"}},
		},
		Archived: []confidence.ResourceOutcome{
			{Name: "metrics/m9", DisplayName: "Old Metric", Action: "ARCHIVE"},
		},
	}}
	startFake(t, fb)

	out, err := run(t, "validate", "--source-reference", "test-repo", fixtures)
	if err != nil {
		t.Fatalf("validate failed: %v\n%s", err, out)
	}

	req := fb.lastSync.Load()
	if req == nil {
		t.Fatal("ApplyMetricsSync was never called")
	}
	if !req.DryRun || req.Reference != "test-repo" {
		t.Fatalf("expected dryRun=true reference=test-repo, got %+v", req)
	}
	if len(req.Resources) != 27 {
		t.Fatalf("expected 27 resources in request, got %d", len(req.Resources))
	}
	for _, resource := range req.Resources {
		switch {
		case resource.FactTable != nil:
			if !strings.HasPrefix(resource.FactTable.Name, "factTables/") {
				t.Errorf("fact table name not propagated: %q", resource.FactTable.Name)
			}
		case resource.Measurement != nil:
			if !strings.HasPrefix(resource.Measurement.Name, "measurements/") || !strings.HasPrefix(resource.Measurement.FactTable, "factTables/") {
				t.Errorf("measurement names not propagated: %+v", resource.Measurement)
			}
		case resource.Metric != nil:
			if !strings.HasPrefix(resource.Metric.Name, "metrics/") || !strings.HasPrefix(resource.Metric.Measurement, "measurements/") {
				t.Errorf("metric names not propagated: %+v", resource.Metric)
			}
		}
	}
	for _, s := range []string{"DRY RUN", "Would create", "Hourly Stream", "Old Metric"} {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q:\n%s", s, out)
		}
	}
	// Created fact table + archived metric are different kinds — no rename hint.
	if strings.Contains(out, "possible resource-name change") {
		t.Errorf("cross-kind create+archive must not hint a rename:\n%s", out)
	}
}

func TestValidateSupportsExternalResourceReferences(t *testing.T) {
	dir := t.TempDir()
	definition := `measurements:
  - name: measurements/local-revenue
    display_name: Local Revenue
    fact_table: factTables/shared-events
    entity: user
    measure: revenue
    operation: sum

metrics:
  - name: metrics/external-measurement
    display_name: External Measurement Metric
    entity: user
    measurement: measurements/shared-measurement
`
	if err := os.WriteFile(filepath.Join(dir, "metrics.yaml"), []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
	fb := &fakeBackend{
		listFactTables: []confidence.FactTable{{
			Name: "factTables/shared-events", DisplayName: "Shared Events",
			Measures: []confidence.Measure{{
				DisplayName: "revenue", Column: &confidence.Column{Name: "revenue_usd"},
			}},
		}},
		syncResponse: confidence.ApplyMetricsSyncResponse{},
	}
	startFake(t, fb)

	out, err := run(t, "validate", "--source-reference", "test-repo", dir)
	if err != nil {
		t.Fatalf("validate failed: %v\n%s", err, out)
	}
	req := fb.lastSync.Load()
	if req == nil || len(req.Resources) != 2 {
		t.Fatalf("unexpected sync request: %+v", req)
	}
	measurement := req.Resources[0].Measurement
	if measurement == nil || measurement.FactTable != "factTables/shared-events" || measurement.TypeSpec.AverageMetricSpec.Measurement.Name != "revenue_usd" {
		t.Fatalf("external fact table reference was not resolved: %+v", measurement)
	}
	metric := req.Resources[1].Metric
	if metric == nil || metric.Measurement != "measurements/shared-measurement" {
		t.Fatalf("external measurement reference was not preserved: %+v", metric)
	}
}

func TestMassPruneWarnsWithoutFailingValidateOrSync(t *testing.T) {
	response := confidence.ApplyMetricsSyncResponse{
		Outcomes: []confidence.ResourceOutcome{
			{Name: "metrics/kept-1", DisplayName: "Kept 1", Action: "UNCHANGED"},
			{Name: "metrics/kept-2", DisplayName: "Kept 2", Action: "UPDATE"},
			{Name: "metrics/kept-3", DisplayName: "Kept 3", Action: "UNCHANGED"},
			{Name: "metrics/kept-4", DisplayName: "Kept 4", Action: "UNCHANGED"},
			{Name: "metrics/kept-5", DisplayName: "Kept 5", Action: "UNCHANGED"},
		},
		Archived: []confidence.ResourceOutcome{
			{Name: "factTables/old", DisplayName: "Old facts", Action: "ARCHIVE"},
			{Name: "measurements/old-1", DisplayName: "Old measurement 1", Action: "ARCHIVE"},
			{Name: "measurements/old-2", DisplayName: "Old measurement 2", Action: "ARCHIVE"},
			{Name: "metrics/old-1", DisplayName: "Old metric 1", Action: "ARCHIVE"},
			{Name: "metrics/old-2", DisplayName: "Old metric 2", Action: "ARCHIVE"},
		},
	}

	for _, command := range []string{"validate", "sync"} {
		t.Run(command, func(t *testing.T) {
			fb := &fakeBackend{syncResponse: response}
			startFake(t, fb)

			out, err := run(t, command, "--source-reference", "repoAwesome", fixtures)
			if err != nil {
				t.Fatalf("mass prune warning must not fail %s: %v\n%s", command, err, out)
			}
			for _, want := range []string{"warning[mass-prune]", "5 of 10", `reference "repoAwesome"`} {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestMassPruneWarningThresholdAndShape(t *testing.T) {
	items := func(archived, unchanged, created int) []report.OutcomeItem {
		var out []report.OutcomeItem
		for range archived {
			out = append(out, report.OutcomeItem{Action: confidence.ActionArchive})
		}
		for range unchanged {
			out = append(out, report.OutcomeItem{Action: confidence.ActionUnchanged})
		}
		for range created {
			out = append(out, report.OutcomeItem{Action: confidence.ActionCreate})
		}
		return out
	}

	tests := []struct {
		name                   string
		items                  []report.OutcomeItem
		warn                   bool
		wantReplacementMessage bool
	}{
		{name: "below absolute floor", items: items(4, 0, 20)},
		{name: "below half", items: items(5, 6, 0)},
		{name: "at threshold", items: items(5, 5, 0), warn: true},
		{name: "complete sunset", items: items(5, 0, 0), warn: true},
		{name: "complete replacement", items: items(5, 0, 7), warn: true, wantReplacementMessage: true},
		{name: "errored plan", items: append(items(8, 2, 0), report.OutcomeItem{Action: confidence.ActionError})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diagnostics := massPruneWarning("repoAwesome", tt.items)
			if got := len(diagnostics) == 1; got != tt.warn {
				t.Fatalf("warning present = %v, want %v: %+v", got, tt.warn, diagnostics)
			}
			if !tt.warn {
				return
			}
			gotReplacement := strings.Contains(diagnostics[0].Message, "replacement-shaped")
			if gotReplacement != tt.wantReplacementMessage {
				t.Errorf("replacement message = %v, want %v: %s", gotReplacement, tt.wantReplacementMessage, diagnostics[0].Message)
			}
		})
	}
}

func TestValidateHintsPossibleRename(t *testing.T) {
	fb := &fakeBackend{syncResponse: confidence.ApplyMetricsSyncResponse{
		Outcomes: []confidence.ResourceOutcome{
			{Name: "metrics/m2", DisplayName: "Minutes Played v2", Action: "CREATE"},
		},
		Archived: []confidence.ResourceOutcome{
			{Name: "metrics/m1", DisplayName: "Minutes Played", Action: "ARCHIVE"},
		},
	}}
	startFake(t, fb)

	out, err := run(t, "validate", "--source-reference", "test-repo", fixtures)
	if err != nil {
		t.Fatalf("validate failed: %v\n%s", err, out)
	}
	// A same-kind create+archive pair can indicate an accidental resource-name
	// edit, so the PR check must call it out.
	for _, s := range []string{
		"possible resource-name change",
		`creates metric "Minutes Played v2"`,
		`archives "Minutes Played"`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q:\n%s", s, out)
		}
	}
}

func TestValidateDryRunErrorOutcomeFails(t *testing.T) {
	fb := &fakeBackend{syncResponse: confidence.ApplyMetricsSyncResponse{
		Outcomes: []confidence.ResourceOutcome{
			{DisplayName: "Minutes Played - Day 1", Action: "ERROR", Errors: []string{"Metric is owned by another source"}},
		},
	}}
	startFake(t, fb)

	out, err := run(t, "validate", "--source-reference", "test-repo", fixtures)
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings for ERROR outcome, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "owned by another source") {
		t.Errorf("output missing server error:\n%s", out)
	}
	// The outcome has no resource name (nothing was created), so the kind
	// must fall back to what the request submitted under that display name.
	if !strings.Contains(out, `✗ metric "Minutes Played - Day 1"`) {
		t.Errorf("nameless ERROR outcome missing request-derived kind:\n%s", out)
	}
}

func TestOutcomeItemsKindFallsBackToRequest(t *testing.T) {
	req := &confidence.ApplyMetricsSyncRequest{Resources: []confidence.SyncResource{
		{Metric: &confidence.Metric{DisplayName: "Conversion"}},
		{FactTable: &confidence.FactTable{DisplayName: "Streams"}},
		{Metric: &confidence.Metric{DisplayName: "Twin"}},
		{Measurement: &confidence.Measurement{DisplayName: "Twin"}},
	}}
	resp := &confidence.ApplyMetricsSyncResponse{Outcomes: []confidence.ResourceOutcome{
		{Name: "metrics/m1", DisplayName: "Conversion", Action: "UPDATE"},
		{DisplayName: "Streams", Action: "ERROR", Errors: []string{"boom"}},
		{DisplayName: "Twin", Action: "ERROR", Errors: []string{"boom"}},
		{DisplayName: "Unrequested", Action: "ERROR", Errors: []string{"boom"}},
	}}

	kinds := map[string]string{}
	for _, it := range outcomeItems(req, resp) {
		kinds[it.DisplayName] = it.Kind
	}
	if kinds["Conversion"] != "metric" {
		t.Errorf("named outcome: kind = %q, want metric (from resource name)", kinds["Conversion"])
	}
	if kinds["Streams"] != "fact table" {
		t.Errorf("nameless outcome: kind = %q, want fact table (from request)", kinds["Streams"])
	}
	if kinds["Twin"] != "" {
		t.Errorf("ambiguous display name: kind = %q, want empty", kinds["Twin"])
	}
	if kinds["Unrequested"] != "" {
		t.Errorf("unknown display name: kind = %q, want empty", kinds["Unrequested"])
	}
}

func TestValidateRequiresSourceReference(t *testing.T) {
	fb := &fakeBackend{}
	startFake(t, fb)
	_, err := run(t, "validate", fixtures)
	if err == nil || errors.Is(err, errFindings) || !strings.Contains(err.Error(), "--source-reference") {
		t.Fatalf("expected source-reference usage error, got %v", err)
	}
}

func TestSyncAppliesAndPollsToActive(t *testing.T) {
	fb := &fakeBackend{
		syncResponse: confidence.ApplyMetricsSyncResponse{
			Outcomes: []confidence.ResourceOutcome{
				{Name: "factTables/f1", DisplayName: "Hourly Stream", Action: "CREATE"},
				{Name: "metrics/m1", DisplayName: "Minutes Played - Day 1", Action: "CREATE"},
			},
		},
		tableStates: map[string][]string{
			"factTables/f1": {confidence.TableStateCreating, confidence.TableStateActive},
		},
	}
	startFake(t, fb)
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = 5 * time.Second })

	out, err := run(t, "sync", "--source-reference", "test-repo", fixtures)
	if err != nil {
		t.Fatalf("sync failed: %v\n%s", err, out)
	}
	if req := fb.lastSync.Load(); req == nil || req.DryRun {
		t.Fatalf("expected real apply (dryRun=false), got %+v", req)
	}
	if fb.tableCalls["factTables/f1"] < 2 {
		t.Fatalf("expected polling until ACTIVE, got %d calls", fb.tableCalls["factTables/f1"])
	}
	for _, s := range []string{"Sync complete", "validated"} {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q:\n%s", s, out)
		}
	}
}

func TestSyncFailsOnFailedTable(t *testing.T) {
	fb := &fakeBackend{
		syncResponse: confidence.ApplyMetricsSyncResponse{
			Outcomes: []confidence.ResourceOutcome{
				{Name: "factTables/f1", DisplayName: "Hourly Stream", Action: "UPDATE", ChangedFields: []string{"sql"}},
			},
		},
		tableStates: map[string][]string{
			"factTables/f1": {confidence.TableStateUpdating, confidence.TableStateFailed},
		},
	}
	startFake(t, fb)
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = 5 * time.Second })

	out, err := run(t, "sync", "--source-reference", "test-repo", fixtures)
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings for FAILED table, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "column 'ms_playd' not found") {
		t.Errorf("output missing warehouse error:\n%s", out)
	}
}

func TestSyncPollsRestoredFactTable(t *testing.T) {
	fb := &fakeBackend{
		syncResponse: confidence.ApplyMetricsSyncResponse{
			Outcomes: []confidence.ResourceOutcome{
				{Name: "factTables/f1", DisplayName: "Hourly Stream", Action: "UPDATE", ChangedFields: []string{"state"}},
			},
		},
		tableStates: map[string][]string{
			"factTables/f1": {confidence.TableStateCreating, confidence.TableStateActive},
		},
	}
	startFake(t, fb)
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = 5 * time.Second })

	out, err := run(t, "sync", "--source-reference", "test-repo", fixtures)
	if err != nil {
		t.Fatalf("sync failed: %v\n%s", err, out)
	}
	if fb.tableCalls["factTables/f1"] < 2 {
		t.Fatalf("restored fact table must be polled until ACTIVE, got %d calls", fb.tableCalls["factTables/f1"])
	}
	if !strings.Contains(out, `✓ "Hourly Stream" validated`) {
		t.Errorf("output missing restored fact-table validation:\n%s", out)
	}
}

func TestSyncPollsTablesInParallelAndReportsAll(t *testing.T) {
	fb := &fakeBackend{
		syncResponse: confidence.ApplyMetricsSyncResponse{
			Outcomes: []confidence.ResourceOutcome{
				{Name: "factTables/ok", DisplayName: "Good Table", Action: "CREATE"},
				{Name: "factTables/bad", DisplayName: "Bad Table", Action: "CREATE"},
			},
		},
		tableStates: map[string][]string{
			"factTables/ok":  {confidence.TableStateCreating, confidence.TableStateActive},
			"factTables/bad": {confidence.TableStateCreating, confidence.TableStateFailed},
		},
	}
	startFake(t, fb)
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = 5 * time.Second })

	out, err := run(t, "sync", "--source-reference", "test-repo", fixtures)
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings when any table fails, got %v\n%s", err, out)
	}
	// Both tables were polled to completion and both results reported —
	// one failure must not short-circuit the other's verdict.
	if !strings.Contains(out, `✓ "Good Table" validated`) {
		t.Errorf("missing success line for the healthy table:\n%s", out)
	}
	if !strings.Contains(out, `✗ "Bad Table"`) {
		t.Errorf("missing failure line for the broken table:\n%s", out)
	}
}

func TestSyncUnchangedFieldsSkipPolling(t *testing.T) {
	fb := &fakeBackend{
		syncResponse: confidence.ApplyMetricsSyncResponse{
			Outcomes: []confidence.ResourceOutcome{
				// description-only update: no warehouse re-validation, no polling
				{Name: "factTables/f1", DisplayName: "Hourly Stream", Action: "UPDATE", ChangedFields: []string{"description"}},
			},
		},
	}
	startFake(t, fb)

	out, err := run(t, "sync", "--source-reference", "test-repo", fixtures)
	if err != nil {
		t.Fatalf("sync failed: %v\n%s", err, out)
	}
	if fb.tableCalls["factTables/f1"] != 0 {
		t.Fatalf("description-only update must not poll, got %d calls", fb.tableCalls["factTables/f1"])
	}
}

func TestSyncAdoptFromAPI(t *testing.T) {
	fb := &fakeBackend{
		syncResponse: confidence.ApplyMetricsSyncResponse{
			Outcomes: []confidence.ResourceOutcome{
				// source-only adoption: ownership flip, no warehouse re-validation
				{Name: "factTables/f1", DisplayName: "Hourly Stream", Action: "ADOPT", ChangedFields: []string{"source"}, PreviousReference: "api"},
				{Name: "metrics/m1", DisplayName: "Minutes Played - Day 1", Action: "ADOPT", ChangedFields: []string{"source", "description"}, PreviousReference: "api"},
			},
		},
	}
	startFake(t, fb)

	out, err := run(t, "sync", "--source-reference", "test-repo", "--adopt-from", "api", fixtures)
	if err != nil {
		t.Fatalf("sync --adopt-from failed: %v\n%s", err, out)
	}

	req := fb.lastSync.Load()
	if req == nil || len(req.AdoptFrom) != 1 || req.AdoptFrom[0] != "api" || req.DryRun {
		t.Fatalf("expected adoptFrom=[api] dryRun=false in request, got %+v", req)
	}
	for _, s := range []string{
		"Adopted:",
		`» fact table "Hourly Stream" from api (source)`,
		`» metric "Minutes Played - Day 1" from api (source, description)`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q:\n%s", s, out)
		}
	}
	// The flag did take something over, so it must not be reported as unused.
	if strings.Contains(out, "adopted nothing") {
		t.Errorf("a flag that adopted resources must not warn:\n%s", out)
	}
	// A source-only fact table adoption must not trigger warehouse polling.
	if fb.tableCalls["factTables/f1"] != 0 {
		t.Fatalf("source-only adoption must not poll, got %d calls", fb.tableCalls["factTables/f1"])
	}
}

// Taking a resource from another repository must not read like claiming an
// unowned one — the plan names the repository losing it.
func TestSyncAdoptFromAnotherRepositoryNamesIt(t *testing.T) {
	const from = "github.com/example/other-metrics"
	fb := &fakeBackend{
		syncResponse: confidence.ApplyMetricsSyncResponse{
			Outcomes: []confidence.ResourceOutcome{
				{Name: "metrics/m1", DisplayName: "Minutes Played - Day 1", Action: "ADOPT", ChangedFields: []string{"source"}, PreviousReference: from},
			},
		},
	}
	startFake(t, fb)

	out, err := run(t, "sync", "--source-reference", "test-repo", "--adopt-from", from, fixtures)
	if err != nil {
		t.Fatalf("sync --adopt-from failed: %v\n%s", err, out)
	}

	req := fb.lastSync.Load()
	if req == nil || len(req.AdoptFrom) != 1 || req.AdoptFrom[0] != from {
		t.Fatalf("expected adoptFrom=[%s], got %+v", from, req)
	}
	if !strings.Contains(out, `» metric "Minutes Played - Day 1" from `+from+" (source)") {
		t.Errorf("adoption did not name the previous owner:\n%s", out)
	}
}

// Adoption is a one-time step, so an entry that took nothing over is either
// spent or wrong — and left standing it silently claims a future collision.
func TestUnusedAdoptFromWarnsWithoutFailing(t *testing.T) {
	const unused = "github.com/example/other-metrics"
	fb := &fakeBackend{
		syncResponse: confidence.ApplyMetricsSyncResponse{
			Outcomes: []confidence.ResourceOutcome{
				{Name: "metrics/m1", DisplayName: "Minutes Played - Day 1", Action: "ADOPT", ChangedFields: []string{"source"}, PreviousReference: "api"},
			},
		},
	}
	startFake(t, fb)

	out, err := run(t, "sync", "--source-reference", "test-repo",
		"--adopt-from", "api", "--adopt-from", unused, fixtures)
	if err != nil {
		t.Fatalf("an unused --adopt-from is a warning, not a failure: %v\n%s", err, out)
	}
	if !strings.Contains(out, unused) || !strings.Contains(out, "adopted nothing") {
		t.Errorf("expected a warning naming %q:\n%s", unused, out)
	}
	// Only the spent entry is named — "api" did adopt something.
	if strings.Contains(out, `"api" adopted nothing`) {
		t.Errorf("warned about an entry that did adopt:\n%s", out)
	}
}

func TestAdoptFromRejectsOwnSourceReference(t *testing.T) {
	fb := &fakeBackend{}
	startFake(t, fb)

	out, err := run(t, "sync", "--source-reference", "test-repo", "--adopt-from", "test-repo", fixtures)
	if err == nil {
		t.Fatalf("expected a usage error, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "own --source-reference") {
		t.Errorf("unhelpful error: %v", err)
	}
	if fb.lastSync.Load() != nil {
		t.Error("a request was sent despite the invalid flag combination")
	}
}

func TestReservedAPISourceReferenceIsRejected(t *testing.T) {
	fb := &fakeBackend{}
	startFake(t, fb)

	out, err := run(t, "sync", "--source-reference", "api", fixtures)
	if err == nil {
		t.Fatalf("expected a usage error, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "reserved for --adopt-from") {
		t.Errorf("unhelpful error: %v", err)
	}
	if fb.lastSync.Load() != nil {
		t.Error("a request was sent despite the reserved reference")
	}
}

func TestSyncAdoptWithSchemaChangePolls(t *testing.T) {
	fb := &fakeBackend{
		syncResponse: confidence.ApplyMetricsSyncResponse{
			Outcomes: []confidence.ResourceOutcome{
				// adoption that also changes the SQL: warehouse re-validates,
				// so the CLI must poll — same rule as UPDATE
				{Name: "factTables/f1", DisplayName: "Hourly Stream", Action: "ADOPT", ChangedFields: []string{"source", "sql"}, PreviousReference: "api"},
			},
		},
		tableStates: map[string][]string{
			"factTables/f1": {confidence.TableStateActive},
		},
	}
	startFake(t, fb)

	out, err := run(t, "sync", "--source-reference", "test-repo", "--adopt-from", "api", fixtures)
	if err != nil {
		t.Fatalf("sync --adopt-from failed: %v\n%s", err, out)
	}
	if fb.tableCalls["factTables/f1"] == 0 {
		t.Fatal("schema-changing adoption must poll warehouse validation")
	}
}

func TestValidateAdoptFromDryRun(t *testing.T) {
	fb := &fakeBackend{
		syncResponse: confidence.ApplyMetricsSyncResponse{
			Outcomes: []confidence.ResourceOutcome{
				{Name: "metrics/m1", DisplayName: "Minutes Played - Day 1", Action: "ADOPT", ChangedFields: []string{"source"}, PreviousReference: "api"},
			},
		},
	}
	startFake(t, fb)

	out, err := run(t, "validate", "--source-reference", "test-repo", "--adopt-from", "api", fixtures)
	if err != nil {
		t.Fatalf("validate --adopt-from failed: %v\n%s", err, out)
	}

	req := fb.lastSync.Load()
	if req == nil || len(req.AdoptFrom) != 1 || req.AdoptFrom[0] != "api" || !req.DryRun {
		t.Fatalf("expected adoptFrom=[api] dryRun=true in request, got %+v", req)
	}
	if !strings.Contains(out, "Would adopt:") {
		t.Errorf("dry-run output missing \"Would adopt:\":\n%s", out)
	}
}

// --- Owner existence (C4S-1379) ---

// The server stores an owner after a syntax parse only, so a well-formed but
// wrong id would silently leave the resource owned by nobody. validate must
// catch it at PR time, positioned.
func TestValidateRejectsNonExistentOwner(t *testing.T) {
	fb := &fakeBackend{missingIdentities: map[string]bool{"identities/abc123": true}}
	startFake(t, fb)

	out, err := run(t, "validate", "--source-reference", "test-repo", fixtures)
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings for a phantom owner, got %v\n%s", err, out)
	}
	if !strings.Contains(out, `owner "identities/abc123" does not exist`) {
		t.Errorf("output missing the owner error:\n%s", out)
	}
	if !strings.Contains(out, "streaming.yaml:") {
		t.Errorf("owner error is not anchored to the source file:\n%s", out)
	}
}

// The apply is atomic, so catching a phantom owner in sync means nothing is
// written at all — assert the sync request was never sent.
func TestSyncRejectsNonExistentOwnerBeforeWriting(t *testing.T) {
	fb := &fakeBackend{missingIdentities: map[string]bool{"identities/abc123": true}}
	startFake(t, fb)

	out, err := run(t, "sync", "--source-reference", "test-repo", fixtures)
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings for a phantom owner, got %v\n%s", err, out)
	}
	if fb.lastSync.Load() != nil {
		t.Error("sync sent an ApplyMetricsSync request despite an unknown owner")
	}
}

// A deactivated owner still exists: warn, but do not fail the build —
// erroring would break every repo whose owner has left the company.
func TestValidateWarnsOnDeactivatedOwner(t *testing.T) {
	fb := &fakeBackend{
		deactivatedIdentities: map[string]bool{"identities/abc123": true},
		syncResponse:          confidence.ApplyMetricsSyncResponse{},
	}
	startFake(t, fb)

	out, err := run(t, "validate", "--source-reference", "test-repo", fixtures)
	if err != nil {
		t.Fatalf("a deactivated owner must not fail validation: %v\n%s", err, out)
	}
	if !strings.Contains(out, "is deactivated") {
		t.Errorf("output missing the deactivation warning:\n%s", out)
	}
}

// Every distinct owner resolves in ONE request, and each is asked for once —
// references far outnumber distinct owners in a real repo.
func TestOwnerLookupsShareOneRequest(t *testing.T) {
	fb := &fakeBackend{}
	startFake(t, fb)

	if _, err := run(t, "validate", "--source-reference", "test-repo", fixtures); err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	fb.identityMu.Lock()
	defer fb.identityMu.Unlock()
	if len(fb.identityCalls) == 0 {
		t.Fatal("no identity lookups were made")
	}
	if fb.identityRequests != 1 {
		t.Errorf("owners resolved in %d requests, want 1", fb.identityRequests)
	}
	for name, n := range fb.identityCalls {
		if n != 1 {
			t.Errorf("owner %q asked for %d times, want 1", name, n)
		}
	}
}

// --offline makes no server calls at all, so owner lookups do not happen —
// and the notice already tells the reader the credentialed tier was skipped.
func TestOfflineValidateSkipsOwnerLookups(t *testing.T) {
	fb := &fakeBackend{}
	startFake(t, fb)

	out, err := run(t, "validate", "--offline", fixtures)
	if err != nil {
		t.Fatalf("offline validate failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "offline mode") {
		t.Errorf("offline run is not announced:\n%s", out)
	}
	fb.identityMu.Lock()
	defer fb.identityMu.Unlock()
	if fb.identityRequests != 0 {
		t.Errorf("offline validate contacted IAM: %d requests", fb.identityRequests)
	}
}

// --- Friendly-name owner resolution (C4S-1398) ---

// friendlyOwnerFixture writes a minimal YAML file with a friendly (non-pinned)
// owner and returns the directory path.
func friendlyOwnerFixture(t *testing.T, owner string) string {
	t.Helper()
	dir := t.TempDir()
	yaml := fmt.Sprintf(`fact_tables:
  - name: factTables/events
    display_name: Events
    table: a.b.c
    timestamp_column: t
    entities:
      - entity: user
        column: user_id
    measures:
      - display_name: m
        column: c

measurements:
  - name: measurements/test-measurement
    display_name: Test Measurement
    fact_table: factTables/events
    entity: user
    owner: %s
    measure: m
    operation: sum
    metrics:
      - name: metrics/test-metric
        display_name: Test Metric
`, owner)
	if err := os.WriteFile(filepath.Join(dir, "metrics.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidateResolvesSingleFriendlyOwner(t *testing.T) {
	fb := &fakeBackend{
		friendlyIdentities: map[string][]confidence.Identity{
			"Growth Team": {{
				Name:        "identities/abc123",
				DisplayName: "Growth Team",
				Email:       "growth@example.com",
				Group:       "groups/test",
			}},
		},
		syncResponse: confidence.ApplyMetricsSyncResponse{},
	}
	startFake(t, fb)

	dir := friendlyOwnerFixture(t, "Growth Team")
	out, err := run(t, "validate", "--source-reference", "test-repo", dir)
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings for a friendly owner, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "is not an identity resource name") {
		t.Errorf("output missing the pin instruction:\n%s", out)
	}
	if !strings.Contains(out, "identities/abc123") {
		t.Errorf("output missing the resolved identity:\n%s", out)
	}
}

func TestValidateRejectsAmbiguousFriendlyOwner(t *testing.T) {
	fb := &fakeBackend{
		friendlyIdentities: map[string][]confidence.Identity{
			"Growth Team": {
				{Name: "identities/abc", DisplayName: "Growth Team", Email: "growth-group@example.com", Group: "groups/test"},
				{Name: "identities/def", DisplayName: "Growth Team", Email: "growth-user@example.com", User: "users/test"},
			},
		},
		syncResponse: confidence.ApplyMetricsSyncResponse{},
	}
	startFake(t, fb)

	dir := friendlyOwnerFixture(t, "Growth Team")
	out, err := run(t, "validate", "--source-reference", "test-repo", dir)
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings for ambiguous owner, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "matches 2 identities") {
		t.Errorf("output missing the ambiguity error:\n%s", out)
	}
}

func TestValidateRejectsUnknownFriendlyOwner(t *testing.T) {
	fb := &fakeBackend{
		friendlyIdentities: map[string][]confidence.Identity{}, // no matches
		syncResponse:       confidence.ApplyMetricsSyncResponse{},
	}
	startFake(t, fb)

	dir := friendlyOwnerFixture(t, "Unknown Squad")
	out, err := run(t, "validate", "--source-reference", "test-repo", dir)
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings for unknown owner, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "no identity matching") {
		t.Errorf("output missing the no-match error:\n%s", out)
	}
}

func TestSyncRejectsFriendlyOwnerBeforeWriting(t *testing.T) {
	fb := &fakeBackend{
		friendlyIdentities: map[string][]confidence.Identity{
			"Growth Team": {{
				Name:        "identities/abc123",
				DisplayName: "Growth Team",
				Group:       "groups/test",
			}},
		},
		syncResponse: confidence.ApplyMetricsSyncResponse{},
	}
	startFake(t, fb)

	dir := friendlyOwnerFixture(t, "Growth Team")
	out, err := run(t, "sync", "--source-reference", "test-repo", dir)
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings for a friendly owner, got %v\n%s", err, out)
	}
	if fb.lastSync.Load() != nil {
		t.Error("sync sent an ApplyMetricsSync request despite an unpinned owner")
	}
}

// Friendly names in --offline mode: they pass schema now (pattern removed),
// and tier 2 is skipped, so the file validates cleanly. Resolution errors
// only surface with credentials.
func TestOfflineFriendlyOwnerPassesSchema(t *testing.T) {
	dir := friendlyOwnerFixture(t, "Growth Team")
	out, err := run(t, "validate", "--offline", dir)
	if err != nil {
		t.Fatalf("offline validate with friendly owner should pass schema: %v\n%s", err, out)
	}
}
