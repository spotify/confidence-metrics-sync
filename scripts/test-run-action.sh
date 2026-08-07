#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

set +e
PATH="$repo_root/bin:$PATH" \
  GITHUB_REPOSITORY=spotify/example-metrics \
  GITHUB_SERVER_URL=https://github.com \
  GITHUB_STEP_SUMMARY="$test_dir/summary" \
  INPUT_MODE=validate \
  INPUT_OFFLINE=true \
  INPUT_PATH="$repo_root/internal/validate/testdata/invalid/bad-values" \
  "$repo_root/scripts/run-action.sh" >"$test_dir/output" 2>&1
exit_code=$?
set -e

if [[ "$exit_code" -ne 1 ]]; then
  echo "expected validation exit code 1, got $exit_code" >&2
  exit 1
fi
grep -Eq '^::error file=.*line=[0-9]+,col=[0-9]+::' "$test_dir/output"
grep -Fq '### Confidence metrics: validate' "$test_dir/summary"
grep -Fq 'error[schema]' "$test_dir/summary"
