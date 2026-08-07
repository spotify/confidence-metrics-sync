package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/report"
)

func newValidateCmd(output *string) *cobra.Command {
	var sourceReference string
	var offline bool
	var adoptFrom []string

	cmd := &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate metric definition YAML files",
		Long: `Validate checks metric definition YAML files without changing Confidence.

Tier 1 (always, offline): JSON Schema + cross-reference checks with
file:line:col errors. Tier 2 (default, needs credentials): a dry run via
ApplyMetricsSync — the server computes the exact plan a sync would apply
(create/update/archive/unchanged, ownership conflicts, per-resource errors).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			files, found, diags, err := loadValidated(args[0])
			if err != nil {
				return err
			}
			if found == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no definition files found — nothing to validate")
				return nil
			}

			if err := checkAdoptFrom(sourceReference, adoptFrom); err != nil {
				return err
			}

			var outcomes []report.OutcomeItem
			cfg, haveCreds := confidence.ConfigFromEnv()
			switch {
			case !haveCreds && !offline:
				return fmt.Errorf("%w: set %s and %s, or pass --offline for schema-only validation",
					errAuth, confidence.EnvClientID, confidence.EnvClientSecret)
			case offline:
				diags = append(diags, report.Diagnostic{
					Severity: report.Notice, Rule: "offline",
					Message: "offline mode — server-side dry run skipped",
				})
				// Adoption is a server-side decision, so there is nothing to
				// preview here. Say so: a flag that was asked for and did
				// nothing must not pass in silence.
				if len(adoptFrom) > 0 {
					diags = append(diags, report.Diagnostic{
						Severity: report.Notice, Rule: "offline",
						Message: "--adopt-from has no effect offline — ownership is resolved server-side, so rerun without --offline to preview adoptions",
					})
				}
			case !report.HasErrors(diags):
				if sourceReference == "" {
					return fmt.Errorf("--source-reference is required for the dry run (or pass --offline)")
				}
				client, err := confidence.NewClient(cfg)
				if err != nil {
					return err
				}
				req, mdiags, err := buildSyncRequest(cmd.Context(), client, files, sourceReference, true, adoptFrom)
				if err != nil {
					return err
				}
				diags = append(diags, mdiags...)
				// Only ask the server for a plan when the request maps fully: a
				// partial resource list would misreport absences as archives.
				if !report.HasErrors(diags) {
					resp, err := client.ApplyMetricsSync(cmd.Context(), req)
					if err != nil {
						return fmt.Errorf("dry run failed: %w", err)
					}
					outcomes = outcomeItems(req, resp)
					diags = append(diags, unusedAdoptFrom(adoptFrom, outcomes)...)
					diags = append(diags, massPruneWarning(sourceReference, outcomes)...)
				}
			}
			report.Sort(diags)

			switch *output {
			case "json":
				if err := report.JSON(cmd.OutOrStdout(), diags, outcomes, found); err != nil {
					return err
				}
			case "text":
				report.Text(cmd.OutOrStdout(), diags, found)
				if outcomes != nil {
					report.Outcomes(cmd.OutOrStdout(), outcomes, true)
				}
			default:
				return fmt.Errorf("unknown output format %q (want text or json)", *output)
			}

			if report.HasErrors(diags) || report.HasOutcomeErrors(outcomes) {
				return errFindings
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sourceReference, "source-reference", "",
		"identifier of this repository for reconciliation (e.g. 'github.com/org/repo')")
	cmd.Flags().BoolVar(&offline, "offline", false,
		"schema-only validation without API credentials (reduced guarantees)")
	cmd.Flags().StringArrayVar(&adoptFrom, "adopt-from", nil,
		"preview the plan as if syncing with --adopt-from (matched resources owned by this source reported as WOULD ADOPT)")
	return cmd
}
