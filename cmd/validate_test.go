package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	// A fresh command tree per invocation — no shared flag state between
	// tests (or between real runs, for that matter).
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestValidateValidFixtures(t *testing.T) {
	out, err := run(t, "validate", "--offline", "../internal/validate/testdata/valid")
	if err != nil {
		t.Fatalf("expected success, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "valid") {
		t.Fatalf("unexpected output: %q", out)
	}
}

// Offline validation cannot preview adoptions — ownership is resolved
// server-side. A flag that was asked for and did nothing must say so.
func TestOfflineValidateNoticesIgnoredAdoptFrom(t *testing.T) {
	out, err := run(t, "validate", "--offline", "--adopt-from", "api",
		"../internal/validate/testdata/valid")
	if err != nil {
		t.Fatalf("an ignored --adopt-from is a notice, not a failure: %v\n%s", err, out)
	}
	if !strings.Contains(out, "--adopt-from has no effect offline") {
		t.Errorf("offline run did not mention the ignored flag:\n%s", out)
	}
	// Without the flag there is nothing to say about it.
	plain, err := run(t, "validate", "--offline", "../internal/validate/testdata/valid")
	if err != nil {
		t.Fatalf("expected success, got %v\n%s", err, plain)
	}
	if strings.Contains(plain, "--adopt-from") {
		t.Errorf("mentioned --adopt-from when it was never passed:\n%s", plain)
	}
}

func TestValidateRequiresCredentialsByDefault(t *testing.T) {
	// No CONFIDENCE_CLIENT_ID/SECRET in the test environment: without
	// --offline, validate must fail hard — a missing CI secret must never
	// produce a weaker green check.
	_, err := run(t, "validate", "../internal/validate/testdata/valid")
	if !errors.Is(err, errAuth) {
		t.Fatalf("expected errAuth without credentials, got %v", err)
	}
}

func TestValidateInvalidFixturesExitContract(t *testing.T) {
	out, err := run(t, "validate", "--offline", "../internal/validate/testdata/invalid/dangling-refs")
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings, got %v\n%s", err, out)
	}
}

func TestValidateJSONOutput(t *testing.T) {
	out, err := run(t, "validate", "--offline", "--output", "json", "../internal/validate/testdata/invalid/dangling-refs")
	if !errors.Is(err, errFindings) {
		t.Fatalf("expected errFindings, got %v", err)
	}
	var parsed struct {
		Diagnostics []struct {
			File string `json:"file"`
			Rule string `json:"rule"`
			Line int    `json:"line"`
			Col  int    `json:"col"`
		} `json:"diagnostics"`
		Summary struct {
			Errors int `json:"errors"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if parsed.Summary.Errors == 0 || len(parsed.Diagnostics) == 0 {
		t.Fatalf("expected errors in JSON output: %s", out)
	}
	wantRules := map[string]bool{
		"unknown-fact-table":  false,
		"unknown-measurement": false,
	}
	for _, diagnostic := range parsed.Diagnostics {
		if _, ok := wantRules[diagnostic.Rule]; ok {
			wantRules[diagnostic.Rule] = true
			if diagnostic.File == "" || diagnostic.Line == 0 || diagnostic.Col == 0 {
				t.Errorf("%s is not positioned: %+v", diagnostic.Rule, diagnostic)
			}
		}
	}
	for rule, found := range wantRules {
		if !found {
			t.Errorf("JSON output is missing %s: %s", rule, out)
		}
	}
}

func TestValidateMissingPath(t *testing.T) {
	_, err := run(t, "validate", "does-not-exist")
	if err == nil || errors.Is(err, errFindings) {
		t.Fatalf("expected hard error for missing path, got %v", err)
	}
}

func TestValidateEmptyDirExitsZero(t *testing.T) {
	out, err := run(t, "validate", "--offline", "testdata/empty-dir")
	if err != nil {
		t.Fatalf("expected exit 0 for empty dir, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to validate") {
		t.Fatalf("expected notice message, got: %q", out)
	}
}

func TestSyncEmptyDirExitsZero(t *testing.T) {
	out, err := run(t, "sync", "--source-reference", "test-repo", "testdata/empty-dir")
	if err != nil {
		t.Fatalf("expected exit 0 for empty dir, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to sync") {
		t.Fatalf("expected notice message, got: %q", out)
	}
}
