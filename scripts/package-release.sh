#!/usr/bin/env bash
set -eu

export GOTOOLCHAIN=local

fail() {
  printf 'package-release: %s\n' "$1" >&2
  exit 1
}

require_value() {
  name=$1
  value=$2
  [ -n "$value" ] || fail "$name is required"
}

require_value VERSION "${VERSION-}"
require_value GOOS "${GOOS-}"
require_value GOARCH "${GOARCH-}"
require_value SOURCE_DATE_EPOCH "${SOURCE_DATE_EPOCH-}"
require_value OUT_DIR "${OUT_DIR-}"

if ! [[ "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail 'VERSION must be strict semver core'
fi
if ! [[ "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]]; then
  fail 'SOURCE_DATE_EPOCH must be a non-negative decimal integer'
fi
case "$GOOS/$GOARCH" in
  darwin/amd64|darwin/arm64|linux/amd64|linux/arm64|windows/amd64|windows/arm64)
    ;;
  *)
    fail "unsupported target $GOOS/$GOARCH"
    ;;
esac

if [ -L "$OUT_DIR" ]; then
  fail 'OUT_DIR must not be a symlink'
fi
[ -d "$OUT_DIR" ] || fail 'OUT_DIR must be an existing directory'
out_dir=$(CDPATH= cd -- "$OUT_DIR" && pwd -P) || fail 'OUT_DIR cannot be resolved'

versioned_name="delegatd_${VERSION}_${GOOS}_${GOARCH}"
binary_name="$versioned_name"
case "$GOOS" in
  windows)
    binary_name="$versioned_name.exe"
    archive_name="$versioned_name.zip"
    archive_format=zip
    ;;
  *)
    archive_name="$versioned_name.tar.gz"
    archive_format=tar.gz
    ;;
esac
sbom_name="$versioned_name.spdx.json"
archive_output="$out_dir/$archive_name"
sbom_output="$out_dir/$sbom_name"
for output in "$archive_output" "$sbom_output"; do
  if [ -e "$output" ] || [ -L "$output" ]; then
    fail "output already exists: $output"
  fi
done

go_version=$(go version | awk '{print $3}')
[ "$go_version" = go1.27.0 ] || fail "required go1.27.0, got $go_version"

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || fail 'must run inside a Git repository'
host_goos=$(go env GOHOSTOS)
host_goarch=$(go env GOHOSTARCH)
mkdir -p "$repo_root/.cache"
stage_dir=$(mktemp -d "$repo_root/.cache/package-release.XXXXXX")
trap 'rm -rf "$stage_dir"' EXIT HUP INT TERM
binary="$stage_dir/$binary_name"
raw_sbom="$stage_dir/$versioned_name.raw.spdx.json"
canonical_sbom="$stage_dir/$sbom_name"
stage_archive="$stage_dir/$archive_name"
syft_config="$stage_dir/syft-empty.yaml"
: > "$syft_config"

GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 go build -trimpath -buildvcs=true -o "$binary" ./cmd/delegatd
GOOS="$host_goos" GOARCH="$host_goarch" SYFT_CHECK_FOR_APP_UPDATE=false go tool syft scan "$binary" --from file --source-name "$versioned_name" --config "$syft_config" -q -o "spdx-json=$raw_sbom"
GOOS="$host_goos" GOARCH="$host_goarch" go run ./internal/releasetool canonicalize-spdx --input "$raw_sbom" --output "$canonical_sbom" --name "$versioned_name" --namespace "https://github.com/poulsena/delegatd/releases/tag/v${VERSION}/sbom/${GOOS}-${GOARCH}" --epoch "$SOURCE_DATE_EPOCH"
GOOS="$host_goos" GOARCH="$host_goarch" go run ./internal/releasetool archive --input "$binary" --output "$stage_archive" --entry "$binary_name" --format "$archive_format" --epoch "$SOURCE_DATE_EPOCH"

[ -s "$canonical_sbom" ] || fail 'canonical SBOM is empty'
[ -s "$stage_archive" ] || fail 'archive is empty'
mv "$canonical_sbom" "$sbom_output"
mv "$stage_archive" "$archive_output"
printf '%s\n' "$archive_name" "$sbom_name"
