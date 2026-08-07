package plan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/report"
)

// lookupFunc builds an IdentityLookup over a fixed account, returning only
// the requested names that exist — the shape the real client returns.
func lookupFunc(account map[string]confidence.Identity) IdentityLookup {
	return func(_ context.Context, names []string) (map[string]confidence.Identity, error) {
		found := map[string]confidence.Identity{}
		for _, n := range names {
			if id, ok := account[n]; ok {
				found[n] = id
			}
		}
		return found, nil
	}
}

func TestCheckOwnersFlagsMissingOwner(t *testing.T) {
	desired := desiredFixture(t)
	account := map[string]confidence.Identity{}
	for _, ref := range ownerRefs(desired) {
		account[ref.owner] = confidence.Identity{Name: ref.owner}
	}
	if len(account) == 0 {
		t.Fatal("fixture corpus has no owners — nothing to check")
	}
	// Drop one owner from the account: it is now a phantom.
	var missing string
	for name := range account {
		missing = name
		break
	}
	delete(account, missing)

	diags, err := CheckOwners(context.Background(), desired, lookupFunc(account))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.HasErrors(diags) {
		t.Fatalf("expected an error diagnostic for missing owner %q, got %v", missing, diags)
	}
	for _, d := range diags {
		if d.Severity != report.Error {
			continue
		}
		if !strings.Contains(d.Message, missing) {
			t.Errorf("diagnostic does not name the missing owner: %q", d.Message)
		}
		if d.File == "" || d.Line == 0 {
			t.Errorf("diagnostic is not positioned: %+v", d)
		}
	}
}

// owner is optional and most resources omit it — blank means "owned by the
// syncing client", which is not a lookup and must never be a finding.
func TestCheckOwnersIgnoresBlankOwners(t *testing.T) {
	desired := desiredFixture(t)
	for i := range desired.FactTables {
		desired.FactTables[i].Def.Owner = ""
	}
	for i := range desired.Measurements {
		desired.Measurements[i].Def.Owner = ""
	}
	for i := range desired.Metrics {
		desired.Metrics[i].Owner = ""
	}

	lookup := func(_ context.Context, names []string) (map[string]confidence.Identity, error) {
		t.Errorf("blank owners triggered a lookup for %q", names)
		return nil, nil
	}
	diags, err := CheckOwners(context.Background(), desired, lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("owner-less resources produced diagnostics: %v", diags)
	}
}

// A deactivated owner still exists — that is a warning, not a hard failure:
// erroring would break repos whose owner simply left the company.
func TestCheckOwnersWarnsOnDeactivatedOwner(t *testing.T) {
	desired := desiredFixture(t)
	account := map[string]confidence.Identity{}
	for _, ref := range ownerRefs(desired) {
		account[ref.owner] = confidence.Identity{Name: ref.owner, Email: "gone@example.com", Deactivated: true}
	}

	diags, err := CheckOwners(context.Background(), desired, lookupFunc(account))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.HasErrors(diags) {
		t.Fatalf("deactivated owner must not be an error: %v", diags)
	}
	if len(diags) == 0 {
		t.Fatal("expected a warning for a deactivated owner")
	}
	for _, d := range diags {
		if d.Severity != report.Warning {
			t.Errorf("severity = %q, want warning", d.Severity)
		}
		if !strings.Contains(d.Message, "deactivated") || !strings.Contains(d.Message, "gone@example.com") {
			t.Errorf("warning should name the identity: %q", d.Message)
		}
	}
}

// "We could not tell" must never render as "does not exist".
func TestCheckOwnersPropagatesLookupFailure(t *testing.T) {
	desired := desiredFixture(t)
	boom := errors.New("iam unavailable")
	lookup := func(_ context.Context, _ []string) (map[string]confidence.Identity, error) {
		return nil, boom
	}

	diags, err := CheckOwners(context.Background(), desired, lookup)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the lookup error, got %v", err)
	}
	if diags != nil {
		t.Errorf("expected no diagnostics when the lookup failed, got %v", diags)
	}
}

// Deduplication is asserted end-to-end in cmd (TestOwnerLookupsAreDeduplicated).

// --- ResolveOwners tests ---

// friendlyFunc builds a FriendlyLookup over a fixed mapping from query string
// to candidate list.
func friendlyFunc(mapping map[string][]confidence.Identity) FriendlyLookup {
	return func(_ context.Context, queries []string) ([]confidence.FriendlyLookupResult, error) {
		out := make([]confidence.FriendlyLookupResult, len(queries))
		for i, q := range queries {
			out[i] = confidence.FriendlyLookupResult{
				Query:      q,
				Candidates: mapping[q],
			}
		}
		return out, nil
	}
}

