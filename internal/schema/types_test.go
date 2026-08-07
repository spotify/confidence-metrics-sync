package schema

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func fileWithSQL(sql string) *File {
	return &File{FactTables: []FactTable{{
		DisplayName:     "FT",
		SQL:             ExactText(sql),
		TimestampColumn: "ts",
		Entities:        []EntityMapping{{Entity: "user", Column: "u"}},
		Measures:        []Measure{{DisplayName: "v", Column: "v"}},
	}}}
}

// yaml.v3 loses the leading whitespace of nested block scalars whose first
// line starts with whitespace — or emits unparseable output for exotic
// whitespace (see ExactText). These strings must survive a marshal/unmarshal
// round trip byte-exactly.
func TestExactTextRoundTripsLeadingWhitespace(t *testing.T) {
	cases := []string{
		"  SELECT\n    a\n  FROM t", // the C4S-1392 shape: uniformly indented SQL
		"\tSELECT 1",
		"\nSELECT 1",
		" SELECT 1",           // LINE SEPARATOR — multi-byte, breaks a first-byte check
		" SELECT 1",           // PARAGRAPH SEPARATOR
		"SELECT\n  a\nFROM t", // control: plain multiline
	}
	for _, sql := range cases {
		data, err := yaml.Marshal(fileWithSQL(sql))
		if err != nil {
			t.Fatal(err)
		}
		var back File
		if err := yaml.Unmarshal(data, &back); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, data)
		}
		if got := string(back.FactTables[0].SQL); got != sql {
			t.Errorf("sql round trip mismatch:\nwant %q\ngot  %q\nyaml:\n%s", sql, got, data)
		}
	}
}

func TestExactTextCoversDescriptions(t *testing.T) {
	desc := "  indented first line\n  of a description"
	file := fileWithSQL("SELECT 1")
	file.FactTables[0].Description = ExactText(desc)

	data, err := yaml.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	var back File
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, data)
	}
	if got := string(back.FactTables[0].Description); got != desc {
		t.Errorf("description round trip mismatch:\nwant %q\ngot  %q\nyaml:\n%s", desc, got, data)
	}
}

func TestExactTextKeepsBlockStyleForPlainMultiline(t *testing.T) {
	data, err := yaml.Marshal(fileWithSQL("SELECT\n  a\nFROM t"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "sql: |") {
		t.Errorf("plain multiline SQL should stay a block scalar:\n%s", data)
	}
}
