// Package validate runs the two validation layers over parsed metric
// definition files: structural (JSON Schema) and semantic (cross-reference
// checks in Go). Friendly pre-checks run before schema validation for the
// mistakes whose raw oneOf errors would be unreadable.
package validate

import (
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/spotify/confidence-metrics-sync/internal/parser"
	"github.com/spotify/confidence-metrics-sync/internal/report"
	"github.com/spotify/confidence-metrics-sync/internal/schema"
)

// printer renders jsonschema error kinds as English text.
var printer = message.NewPrinter(language.English)

// Files validates all files and returns the combined, sorted diagnostics.
func Files(files []*parser.File) ([]report.Diagnostic, error) {
	compiled, err := schema.Compile()
	if err != nil {
		return nil, fmt.Errorf("compiling bundled schema: %w", err)
	}

	var diags []report.Diagnostic
	for _, f := range files {
		pre := preChecks(f)
		diags = append(diags, pre...)
		diags = append(diags, structural(compiled, f, pre)...)
	}
	diags = append(diags, semantic(files)...)
	report.Sort(diags)
	return diags, nil
}

// preChecks reports the two exclusivity mistakes with plain-English messages
// BEFORE schema validation (whose oneOf output for them is unreadable).
func preChecks(f *parser.File) []report.Diagnostic {
	var diags []report.Diagnostic
	if len(f.Def.FactTables) == 0 && len(f.Def.Measurements) == 0 && len(f.Def.Metrics) == 0 {
		pos := f.Position("")
		diags = append(diags, report.Diagnostic{
			File: f.Path, Line: pos.Line, Col: pos.Col,
			Severity: report.Warning, Rule: "no-definitions",
			Message: "file defines no fact tables, measurements, or metrics",
		})
	}
	for i, ft := range f.Def.FactTables {
		ptr := fmt.Sprintf("/fact_tables/%d", i)
		hasSQL, hasTable := ft.SQL != "", ft.Table != ""
		switch {
		case hasSQL && hasTable:
			diags = append(diags, diag(f, ptr, "source-exclusive",
				fmt.Sprintf("fact table %q sets both `sql` and `table` — use exactly one source", ft.DisplayName)))
		case !hasSQL && !hasTable:
			diags = append(diags, diag(f, ptr, "source-exclusive",
				fmt.Sprintf("fact table %q needs a source: set either `sql` or `table`", ft.DisplayName)))
		}
	}
	for i, m := range f.Def.Measurements {
		ptr := fmt.Sprintf("/measurements/%d", i)
		simple := m.Measure != "" || m.Operation != ""
		ratio := m.Numerator != nil || m.Denominator != nil
		switch {
		case simple && ratio:
			diags = append(diags, diag(f, ptr, "aggregation-exclusive",
				fmt.Sprintf("measurement %q mixes simple (`measure`/`operation`) and ratio (`numerator`/`denominator`) aggregation — use one or the other", m.DisplayName)))
		case !simple && !ratio:
			diags = append(diags, diag(f, ptr, "aggregation-exclusive",
				fmt.Sprintf("measurement %q needs an aggregation: set `measure`+`operation`, or `numerator`+`denominator`", m.DisplayName)))
		case simple && m.Operation == "":
			diags = append(diags, diag(f, ptr, "aggregation-exclusive",
				fmt.Sprintf("measurement %q needs an `operation` for simple aggregation", m.DisplayName)))
		case simple && m.Measure == "" && m.Operation != "count":
			// count aggregates facts and needs no measure column.
			diags = append(diags, diag(f, ptr, "aggregation-exclusive",
				fmt.Sprintf("measurement %q needs a `measure` for operation %q (only `count` works without one)", m.DisplayName, m.Operation)))
		case ratio && (m.Numerator == nil || m.Denominator == nil):
			diags = append(diags, diag(f, ptr, "aggregation-exclusive",
				fmt.Sprintf("measurement %q needs both `numerator` and `denominator` for ratio aggregation", m.DisplayName)))
		}
	}
	return diags
}

// structural validates one file against the JSON Schema and converts leaf
// errors to positioned diagnostics. Errors under instance locations already
// covered by pre-checks are suppressed.
func structural(compiled *jsonschema.Schema, f *parser.File, pre []report.Diagnostic) []report.Diagnostic {
	err := compiled.Validate(f.Generic)
	if err == nil {
		return nil
	}
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []report.Diagnostic{{
			File: f.Path, Severity: report.Error, Rule: "schema", Message: err.Error(),
		}}
	}

	covered := map[string]bool{}
	for _, d := range pre {
		covered[fmt.Sprintf("%s:%d:%d", d.File, d.Line, d.Col)] = true
	}

	var diags []report.Diagnostic
	seen := map[string]bool{}
	for _, unit := range flatten(ve) {
		d := diag(f, unit.pointer, "schema",
			fmt.Sprintf("%s: %s", parser.FmtPointer(unit.pointer), unit.message))
		key := fmt.Sprintf("%s:%d:%d:%s", d.File, d.Line, d.Col, d.Message)
		posKey := fmt.Sprintf("%s:%d:%d", d.File, d.Line, d.Col)
		if seen[key] || covered[posKey] {
			continue
		}
		seen[key] = true
		diags = append(diags, d)
	}
	return diags
}

type errUnit struct {
	pointer string
	message string
}

// flatten walks the validation error tree and keeps leaf causes, which carry
// the specific keyword failures.
func flatten(ve *jsonschema.ValidationError) []errUnit {
	if len(ve.Causes) == 0 {
		return []errUnit{{
			pointer: pointerFromSegments(ve.InstanceLocation),
			message: ve.ErrorKind.LocalizedString(printer),
		}}
	}
	var units []errUnit
	for _, c := range ve.Causes {
		units = append(units, flatten(c)...)
	}
	return units
}

func pointerFromSegments(segments []string) string {
	p := ""
	for _, s := range segments {
		p += "/" + s
	}
	return p
}

func diag(f *parser.File, pointer, rule, message string) report.Diagnostic {
	pos := f.Position(pointer)
	return report.Diagnostic{
		File:     f.Path,
		Line:     pos.Line,
		Col:      pos.Col,
		Severity: report.Error,
		Rule:     rule,
		Message:  message,
	}
}