func TestResolveOwnersSkipsPinned(t *testing.T) {
	desired := desiredFixture(t) // all owners are identities/...
	lookup := func(_ context.Context, queries []string) ([]confidence.FriendlyLookupResult, error) {
		t.Errorf("pinned owners triggered a friendly lookup for %q", queries)
		return nil, nil
	}
	diags, err := ResolveOwners(context.Background(), desired, lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("pinned owners produced diagnostics: %v", diags)
	}
}

func TestResolveOwnersSingleMatch(t *testing.T) {
	desired := desiredFixture(t)
	// Replace one owner with a friendly name.
	desired.Measurements[0].Def.Owner = "Growth Team"

	mapping := map[string][]confidence.Identity{
		"Growth Team": {{
			Name:        "identities/abc123",
			DisplayName: "Growth Team",
			Email:       "growth@example.com",
			Group:       "groups/test",
		}},
	}
	diags, err := ResolveOwners(context.Background(), desired, friendlyFunc(mapping))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.HasErrors(diags) {
		t.Fatal("expected error diagnostic for friendly owner")
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "is not an identity resource name") &&
			strings.Contains(d.Message, "identities/abc123") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected paste-ready replacement with identities/abc123, got %v", diags)
	}
}

func TestResolveOwnersNoMatch(t *testing.T) {
	desired := desiredFixture(t)
	desired.Measurements[0].Def.Owner = "Unknown Squad"

	mapping := map[string][]confidence.Identity{} // no matches
	diags, err := ResolveOwners(context.Background(), desired, friendlyFunc(mapping))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.HasErrors(diags) {
		t.Fatal("expected error diagnostic for unknown friendly owner")
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "no identity matching") &&
			strings.Contains(d.Message, "Unknown Squad") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected no-match error, got %v", diags)
	}
}

func TestResolveOwnersAmbiguous(t *testing.T) {
	desired := desiredFixture(t)
	desired.Measurements[0].Def.Owner = "Growth Team"

	mapping := map[string][]confidence.Identity{
		"Growth Team": {
			{Name: "identities/abc", DisplayName: "Growth Team", Email: "growth@example.com", Group: "groups/test"},
			{Name: "identities/def", DisplayName: "Growth Team", Email: "growth-user@example.com", User: "users/test"},
		},
	}
	diags, err := ResolveOwners(context.Background(), desired, friendlyFunc(mapping))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.HasErrors(diags) {
		t.Fatal("expected error diagnostic for ambiguous owner")
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "matches 2 identities") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ambiguity error, got %v", diags)
	}
}

func TestResolveOwnersPropagatesLookupFailure(t *testing.T) {
	desired := desiredFixture(t)
	desired.Measurements[0].Def.Owner = "Growth Team"

	boom := errors.New("iam unavailable")
	lookup := func(_ context.Context, _ []string) ([]confidence.FriendlyLookupResult, error) {
		return nil, boom
	}
	diags, err := ResolveOwners(context.Background(), desired, lookup)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the lookup error, got %v", err)
	}
	if diags != nil {
		t.Errorf("expected no diagnostics when the lookup failed, got %v", diags)
	}
}

func TestResolveOwnersDeduplicates(t *testing.T) {
	desired := desiredFixture(t)
	// Set multiple resources to the same friendly name.
	desired.Measurements[0].Def.Owner = "Growth Team"
	for i := range desired.Metrics {
		desired.Metrics[i].Owner = "Growth Team"
	}

	var calledWith []string
	lookup := func(_ context.Context, queries []string) ([]confidence.FriendlyLookupResult, error) {
		calledWith = queries
		out := make([]confidence.FriendlyLookupResult, len(queries))
		for i, q := range queries {
			out[i] = confidence.FriendlyLookupResult{
				Query:      q,
				Candidates: []confidence.Identity{{Name: "identities/abc", DisplayName: q, Group: "groups/test"}},
			}
		}
		return out, nil
	}
	diags, err := ResolveOwners(context.Background(), desired, lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have deduplicated to one query.
	if len(calledWith) != 1 || calledWith[0] != "Growth Team" {
		t.Errorf("expected one deduplicated query, got %q", calledWith)
	}
	// But diagnostics should be emitted for every reference.
	if len(diags) < 2 {
		t.Errorf("expected diagnostics for every friendly reference, got %d", len(diags))
	}
}
