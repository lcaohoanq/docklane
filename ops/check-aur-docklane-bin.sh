#!/usr/bin/env bash

set -euo pipefail

root_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
package_directory="${root_directory}/packaging/aur/docklane-bin"
pkgbuild_path="${package_directory}/PKGBUILD"

if [[ ! -f "$pkgbuild_path" ]]; then
  echo "PKGBUILD not found: ${pkgbuild_path}" >&2
  exit 1
fi

upstream_version="$(awk -F"'" '/^_upstream_version=/ { print $2; exit }' "$pkgbuild_path")"
amd64_checksum="$(awk -F"'" '/^sha256sums_x86_64=/{getline; print $2; exit}' "$pkgbuild_path")"
arm64_checksum="$(awk -F"'" '/^sha256sums_aarch64=/{getline; print $2; exit}' "$pkgbuild_path")"

if [[ -z "$upstream_version" || -z "$amd64_checksum" || -z "$arm64_checksum" ]]; then
  echo "failed to parse packaging metadata from ${pkgbuild_path}" >&2
  exit 1
fi

version="v${upstream_version}"

work_root="$(mktemp -d)"
cleanup() {
  rm -rf -- "$work_root"
}
trap cleanup EXIT

checksums_file="${work_root}/checksums.txt"
cat >"$checksums_file" <<EOF
${amd64_checksum}  docklane_${upstream_version}_linux_amd64.tar.gz
${arm64_checksum}  docklane_${upstream_version}_linux_arm64.tar.gz
EOF

mkdir -p "${work_root}/pkg"
cp "$pkgbuild_path" "${work_root}/pkg/PKGBUILD"

"${root_directory}/ops/bump-aur-docklane-bin.sh" \
  "$version" \
  "$checksums_file" \
  "${work_root}/pkg"

diff -u "${package_directory}/PKGBUILD" "${work_root}/pkg/PKGBUILD"
diff -u "${package_directory}/.SRCINFO" "${work_root}/pkg/.SRCINFO"

git init --bare "${work_root}/aur.git" >/dev/null
git clone "${work_root}/aur.git" "${work_root}/seed" >/dev/null
cp \
  "${package_directory}/PKGBUILD" \
  "${package_directory}/.SRCINFO" \
  "${package_directory}/LICENSE" \
  "${work_root}/seed/"
git -C "${work_root}/seed" add PKGBUILD .SRCINFO LICENSE
git -C "${work_root}/seed" \
  -c user.name=aur-check \
  -c user.email=aur-check@docklane.local \
  commit -m "seed docklane-bin ${upstream_version}" >/dev/null
git -C "${work_root}/seed" push origin HEAD:master >/dev/null

AUR_DRY_RUN=1 \
  AUR_REPO_URL="${work_root}/aur.git" \
  "${root_directory}/ops/publish-aur-docklane-bin.sh" \
  "$package_directory" >/dev/null

echo "AUR docklane-bin packaging checks passed for ${upstream_version}"
