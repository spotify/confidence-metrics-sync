package cmd

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/report"
)

// Polling knobs; overridden in tests.
var (
	pollInterval = 5 * time.Second
	pollTimeout  = 10 * time.Minute
)

// warehouseValidationFields are the fact-table changed_fields that trigger
// warehouse validation — only those updates are worth polling. A state change
// is emitted when a deleted fact table is restored in CREATING state.
var warehouseValidationFields = map[string]bool{
	"sql": true, "timestamp_column": true, "entities": true,
	"measures": true, "dimensions": true, "state": true,
}

func newSyncCmd(output *string) *cobra.Command {
	var sourceReference string
	var adoptFrom []string

	cmd := &cobra.Command{
		Use:   "sync <path>",
		Short: "Sync metric definitions to Confidence",
		Long: `Sync reconciles Confidence with the repository via one atomic
ApplyMetricsSync call: creates/updates everything defined under <path>,
archives resources owned by this reference that are no longer defined, and
reports per-resource outcomes. Created or schema-changed fact tables are then
polled until the warehouse validates them (ACTIVE) or rejects them (FAILED).

A resource that already exists under a different owner is an ownership
error. Pass --adopt-from to take it over, naming what you are taking it
from: --adopt-from api for resources no repository manages (e.g. after
export), or --adopt-from <reference> for another repository's. Adopted
resources flip to this repository's reference, reported as ADOPT. Anything
you do not name stays an error — there is no wildcard.

Moving a resource between repositories takes two changes, in this order:

  1. In the new repo, add the definitions and sync with
     --adopt-from <old-reference>.
  2. In the old repo, delete them. Until it does, its syncs fail — the
     resource it still declares now belongs to someone else.

Never the reverse. Removing them from the old repo first archives the
metric and DELETES the measurement and fact table. Stable names make
restoration possible, but adopting first avoids an interval where the
resource is unavailable.

Adoption is a one-time migration step with no undo — remove the flag once
the migration has landed and never leave it enabled in CI, or a future
resource-name collision is silently taken over.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			files, found, diags, err := loadValidated(args[0])
			if err != nil {
				return err
			}
			if found == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no definition files found — nothing to sync")
				return nil
			}

			cfg, haveCreds := confidence.ConfigFromEnv()
			if !haveCreds {
				return fmt.Errorf("%w: set %s and %s", errAuth, confidence.EnvClientID, confidence.EnvClientSecret)
			}
			if sourceReference == "" {
				return fmt.Errorf("--source-reference is required")
			}
			if err := checkAdoptFrom(sourceReference, adoptFrom); err != nil {
				return err
			}

			// Never apply definitions that fail offline validation.
			if report.HasErrors(diags) {
				report.Sort(diags)
				report.Text(cmd.OutOrStdout(), diags, found)
				return errFindings
			}

			client, err := confidence.NewClient(cfg)
			if err != nil {
				return err
			}
			req, mdiags, err := buildSyncRequest(cmd.Context(), client, files, sourceReference, false, adoptFrom)
			if err != nil {
				return err
			}
			diags = append(diags, mdiags...)
			if report.HasErrors(diags) {
				report.Sort(diags)
				report.Text(cmd.OutOrStdout(), diags, found)
				return errFindings
			}

			resp, err := client.ApplyMetricsSync(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("sync failed: %w", err)
			}
			outcomes := outcomeItems(req, resp)
			diags = append(diags, unusedAdoptFrom(adoptFrom, outcomes)...)
			diags = append(diags, massPruneWarning(sourceReference, outcomes)...)

			switch *output {
			case "json":
				if err := report.JSON(cmd.OutOrStdout(), diags, outcomes, found); err != nil {
					return err
				}
			case "text":
				report.Text(cmd.OutOrStdout(), diags, found)
				report.Outcomes(cmd.OutOrStdout(), outcomes, false)
			default:
				return fmt.Errorf("unknown output format %q (want text or json)", *output)
			}

			// The apply is atomic: any ERROR outcome means nothing persisted.
			if report.HasOutcomeErrors(outcomes) {
				return errFindings
			}

			// Post-apply safety net: warehouse schema validation runs async on
			// created/changed fact tables — poll them so a broken table fails
			// THIS build, loudly, instead of silently sitting in FAILED state.
			if failed := pollFactTables(cmd.Context(), cmd.OutOrStdout(), client, outcomes); failed {
				return errFindings
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sourceReference, "source-reference", "",
		"identifier of this repository for reconciliation (e.g. 'github.com/org/repo')")
	cmd.Flags().StringArrayVar(&adoptFrom, "adopt-from", nil,
		"take ownership of matched resources currently owned by this source: 'api' for resources no repository manages, or another repository's reference (repeatable; one-time migration, do not leave enabled in CI)")
	return cmd
}

type pollResult struct {
	line   string
	failed bool
}

// pollFactTables waits for created or schema-changed fact tables to leave
// CREATING/UPDATING — in parallel, so N slow tables cost one validation's
// wall-clock, not N, and every table gets the full deadline. Returns true if
// any ended FAILED (or couldn't be confirmed).
func pollFactTables(ctx context.Context, out io.Writer, client *confidence.Client, outcomes []report.OutcomeItem) bool {
	var pending []report.OutcomeItem
	for _, o := range outcomes {
		if o.Kind != "fact table" {
			continue
		}
		if o.Action == "CREATE" ||
			((o.Action == "UPDATE" || o.Action == "ADOPT") && triggersWarehouseValidation(o.ChangedFields)) {
			pending = append(pending, o)
		}
	}
	if len(pending) == 0 {
		return false
	}

	fmt.Fprintf(out, "\nWaiting for warehouse validation of %d fact table(s)...\n", len(pending))
	deadline := time.Now().Add(pollTimeout)

	results := make([]pollResult, len(pending))
	var wg sync.WaitGroup
	for i, o := range pending {
		wg.Add(1)
		go func(i int, o report.OutcomeItem) {
			defer wg.Done()
			results[i] = pollOne(ctx, client, o, deadline)
		}(i, o)
	}
	wg.Wait()

	anyFailed := false
	for _, r := range results {
		fmt.Fprint(out, r.line)
		anyFailed = anyFailed || r.failed
	}
	return anyFailed
}

// pollOne polls a single fact table until ACTIVE, FAILED, deadline, or
// context cancellation.
func pollOne(ctx context.Context, client *confidence.Client, o report.OutcomeItem, deadline time.Time) pollResult {
	for {
		ft, err := client.GetFactTable(ctx, o.Name)
		if err != nil {
			return pollResult{fmt.Sprintf("  ? %q: polling failed: %v\n", o.DisplayName, err), true}
		}
		switch ft.State {
		case confidence.TableStateActive:
			return pollResult{fmt.Sprintf("  ✓ %q validated\n", o.DisplayName), false}
		case confidence.TableStateFailed:
			msg := "warehouse validation failed"
			if ft.Error != nil && ft.Error.Message != "" {
				msg = ft.Error.Message
			}
			return pollResult{fmt.Sprintf("  ✗ %q: %s\n", o.DisplayName, msg), true}
		}
		if time.Now().After(deadline) {
			return pollResult{fmt.Sprintf("  ? %q: still %s after %s — check Confidence\n", o.DisplayName, ft.State, pollTimeout), true}
		}
		select {
		case <-ctx.Done():
			return pollResult{fmt.Sprintf("  ? %q: cancelled\n", o.DisplayName), true}
		case <-time.After(pollInterval):
		}
	}
}

func triggersWarehouseValidation(changedFields []string) bool {
	for _, f := range changedFields {
		if warehouseValidationFields[f] {
			return true
		}
	}
	return false
}
