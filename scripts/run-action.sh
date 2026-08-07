#!/usr/bin/env bash
set -euo pipefail

mode="${INPUT_MODE:-validate}"
case "$mode" in
  validate|sync) ;;
  *) echo "mode must be validate or sync, got: $mode" >&2; exit 2 ;;
esac

offline="${INPUT_OFFLINE:-false}"
case "$offline" in
  true|false) ;;
  *) echo "offline must be true or false, got: $offline" >&2; exit 2 ;;
esac
if [[ "$mode" == sync && "$offline" == true ]]; then
  echo "offline mode is only valid with mode: validate" >&2
  exit 2
fi

source_reference="${INPUT_SOURCE_REFERENCE:-}"
if [[ -z "$source_reference" && -n "${GITHUB_REPOSITORY:-}" ]]; then
  server="${GITHUB_SERVER_URL:-https://github.com}"
  server="${server#https://}"
  server="${server#http://}"
  source_reference="${server%/}/${GITHUB_REPOSITORY}"
fi

args=("$mode")
if [[ -n "$source_reference" ]]; then
  args+=(--source-reference "$source_reference")
fi
if [[ "$offline" == true ]]; then
  args+=(--offline)
fi
while IFS= read -r source; do
  if [[ -n "$source" ]]; then
    args+=(--adopt-from "$source")
  fi
done <<< "${INPUT_ADOPT_FROM:-}"
args+=("${INPUT_PATH:-metrics}")

log_file="$(mktemp)"
trap 'rm -f "$log_file"' EXIT

set +e
confidence-metrics "${args[@]}" 2>&1 | tee "$log_file"
exit_code=${PIPESTATUS[0]}
set -e

escape_data() {
  local value=$1
  value=${value//'%'/'%25'}
  value=${value//$'\r'/'%0D'}
  value=${value//$'\n'/'%0A'}
  printf '%s' "$value"
}

escape_property() {
  local value
  value=$(escape_data "$1")
  value=${value//':'/'%3A'}
  value=${value//','/'%2C'}
  printf '%s' "$value"
}

while IFS= read -r line; do
  if [[ "$line" =~ ^(.+):([0-9]+):([0-9]+):[[:space:]](error|warning|notice)\[[^]]+\]:[[:space:]](.*)$ ]]; then
    file=$(escape_property "${BASH_REMATCH[1]}")
    line_number=${BASH_REMATCH[2]}
    column=${BASH_REMATCH[3]}
    level=${BASH_REMATCH[4]}
    message=$(escape_data "${BASH_REMATCH[5]}")
    echo "::${level} file=${file},line=${line_number},col=${column}::${message}"
  fi
done < "$log_file"

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "### Confidence metrics: $mode"
    echo
    echo '```text'
    cat "$log_file"
    echo '```'
  } >> "$GITHUB_STEP_SUMMARY"
fi

exit "$exit_code"
