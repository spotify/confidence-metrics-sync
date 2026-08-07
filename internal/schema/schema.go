// Package schema holds the bundled JSON Schema for metric definition files
// and the Go types that mirror it. The JSON Schema file is the source of
// truth; the structs are written to match, and the fixture drift test keeps
// them in sync.
package schema

import (
	"bytes"
	_ "embed"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed metric.schema.json
var schemaJSON []byte

// ID is the canonical schema identifier (also its future public URL).
const ID = "https://confidence.dev/schemas/metrics/v1"

// Compile returns the compiled JSON Schema for metric definition files.
func Compile() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(ID, doc); err != nil {
		return nil, err
	}
	return c.Compile(ID)
}
