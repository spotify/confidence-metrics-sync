package cmd

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/spotify/confidence-metrics-sync/internal/confidence"
	"github.com/spotify/confidence-metrics-sync/internal/export"
)

func newExportCmd() *cobra.Command {
	var opts export.Options
	var outFile string

	cmd := &cobra.Command{
		Use:   "export [name ...]",
		Short: "Export Confidence metrics as YAML",
		Long: `Export reads metrics from your Confidence account and writes them in the
repo YAML format.

Metrics are selected by display name. Each argument is a case-insensitive
pattern: plain text must match the full name — quote names with spaces
('checkout conversion - day 7') — and glob characters (* ? [) switch to
glob matching ('*conversion*'). Multiple patterns select the union; no
patterns exports every metric.

Resource names are preserved and references use exact resource names. By
default, only the selected metrics are exported, so the result is a partial
snippet whose referenced measurements are not included. Use
--with-dependencies to produce definitions that can be validated and synced:
it also exports each selected metric's measurement and that measurement's
fact table.

NOTE: sync treats the repository as the full statement of what its
reference owns — resources absent from the files are ARCHIVED. Do not
sync a partial export against a reference that owns more. Separately,
syncing exported API-managed resources reports ownership errors unless
sync/validate is run with --adopt-from api, which takes ownership of them;
exporting your own repo-managed resources round-trips as a no-op.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A malformed glob would otherwise silently match nothing.
			for _, p := range args {
				if _, err := path.Match(strings.ToLower(p), ""); err != nil {
					return fmt.Errorf("invalid pattern %q: %v", p, err)
				}
			}
			cfg, haveCreds := confidence.ConfigFromEnv()
			if !haveCreds {
				return fmt.Errorf("%w: set %s and %s", errAuth, confidence.EnvClientID, confidence.EnvClientSecret)
			}
			client, err := confidence.NewClient(cfg)
			if err != nil {
				return err
			}
			opts.Metrics = true
			opts.Patterns = args

			ctx := cmd.Context()
			// Full listings regardless of selection — entity display-name
			// resolution and dependency export need them.
			entities, err := client.ListEntities(ctx)
			if err != nil {
				return fmt.Errorf("listing entities: %w", err)
			}
			factTables, err := client.ListFactTables(ctx, "")
			if err != nil {
				return fmt.Errorf("listing fact tables: %w", err)
			}
			measurements, err := client.ListMeasurements(ctx, "")
			if err != nil {
				return fmt.Errorf("listing measurements: %w", err)
			}
			metrics, err := client.ListMetrics(ctx, "")
			if err != nil {
				return fmt.Errorf("listing metrics: %w", err)
			}

			file, warnings, stats, err := export.Build(entities, factTables, measurements, metrics, opts)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			for _, kind := range []string{"fact table", "measurement", "metric"} {
				if stats.Fetched[kind] == 0 && stats.Exported[kind] == 0 {
					continue
				}
				deps := ""
				if stats.Deps[kind] > 0 {
					deps = fmt.Sprintf(" (%d as dependencies)", stats.Deps[kind])
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "%ss: %d in account, %d archived/deleted/system, %d filtered out, %d not expressible, %d exported%s\n",
					kind, stats.Fetched[kind], stats.NotAlive[kind], stats.FilteredOut[kind], stats.Skipped[kind], stats.Exported[kind], deps)
			}
			if len(file.FactTables)+len(file.Measurements)+len(file.Metrics) == 0 {
				return fmt.Errorf("no metrics matched — see the per-kind breakdown above (plain patterns must equal the full display name, case-insensitive; use glob characters for partial matches, e.g. '*conversion*')")
			}

			data, err := yaml.Marshal(file)
			if err != nil {
				return err
			}
			header := []byte("# yaml-language-server: $schema=https://raw.githubusercontent.com/spotify/confidence-metrics-sync/main/internal/schema/metric.schema.json\n# Exported by confidence-metrics; resource names are stable identifiers.\n\n")
			data = append(header, data...)

			if outFile == "" {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			return os.WriteFile(outFile, data, 0o644)
		},
	}

	cmd.Flags().BoolVar(&opts.WithDependencies, "with-dependencies", false, "also export each metric's measurement and fact table")
	cmd.Flags().StringVar(&opts.Reference, "source-reference", "", "only metrics owned by this reference")
	cmd.Flags().StringVar(&outFile, "out", "", "write to file instead of stdout")
	return cmd
}
