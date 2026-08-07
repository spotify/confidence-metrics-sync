package report

import (
	"fmt"
	"io"
)

// Text renders diagnostics in a compact, grep-able format:
//
//	metrics/a.yaml:12:5: error[unknown-measure]: measurement "x" aggregates ...
//
// followed by a one-line summary. Returns nothing; callers decide exit codes
// via HasErrors.
func Text(w io.Writer, ds []Diagnostic, filesChecked int) {
	for _, d := range ds {
		switch {
		case d.Line > 0 && d.Col > 0:
			fmt.Fprintf(w, "%s:%d:%d: %s[%s]: %s\n", d.File, d.Line, d.Col, d.Severity, d.Rule, d.Message)
		case d.Line > 0:
			fmt.Fprintf(w, "%s:%d: %s[%s]: %s\n", d.File, d.Line, d.Severity, d.Rule, d.Message)
		default:
			fmt.Fprintf(w, "%s: %s[%s]: %s\n", d.File, d.Severity, d.Rule, d.Message)
		}
	}

	errs, warns := 0, 0
	for _, d := range ds {
		switch d.Severity {
		case Error:
			errs++
		case Warning:
			warns++
		}
	}
	switch {
	case errs == 0 && warns == 0:
		fmt.Fprintf(w, "✓ %d file(s) valid\n", filesChecked)
	case errs == 0:
		fmt.Fprintf(w, "✓ %d file(s) valid, %d warning(s)\n", filesChecked, warns)
	default:
		fmt.Fprintf(w, "✗ %d error(s), %d warning(s) in %d file(s)\n", errs, warns, filesChecked)
	}
}
