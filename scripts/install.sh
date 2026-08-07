#!/usr/bin/env bash
set -euo pipefail

version="${CONFIDENCE_METRICS_VERSION:?CONFIDENCE_METRICS_VERSION is required}"
version="${version#v}"

case "${RUNNER_OS:-}" in
  Linux) os=linux ;;
  macOS) os=darwin ;;
  Windows) os=windows ;;
  *) echo "Unsupported runner OS: ${RUNNER_OS:-unknown}" >&2; exit 1 ;;
esac

case "${RUNNER_ARCH:-}" in
  X64) arch=amd64 ;;
  ARM64) arch=arm64 ;;
  *) echo "Unsupported runner architecture: ${RUNNER_ARCH:-unknown}" >&2; exit 1 ;;
esac

extension=tar.gz
binary=confidence-metrics
if [[ "$os" == windows ]]; then
  extension=zip
  binary=confidence-metrics.exe
fi

archive="confidence-metrics_${os}_${arch}.${extension}"
release_root="${CONFIDENCE_METRICS_RELEASE_ROOT:-https://github.com/spotify/confidence-metrics-sync/releases/download}"
base_url="${release_root%/}/v${version}"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

curl --fail --silent --show-error --location --retry 3 \
  --output "$work_dir/$archive" "$base_url/$archive"
curl --fail --silent --show-error --location --retry 3 \
  --output "$work_dir/SHA256SUMS" "$base_url/SHA256SUMS"

expected="$(awk -v archive="$archive" '$2 == archive || $2 == "*" archive { print $1 }' "$work_dir/SHA256SUMS")"
if [[ -z "$expected" ]]; then
  echo "SHA256SUMS does not contain $archive" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$work_dir/$archive" | awk '{ print $1 }')"
else
  actual="$(shasum -a 256 "$work_dir/$archive" | awk '{ print $1 }')"
fi
if [[ "$actual" != "$expected" ]]; then
  echo "Checksum verification failed for $archive" >&2
  exit 1
fi

if [[ "$extension" == zip ]]; then
  unzip -q "$work_dir/$archive" -d "$work_dir/extracted"
else
  mkdir -p "$work_dir/extracted"
  tar -xzf "$work_dir/$archive" -C "$work_dir/extracted"
fi

install_dir="${RUNNER_TEMP:?RUNNER_TEMP is required}/confidence-metrics-${version}"
mkdir -p "$install_dir"
cp "$work_dir/extracted/$binary" "$install_dir/$binary"
chmod +x "$install_dir/$binary"
echo "$install_dir" >> "${GITHUB_PATH:?GITHUB_PATH is required}"
