package report

import (
	"fmt"
	"io"
	"strings"
)

// OutcomeItem is one per-resource result from ApplyMetricsSync, decoupled
// from the wire type so this package stays dependency-free.
type OutcomeItem struct {
	Kind          string   `json:"kind"` // "metric" | "measurement" | "fact table" | ""
	Name          string   `json:"name,omitempty"`
	DisplayName   string   `json:"display_name"`
	Action        string   `json:"action"` // CREATE | UPDATE | ADOPT | UNCHANGED | ARCHIVE | ERROR
	ChangedFields []string `json:"changed_fields,omitempty"`
	Errors        []string `json:"errors,omitempty"`
	// PreviousReference is the source an ADOPT took the resource over from.
	PreviousReference string `json:"previous_reference,omitempty"`
}

// HasOutcomeErrors reports whether any outcome is an ERROR.
func HasOutcomeErrors(items []OutcomeItem) bool {
	for _, it := range items {
		if it.Action == "ERROR" {
			return true
		}
	}
	return false
}

// Outcomes renders the apply/dry-run result in the design doc's summary
// format, then lists individual changes and errors.
func Outcomes(w io.Writer, items []OutcomeItem, dryRun bool) {
	if dryRun {
		fmt.Fprintln(w, "\nDRY RUN — no changes made:")
	} else {
		fmt.Fprintln(w, "\nSync complete:")
	}
	verb := map[bool][2]string{true: {"Would create", "Would archive"}, false: {"Created", "Archived"}}[dryRun]

	fmt.Fprintf(w, "  %-14s %s\n", verb[0]+":", countByKind(items, "CREATE"))
	fmt.Fprintf(w, "  %-14s %s\n", map[bool]string{true: "Would update:", false: "Updated:"}[dryRun], countByKind(items, "UPDATE"))
	fmt.Fprintf(w, "  %-14s %s\n", verb[1]+":", countByKind(items, "ARCHIVE"))
	// Adoption is an explicit ownership takeover — only surfaced when present.
	if count(items, "ADOPT") > 0 {
		fmt.Fprintf(w, "  %-14s %s\n", map[bool]string{true: "Would adopt:", false: "Adopted:"}[dryRun], countByKind(items, "ADOPT"))
	}
	fmt.Fprintf(w, "  %-14s %s\n", "Unchanged:", countByKind(items, "UNCHANGED"))
	fmt.Fprintf(w, "  %-14s %d\n", "Errors:", count(items, "ERROR"))

	for _, it := range items {
		switch it.Action {
		case "CREATE":
			fmt.Fprintf(w, "  + %s %q\n", it.Kind, it.DisplayName)
		case "UPDATE":
			fmt.Fprintf(w, "  ~ %s %q (%s)\n", it.Kind, it.DisplayName, strings.Join(it.ChangedFields, ", "))
		case "ADOPT":
			// Name the previous owner: taking a resource from another
			// repository must never read the same as claiming an unowned one.
			from := ""
			if it.PreviousReference != "" {
				from = fmt.Sprintf(" from %s", it.PreviousReference)
			}
			fmt.Fprintf(w, "  » %s %q%s (%s)\n", it.Kind, it.DisplayName, from, strings.Join(it.ChangedFields, ", "))
		case "ARCHIVE":
			fmt.Fprintf(w, "  - %s %q\n", it.Kind, it.DisplayName)
		}
	}
	for _, it := range items {
		if it.Action == "ERROR" {
			for _, e := range it.Errors {
				fmt.Fprintf(w, "  ✗ %s %q: %s\n", it.Kind, it.DisplayName, e)
			}
			if len(it.Errors) == 0 {
				fmt.Fprintf(w, "  ✗ %s %q: unspecified error\n", it.Kind, it.DisplayName)
			}
		}
	}
	for _, hint := range renameHints(items) {
		fmt.Fprintf(w, "  ⚠ %s\n", hint)
	}
}

// renameHints flags same-kind create+archive pairs in one plan. Display
// names are identity, so a rename in the YAML is a create + archive: the
// new resource starts with a fresh ID and empty history, and existing
// references keep pointing at the archived original. That is easy to do
// by accident and impossible to see from the summary counts alone.
func renameHints(items []OutcomeItem) []string {
	created := map[string][]string{}
	archived := map[string][]string{}
	order := []string{}
	for _, it := range items {
		kind := it.Kind
		if kind == "" {
			kind = "resource"
		}
		switch it.Action {
		case "CREATE":
			if len(created[kind]) == 0 && len(archived[kind]) == 0 {
				order = append(order, kind)
			}
			created[kind] = append(created[kind], it.DisplayName)
		case "ARCHIVE":
			if len(created[kind]) == 0 && len(archived[kind]) == 0 {
				order = append(order, kind)
			}
			archived[kind] = append(archived[kind], it.DisplayName)
		}
	}
	var hints []string
	for _, kind := range order {
		if len(created[kind]) == 0 || len(archived[kind]) == 0 {
			continue
		}
		hints = append(hints, fmt.Sprintf(
			"possible rename: this plan creates %s %s and archives %s. Display names are identity — a rename creates a new resource (fresh ID, no history) and archives the old one, and existing references keep pointing at the archived resource. If that is intended, ignore this warning.",
			kind, quotedList(created[kind]), quotedList(archived[kind])))
	}
	return hints
}

func quotedList(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, fmt.Sprintf("%q", n))
	}
	return strings.Join(quoted, ", ")
}

func count(items []OutcomeItem, action string) int {
	n := 0
	for _, it := range items {
		if it.Action == action {
			n++
		}
	}
	return n
}

func countByKind(items []OutcomeItem, action string) string {
	counts := map[string]int{}
	order := []string{}
	total := 0
	for _, it := range items {
		if it.Action != action {
			continue
		}
		total++
		kind := it.Kind
		if kind == "" {
			kind = "resource"
		}
		if counts[kind] == 0 {
			order = append(order, kind)
		}
		counts[kind]++
	}
	if total == 0 {
		return "0"
	}
	parts := make([]string, 0, len(order))
	for _, kind := range order {
		parts = append(parts, fmt.Sprintf("%d %ss", counts[kind], kind))
	}
	return strings.Join(parts, ", ")
}
