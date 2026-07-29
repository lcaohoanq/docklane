#!/usr/bin/env bash

set -euo pipefail

version="${1:-}"
release_directory="${2:-}"
output_directory="${3:-}"

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]] ||
  [[ -z "$release_directory" || -z "$output_directory" ]]; then
  echo "usage: $0 vMAJOR.MINOR.PATCH[-PRERELEASE] RELEASE_DIRECTORY OUTPUT_DIRECTORY" >&2
  exit 2
fi

for command in install mktemp tar; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "required runtime-image command is unavailable: $command" >&2
    exit 1
  fi
done

if [[ -e "$output_directory" ]]; then
  echo "refusing to overwrite runtime-image context: $output_directory" >&2
  exit 1
fi

release_directory="$(cd "$release_directory" && pwd)"
output_parent="$(dirname "$output_directory")"
output_name="$(basename "$output_directory")"
if [[ ! -d "$output_parent" ]]; then
  install -d -m 0755 "$output_parent"
fi
output_parent="$(cd "$output_parent" && pwd)"

release_version="${version#v}"
staging_root="$(mktemp -d "${output_parent}/.${output_name}.XXXXXX")"
cleanup() {
  rm -rf -- "$staging_root"
}
trap cleanup EXIT

install -m 0644 ops/runtime-image.Dockerfile "${staging_root}/Dockerfile"
for architecture in amd64 arm64; do
  package_name="docklane_${release_version}_linux_${architecture}"
  archive="${release_directory}/${package_name}.tar.gz"
  if [[ ! -f "$archive" ]]; then
    echo "release archive is unavailable: $archive" >&2
    exit 1
  fi
  install -d -m 0755 "${staging_root}/linux/${architecture}"
  tar -xOf "$archive" "${package_name}/docklane" \
    >"${staging_root}/linux/${architecture}/docklane"
  chmod 0755 "${staging_root}/linux/${architecture}/docklane"
  if [[ "$architecture" == "amd64" ]] &&
    [[ "$("${staging_root}/linux/${architecture}/docklane" version)" != \
      "docklane ${version}" ]]; then
    echo "release binary does not report expected version ${version}" >&2
    exit 1
  fi
done

mv "$staging_root" "${output_parent}/${output_name}"
trap - EXIT
echo "Prepared multi-platform runtime-image context at ${output_parent}/${output_name}"
