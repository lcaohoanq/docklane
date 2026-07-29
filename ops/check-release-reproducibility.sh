#!/usr/bin/env bash

set -euo pipefail

version="${1:-}"
if [[ -z "$version" ]]; then
  echo "usage: $0 vMAJOR.MINOR.PATCH[-PRERELEASE]" >&2
  exit 2
fi

comparison_root="$(mktemp -d)"
cleanup() {
  rm -rf -- "$comparison_root"
}
trap cleanup EXIT

first="${comparison_root}/first"
second="${comparison_root}/second"

"$(dirname "$0")/build-release.sh" "$version" "$first"
"$(dirname "$0")/build-release.sh" "$version" "$second"

for artifact in "$first"/*; do
  name="${artifact##*/}"
  cmp "$artifact" "${second}/${name}"
done

echo "Release artifacts are byte-for-byte reproducible for ${version}"
