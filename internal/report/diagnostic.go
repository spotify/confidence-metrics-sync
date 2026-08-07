// Package report defines the diagnostics model and output renderers.
package report

import (
	"cmp"
	"slices"
)

// Severity of a diagnostic.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
	Notice  Severity = "notice"
)

// Diagnostic is one finding, anchored to a position in a YAML file when known
// (Line/Col are 1-based; 0 means unknown).
type Diagnostic struct {
	File     string   `json:"file"`
	Line     int      `json:"line,omitempty"`
	Col      int      `json:"col,omitempty"`
	Severity Severity `json:"severity"`
	Rule     string   `json:"rule"`
	Message  string   `json:"message"`
}

// Sort orders diagnostics by file, then line, then column, then message.
func Sort(ds []Diagnostic) {
	slices.SortFunc(ds, func(a, b Diagnostic) int {
		if c := cmp.Compare(a.File, b.File); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Line, b.Line); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Col, b.Col); c != 0 {
			return c
		}
		return cmp.Compare(a.Message, b.Message)
	})
}

// HasErrors reports whether any diagnostic has Error severity.
func HasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == Error {
			return true
		}
	}
	return false
}
