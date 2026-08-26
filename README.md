# confidence-metrics-sync

Manage [Confidence](https://confidence.spotify.com) metrics as code: define
fact tables, measurements, and metrics in YAML in a git repository, validate
every change on pull requests, and sync to Confidence on merge. Synced metrics
appear in Confidence as **repo-managed** — read-only in the UI, owned by your
repo and its review process.

```
confidence-metrics validate <path>   # check definitions, show what would change
confidence-metrics sync <path>       # apply definitions to Confidence
```

## Quick start

1. Create an API client in Confidence and store its credentials as secrets in
   your repository (`CONFIDENCE_CLIENT_ID`, `CONFIDENCE_CLIENT_SECRET`). Use a
   dedicated API client per metrics repository.
2. Add metric definitions under `metrics/`:

```yaml
# metrics/streaming.yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/spotify/confidence-metrics-sync/main/internal/schema/metric.schema.json

fact_tables:
  - name: factTables/hourly-stream
    display_name: Hourly Stream
    table: analytics.prod.hourly_stream
    timestamp_column: event_time
    entities:
      - entity: user
        column: user_id
    measures:
      - display_name: minutes_played
        column: ms_played / 60000

measurements:
  - name: measurements/hourly-minutes-played
    display_name: Hourly Minutes Played
    fact_table: factTables/hourly-stream
    entity: user
    measure: minutes_played
    operation: sum
    null_handling:
      replace_entity_null_with_zero: true
    metrics:
      - name: metrics/minutes-played-day-1
        display_name: Minutes Played - Day 1
        measurement_window:
          aggregation_window: 86400s
        preferred_direction: increase
```

3. Add the workflow:

```yaml
# .github/workflows/confidence-metrics.yaml
name: Confidence Metrics
on:
  pull_request:
    paths: ['metrics/**']
  push:
    branches: [main]
    paths: ['metrics/**']

concurrency:
  group: confidence-metrics-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

jobs:
  validate:
    if: github.event_name == 'pull_request'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: spotify/confidence-metrics-sync@v1
        with:
          path: metrics/
          mode: validate
          client-id: ${{ secrets.CONFIDENCE_CLIENT_ID }}
          client-secret: ${{ secrets.CONFIDENCE_CLIENT_SECRET }}

  sync:
    if: github.event_name == 'push'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: spotify/confidence-metrics-sync@v1
        with:
          path: metrics/
          mode: sync
          client-id: ${{ secrets.CONFIDENCE_CLIENT_ID }}
          client-secret: ${{ secrets.CONFIDENCE_CLIENT_SECRET }}
```

That's it: pull requests get inline annotations on definition errors plus a
dry-run summary of what would change; merges to main reconcile Confidence with
the repository.

The action derives `--source-reference` as `github.com/<owner>/<repository>`
unless the `source-reference` input overrides it. It downloads the CLI release
pinned by the selected action version and verifies the archive against the
published SHA256 checksums before execution.

## How it works

### `validate`

Runs on pull requests. Makes no changes.

- Parses all YAML files under the given path and validates them against the
  [metrics schema](https://raw.githubusercontent.com/spotify/confidence-metrics-sync/main/internal/schema/metric.schema.json): structure,
  types, enums, stable resource names, and relationships. A measurement's fact
  table and a metric's measurement must be declared somewhere under the same
  path; undeclared references are reported at their YAML location.
- Runs each metric through Confidence's server-side validation.
- Shows a dry-run plan against your Confidence account: what would be created,
  updated, deleted, or left unchanged.
- With `--check-warehouse` *(coming soon)*, additionally verifies each fact
  table against your warehouse via Confidence — the query compiles and every
  declared column exists with a supported type — without reading any rows.

Credentials are required by default: a missing CI secret fails the check
loudly instead of silently passing a weaker one. For contexts without secrets
(e.g. pull requests from forks), `--offline` explicitly opts into schema-only
validation.

### `sync`

Runs on merge. Reconciles Confidence with the repository:

- Creates and updates fact tables, measurements, and metrics to match the
  YAML. Synced metrics are marked repo-managed and become read-only in the
  Confidence UI.
- Uses `name` as stable identity. Changing `display_name` updates the same
  resource; changing `name` creates a replacement and removes the old resource.
- Archives metrics that this repository previously synced but that are no
  longer defined in the YAML — never hard-deletes. Re-adding the definition
  unarchives it.
- Refuses to take over a resource that already exists in Confidence but is not
  managed by this repository — a name collision can never silently overwrite
  another team's metric. Intentional migrations opt in with `sync --adopt-from`,
  which names the source being taken over, and `validate` always shows pending
  adoptions in the plan first.

Sync reports a summary of every run:

```
Sync complete:
  Created:   3 metrics, 1 fact table
  Updated:   1 metric
  Adopted:   0
  Archived:  0
  Unchanged: 12 metrics, 4 fact tables
  Errors:    0
```

### Stable resource names and repository-local relationships

Every fact table, measurement, and metric requires a full Confidence resource
name (`factTables/...`, `measurements/...`, or `metrics/...`). Treat `name` as
an immutable identifier: edit `display_name` for a user-facing rename. Editing
`name` requests a replacement and can break consumers that reference the old
resource.

`fact_table` and `measurement` relationships also use resource names. Every
referenced fact table and measurement must be declared somewhere under the
path passed to `validate` or `sync`; references may cross YAML files within
that repository snapshot. This keeps the submitted graph self-contained and
matches the server's atomic reconciliation contract.

When migrating an existing repository, use `export` to discover the exact
backend names and copy them into the definitions. Do not invent prettier IDs
for existing resources, and require `validate` to report a no-op before the
first sync with named definitions.

### Moving resources between repositories

Ownership follows `--source-reference`, so moving a metric to another
repository is a transfer, in this order:

1. **In the new repo**, add the definitions and sync with
   `--adopt-from <old-reference>`. The plan reports each one as
   `» metric "…" from <old-reference>`.
2. **In the old repo**, delete them. Until it does, its syncs fail: it still
   declares resources that now belong elsewhere. That failure is the reminder,
   and only the old repo can clear it.

**Do not reverse these steps.** Removing the definitions from the old repo
first archives the metric and *deletes* the measurement and fact table. Stable
names make restoration possible, but adopting first avoids any interval where
the shared resource is unavailable. Once ownership has moved, the old repo can
no longer archive it, so step 2 is safe.

The same flag recovers from a mistyped `--source-reference`: sync once with
`--adopt-from <the-typo>`.

Remove `--adopt-from` once the migration has landed. Sync warns when an entry
adopted nothing, because a flag left standing in CI could silently take over
whatever collides on a resource name next.

## Configuration

| Environment variable | Purpose |
|---|---|
| `CONFIDENCE_CLIENT_ID` | API client ID (required for plan/sync) |
| `CONFIDENCE_CLIENT_SECRET` | API client secret (required for plan/sync) |
| `CONFIDENCE_METRICS_URL` | Metrics API override (default `https://metrics.confidence.dev`) |
| `CONFIDENCE_IAM_URL` | IAM/token API override (default `https://iam.confidence.dev`) |

Credentials are only ever read from the environment — never from flags or
files. Flags:

- `--source-reference` — identifier of this repository; scopes ownership and
  reconciliation (e.g. `github.com/org/repo`)
- `--output text|json` — machine-readable results for scripting (default `text`)
- `--offline` — schema-only validation without credentials (validate only)
- `--adopt-from <source>` — take ownership of matched resources currently owned
  by that source: `api` for resources no repository manages, or another
  repository's reference. Repeatable. There is no wildcard, and `api` is
  reserved as a `--source-reference`

Exit codes: `0` success · `1` validation findings or sync errors · `2` usage
error · `3` authentication or transient API failure (safe to retry).

## Other CI systems

The GitHub Action is a thin wrapper; any CI works the same way. Replace the
version and platform below as needed:

```bash
VERSION=v1.0.0
curl -fsSLO "https://github.com/spotify/confidence-metrics-sync/releases/download/${VERSION}/confidence-metrics_linux_amd64.tar.gz"
curl -fsSLO "https://github.com/spotify/confidence-metrics-sync/releases/download/${VERSION}/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS
tar xzf confidence-metrics_linux_amd64.tar.gz
export CONFIDENCE_CLIENT_ID=... CONFIDENCE_CLIENT_SECRET=...
./confidence-metrics validate metrics/    # PR pipeline
./confidence-metrics sync metrics/        # main-branch pipeline
```

Release binaries are available for Linux, macOS, and Windows on amd64 and
arm64. They ship with SHA256 checksums; the GitHub Action verifies them on
download.

## Editor support

The YAML schema is published from this repository at
`https://raw.githubusercontent.com/spotify/confidence-metrics-sync/main/internal/schema/metric.schema.json`.
Add the
`# yaml-language-server: $schema=...` header (as in the example above) to get
autocomplete and inline validation in VS Code, IntelliJ, and any editor with
YAML language server support.

## Development

Requires Go 1.26+.

```bash
make ci        # vet + test + build
make build     # binary at bin/confidence-metrics
make snapshot  # cross-platform builds via goreleaser
```

Pull requests and `main` run `make ci` on GitHub Actions. Release Please opens
version PRs from Conventional Commit titles. Merging a release PR creates the
tag and GitHub release; GoReleaser then builds the platform archives and
publishes `SHA256SUMS`, and advances the matching major action tag such as
`v1` only after those assets are available.

## License

[Apache-2.0](LICENSE). See [NOTICE](NOTICE) for attribution information.
