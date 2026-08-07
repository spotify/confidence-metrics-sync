package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags.
var version = "dev"

// errFindings marks "the tool worked, the definitions have errors" — exit 1,
// as opposed to usage/unexpected errors (exit 2).
var errFindings = errors.New("validation failed")

// errAuth marks missing/failed authentication — exit 3, distinct from
// findings (1) and usage errors (2) so CI can retry transient auth issues.
var errAuth = errors.New("authentication required")

// newRootCmd builds a fresh command tree. Commands hold their flag state in
// constructor-local variables, so trees are independent — no shared
// package-level state between invocations (or between tests).
func newRootCmd() *cobra.Command {
	var output string

	root := &cobra.Command{
		Use:   "confidence-metrics",
		Short: "Validate and sync repo-managed Confidence metrics",
		Long: `confidence-metrics manages repo-managed Confidence metrics defined in YAML.

Definitions live in a git repository; this tool validates them (on pull
requests) and syncs them to Confidence (on merge to the default branch).`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&output, "output", "o", "text", "output format: text or json")

	root.AddCommand(newValidateCmd(&output))
	root.AddCommand(newSyncCmd(&output))
	root.AddCommand(newExportCmd())
	return root
}

// Execute runs a fresh command tree. Exit codes: 0 success, 1 findings/apply
// errors, 2 usage or unexpected errors, 3 authentication failures.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		if errors.Is(err, errFindings) {
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if errors.Is(err, errAuth) {
			os.Exit(3)
		}
		os.Exit(2)
	}
}
