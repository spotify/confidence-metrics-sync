// Package parser loads metric definition YAML files into typed structs while
// retaining source positions for every node, so downstream validation can
// report file:line:col.
package parser

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/spotify/confidence-metrics-sync/internal/report"
	"github.com/spotify/confidence-metrics-sync/internal/schema"
)

// File is one parsed YAML file: its typed content, a generic value for JSON
// Schema validation, and a position index keyed by JSON-pointer-like paths
// (e.g. "/fact_tables/0/display_name").
type File struct {
	Path      string
	Def       *schema.File
	Generic   any
	Positions map[string]Pos
}

// Pos is a 1-based position in a file.
type Pos struct {
	Line int
	Col  int
}

// Position resolves a JSON pointer to a source position, walking up the
// pointer until a known node is found (so errors on a missing field anchor
// to the enclosing object).
func (f *File) Position(pointer string) Pos {
	for {
		if p, ok := f.Positions[pointer]; ok {
			return p
		}
		i := strings.LastIndex(pointer, "/")
		if i < 0 {
			return Pos{}
		}
		pointer = pointer[:i]
	}
}

// LoadDir walks root and parses every *.yaml / *.yml file. Parse failures
// (unreadable file, YAML syntax error, duplicate key, unknown field, empty or
// multi-document file) are returned as diagnostics; files that parse land in
// the returned slice. The int is the number of YAML files found, parsed or
// not.
func LoadDir(root string) ([]*File, int, []report.Diagnostic, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, 0, nil, err
	}
	sort.Strings(paths)

	var files []*File
	var diags []report.Diagnostic
	for _, path := range paths {
		f, ds := loadFile(path)
		diags = append(diags, ds...)
		if f != nil {
			files = append(files, f)
		}
	}
	return files, len(paths), diags, nil
}

func loadFile(path string) (*File, []report.Diagnostic) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []report.Diagnostic{{
			File: path, Severity: report.Error, Rule: "read",
			Message: err.Error(),
		}}
	}

	// First pass: parse into a node tree for positions and the generic value.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, yamlErrDiagnostics(path, err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		// Deliberate team decision: an empty file is a warning, not an error —
		// the schema does not reject "nothing defined" and neither do we.
		return nil, []report.Diagnostic{{
			File: path, Severity: report.Warning, Rule: "no-definitions",
			Message: "file contains no YAML document",
		}}
	}

	// Second pass: strict decode into typed structs. Catches unknown fields
	// and duplicate keys with precise messages.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var def schema.File
	if err := dec.Decode(&def); err != nil {
		return nil, yamlErrDiagnostics(path, err)
	}
	// Reject multi-document files: everything after the first document is
	// silently ignored otherwise, which would be a silent no-op for its
	// definitions.
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, []report.Diagnostic{{
			File: path, Severity: report.Error, Rule: "multi-document",
			Message: "file contains more than one YAML document; define everything in a single document",
		}}
	}

	root := doc.Content[0]
	f := &File{
		Path:      path,
		Def:       &def,
		Generic:   nodeToAny(root),
		Positions: map[string]Pos{},
	}
	indexPositions(root, "", f.Positions)
	f.Positions[""] = Pos{Line: root.Line, Col: root.Column}
	return f, nil
}

// yamlErrDiagnostics converts a yaml.v3 error into positioned diagnostics.
// A yaml.TypeError bundles one message per problem (unknown field, duplicate
// key, type mismatch); each becomes its own diagnostic. Go type names from
// strict decoding are rewritten into user vocabulary.
func yamlErrDiagnostics(path string, err error) []report.Diagnostic {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		diags := make([]report.Diagnostic, 0, len(te.Errors))
		for _, msg := range te.Errors {
			diags = append(diags, yamlMsgDiagnostic(path, msg))
		}
		return diags
	}
	return []report.Diagnostic{yamlMsgDiagnostic(path, strings.TrimPrefix(err.Error(), "yaml: "))}
}

var unknownFieldRe = regexp.MustCompile(`field (\S+) not found in type \S+`)

// inlineAggregationFields are measurement-level fields people coming from
// Eppo-style schemas try to put on a metric. Give them a pointer instead of
// a bare "unknown field".
var inlineAggregationFields = map[string]bool{
	"numerator": true, "denominator": true, "measure": true,
	"operation": true, "fact_table": true,
}

func yamlMsgDiagnostic(path, msg string) report.Diagnostic {
	d := report.Diagnostic{File: path, Severity: report.Error, Rule: "yaml", Message: msg}

	// Lift "line N: " prefixes into the diagnostic position.
	if rest, ok := strings.CutPrefix(msg, "line "); ok {
		if j := strings.Index(rest, ": "); j > 0 {
			if n, err := strconv.Atoi(rest[:j]); err == nil {
				d.Line = n
				d.Message = rest[j+2:]
			}
		}
	}

	if m := unknownFieldRe.FindStringSubmatch(d.Message); m != nil {
		d.Rule = "unknown-field"
		d.Message = fmt.Sprintf("unknown field %q", m[1])
		if inlineAggregationFields[m[1]] {
			d.Message += " — metrics do not define aggregation inline; define it on a measurement and reference the measurement by display name"
		}
	} else if strings.Contains(d.Message, "already defined at line") {
		d.Rule = "duplicate-key"
	}
	return d
}

// nodeToAny converts a YAML node tree into generic Go values suitable for
// JSON Schema validation (map[string]any, []any, string, bool, nil, and
// json.Number-compatible numbers).
func nodeToAny(n *yaml.Node) any {
	switch n.Kind {
	case yaml.AliasNode:
		return nodeToAny(n.Alias)
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return nodeToAny(n.Content[0])
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			m[n.Content[i].Value] = nodeToAny(n.Content[i+1])
		}
		return m
	case yaml.SequenceNode:
		s := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			s = append(s, nodeToAny(c))
		}
		return s
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			return nil
		case "!!bool":
			var b bool
			_ = n.Decode(&b)
			return b
		case "!!int":
			var i int64
			if err := n.Decode(&i); err == nil {
				return i
			}
			return n.Value
		case "!!float":
			var f float64
			if err := n.Decode(&f); err == nil {
				return f
			}
			return n.Value
		default:
			return n.Value
		}
	}
	return nil
}

// indexPositions records the position of every node keyed by JSON pointer.
func indexPositions(n *yaml.Node, pointer string, out map[string]Pos) {
	switch n.Kind {
	case yaml.AliasNode:
		return // positions of the anchor, not the alias, are indexed
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			p := pointer + "/" + escapePointer(k.Value)
			// Anchor fields to their key, not the value: for block values the
			// key line is where a human looks.
			out[p] = Pos{Line: k.Line, Col: k.Column}
			indexPositions(v, p, out)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			p := pointer + "/" + strconv.Itoa(i)
			out[p] = Pos{Line: c.Line, Col: c.Column}
			indexPositions(c, p, out)
		}
	}
}

func escapePointer(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

// FmtPointer renders a JSON pointer for humans, e.g. "fact_tables[0].name".
func FmtPointer(pointer string) string {
	if pointer == "" {
		return "(document root)"
	}
	var b strings.Builder
	for _, seg := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if _, err := strconv.Atoi(seg); err == nil {
			fmt.Fprintf(&b, "[%s]", seg)
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(seg)
	}
	return b.String()
}
