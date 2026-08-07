package report

import (
	"encoding/json"
	"io"
)

type jsonOutput struct {
	Diagnostics []Diagnostic  `json:"diagnostics"`
	Outcomes    []OutcomeItem `json:"outcomes,omitempty"`
	Summary     jsonSummary   `json:"summary"`
}

type jsonSummary struct {
	FilesChecked int `json:"files_checked"`
	Errors       int `json:"errors"`
	Warnings     int `json:"warnings"`
}

// JSON renders diagnostics (and apply/dry-run outcomes, when present) as a
// stable machine-readable document.
func JSON(w io.Writer, ds []Diagnostic, outcomes []OutcomeItem, filesChecked int) error {
	out := jsonOutput{
		Diagnostics: ds,
		Outcomes:    outcomes,
		Summary:     jsonSummary{FilesChecked: filesChecked},
	}
	if out.Diagnostics == nil {
		out.Diagnostics = []Diagnostic{}
	}
	for _, d := range ds {
		switch d.Severity {
		case Error:
			out.Summary.Errors++
		case Warning:
			out.Summary.Warnings++
		}
	}
	for _, o := range outcomes {
		if o.Action == "ERROR" {
			out.Summary.Errors++
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
