package plan

import (
	"context"
	"fmt"
	"strings"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/report"
)

// IdentityLookup resolves owner resource names in one go, returning only
// those that exist, keyed by name. A name absent from the result does not
// exist in the account.
type IdentityLookup func(ctx context.Context, names []string) (map[string]confidence.Identity, error)

// ownerRef is one owner reference in the desired state, with the source
// location to point a diagnostic at.
type ownerRef struct {
	owner string
	loc   Loc
}

// ownerRefs collects every owner reference in the desired state, in file
// order. Owners are optional; blank means "owned by the syncing client".
func ownerRefs(desired Desired) []ownerRef {
	var refs []ownerRef
	add := func(owner string, loc Loc) {
		if owner != "" {
			refs = append(refs, ownerRef{owner, loc})
		}
	}
	for _, d := range desired.FactTables {
		add(d.Def.Owner, d.Loc)
	}
	for _, d := range desired.Measurements {
		add(d.Def.Owner, d.Loc)
	}
	for _, d := range desired.Metrics {
		add(d.Owner, d.Loc)
	}
	return refs
}

// CheckOwners verifies that every owner named in the desired state exists.
//
// The server stores an owner after a resource-name SYNTAX parse only — a
// well-formed but wrong id ("identities/<typo>") is accepted silently and
// the resource ends up owned by nobody, with no error ever (C4S-1379). This
// is the check that turns that into a positioned finding at PR time.
//
// All distinct owners resolve in ONE lookup — a repo points many resources
// at a handful of teams, and owner is optional, so references far outnumber
// distinct owners. A lookup that fails outright is returned as an error
// rather than reported as missing owners: "we could not tell" must never
// render as "does not exist".
func CheckOwners(ctx context.Context, desired Desired, lookup IdentityLookup) ([]report.Diagnostic, error) {
	refs := ownerRefs(desired)
	if len(refs) == 0 {
		return nil, nil
	}

	seen := map[string]bool{}
	var distinct []string
	for _, r := range refs {
		if !isPinned(r.owner) {
			continue // friendly names are handled by ResolveOwners
		}
		if !seen[r.owner] {
			seen[r.owner] = true
			distinct = append(distinct, r.owner) // file order, for stable requests
		}
	}
	if len(distinct) == 0 {
		return nil, nil
	}
	found, err := lookup(ctx, distinct)
	if err != nil {
		return nil, err
	}

	var diags []report.Diagnostic
	for _, r := range refs {
		if !isPinned(r.owner) {
			continue
		}
		id, ok := found[r.owner]
		switch {
		case !ok:
			diags = append(diags, r.loc.diag(report.Error, "owner",
				fmt.Sprintf("owner %q does not exist in this Confidence account — the resource would be owned by nobody", r.owner)))
		case id.Deactivated:
			diags = append(diags, r.loc.diag(report.Warning, "owner",
				fmt.Sprintf("owner %q (%s) is deactivated", r.owner, describe(id))))
		}
	}
	return diags, nil
}

// describe names an identity the way a human would recognise it.
func describe(id confidence.Identity) string {
	switch {
	case id.Email != "":
		return id.Email
	case id.DisplayName != "":
		return id.DisplayName
	default:
		return id.Name
	}
}

// FriendlyLookup resolves friendly owner names (display names or emails)
// against IAM, returning candidates for each query.
type FriendlyLookup func(ctx context.Context, queries []string) ([]confidence.FriendlyLookupResult, error)

// isPinned reports whether an owner value is already a pinned identity
// resource name.
func isPinned(owner string) bool {
	return strings.HasPrefix(owner, "identities/")
}

// ResolveOwners checks all owner references for unpinned (friendly) names
// and produces Error diagnostics with paste-ready replacements. It never
// modifies the desired state — this is resolve-then-pin, not resolve-then-
// substitute.
//
// For each friendly name:
//   - Exactly 1 match: Error with paste-ready replacement
//   - 0 matches:       Error "no identity found"
//   - >1 matches:      Error listing all candidates
//
// Already-pinned owners (identities/...) are left for CheckOwners.
func ResolveOwners(ctx context.Context, desired Desired, lookup FriendlyLookup) ([]report.Diagnostic, error) {
	refs := ownerRefs(desired)
	if len(refs) == 0 {
		return nil, nil
	}

	// Collect distinct friendly (non-pinned) names, preserving order.
	seen := map[string]bool{}
	var distinct []string
	for _, r := range refs {
		if isPinned(r.owner) || seen[r.owner] {
			continue
		}
		seen[r.owner] = true
		distinct = append(distinct, r.owner)
	}
	if len(distinct) == 0 {
		return nil, nil
	}

	results, err := lookup(ctx, distinct)
	if err != nil {
		return nil, err
	}
	// Index results by query for O(1) lookup per ref.
	byQuery := map[string][]confidence.Identity{}
	for _, r := range results {
		byQuery[r.Query] = r.Candidates
	}

	var diags []report.Diagnostic
	for _, r := range refs {
		if isPinned(r.owner) {
			continue
		}
		candidates := byQuery[r.owner]
		switch len(candidates) {
		case 0:
			diags = append(diags, r.loc.diag(report.Error, "owner",
				fmt.Sprintf("no identity matching %q found in this Confidence account", r.owner)))
		case 1:
			c := candidates[0]
			diags = append(diags, r.loc.diag(report.Error, "owner",
				fmt.Sprintf("owner %q is not an identity resource name — did you mean:\n  owner: %s  # %s",
					r.owner, c.Name, describeCandidate(c))))
		default:
			var lines []string
			for _, c := range candidates {
				lines = append(lines, fmt.Sprintf("  owner: %s  # %s", c.Name, describeCandidate(c)))
			}
			diags = append(diags, r.loc.diag(report.Error, "owner",
				fmt.Sprintf("owner %q matches %d identities — pick one:\n%s",
					r.owner, len(candidates), strings.Join(lines, "\n"))))
		}
	}
	return diags, nil
}

// describeCandidate formats a candidate identity for the replacement hint.
func describeCandidate(id confidence.Identity) string {
	kind := friendlyKind(id)
	switch {
	case id.DisplayName != "" && id.Email != "":
		return fmt.Sprintf("%s <%s> (%s)", id.DisplayName, id.Email, kind)
	case id.Email != "":
		return fmt.Sprintf("%s (%s)", id.Email, kind)
	case id.DisplayName != "":
		return fmt.Sprintf("%s (%s)", id.DisplayName, kind)
	default:
		return kind
	}
}

func friendlyKind(id confidence.Identity) string {
	switch {
	case id.Group != "":
		return "group"
	case id.User != "":
		return "user"
	case id.ApiClient != "":
		return "api_client"
	case id.Service != "":
		return "service"
	case id.Agent != "":
		return "agent"
	case id.Everyone:
		return "everyone"
	default:
		return "identity"
	}
}
