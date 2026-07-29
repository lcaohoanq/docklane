#!/usr/bin/env bash

set -euo pipefail

version="${1:-}"
output_directory="${2:-dist}"

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  echo "usage: $0 vMAJOR.MINOR.PATCH[-PRERELEASE] [OUTPUT_DIRECTORY]" >&2
  exit 2
fi

for command in go tar gzip sha256sum install mktemp; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "required release command is unavailable: $command" >&2
    exit 1
  fi
done

if [[ -z "${SOURCE_DATE_EPOCH:-}" ]]; then
  if ! command -v git >/dev/null 2>&1 ||
    ! SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD 2>/dev/null)"; then
    echo "SOURCE_DATE_EPOCH is required outside a Git checkout" >&2
    exit 1
  fi
fi
if [[ ! "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]]; then
  echo "SOURCE_DATE_EPOCH must be a non-negative integer" >&2
  exit 2
fi

release_version="${version#v}"
mkdir -p "$output_directory"
output_directory="$(cd "$output_directory" && pwd)"

archives=()
for architecture in amd64 arm64; do
  package_name="docklane_${release_version}_linux_${architecture}"
  archive_name="${package_name}.tar.gz"
  archive_path="${output_directory}/${archive_name}"
  if [[ -e "$archive_path" ]]; then
    echo "refusing to overwrite existing release artifact: $archive_path" >&2
    exit 1
  fi
  archives+=("$archive_name")
done
if [[ -e "${output_directory}/checksums.txt" ]]; then
  echo "refusing to overwrite existing release artifact: ${output_directory}/checksums.txt" >&2
  exit 1
fi

staging_root="$(mktemp -d)"
cleanup() {
  rm -rf -- "$staging_root"
}
trap cleanup EXIT

export LC_ALL=C
export TZ=UTC

for architecture in amd64 arm64; do
  package_name="docklane_${release_version}_linux_${architecture}"
  package_directory="${staging_root}/${package_name}"
  binary_path="${staging_root}/docklane-${architecture}"

  CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" GOFLAGS=-mod=readonly \
    go build \
      -trimpath \
      -buildvcs=false \
      -ldflags="-s -w -buildid= -X main.docklaneVersion=${version}" \
      -o "$binary_path" \
      ./cmd/docklane

  install -d -m 0755 "$package_directory"
  install -m 0755 "$binary_path" "${package_directory}/docklane"
  install -m 0644 LICENSE "${package_directory}/LICENSE"
  install -m 0644 README.md "${package_directory}/README.md"

  (
    cd "$staging_root"
    tar \
      --sort=name \
      --format=ustar \
      --owner=0 \
      --group=0 \
      --numeric-owner \
      --mtime="@${SOURCE_DATE_EPOCH}" \
      -cf - \
      "$package_name"
  ) | gzip -n -9 >"${output_directory}/${package_name}.tar.gz"
done

(
  cd "$output_directory"
  sha256sum "${archives[@]}" >checksums.txt
)

echo "Built Docklane ${version} release artifacts in ${output_directory}"
