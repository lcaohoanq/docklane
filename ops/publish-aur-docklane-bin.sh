#!/usr/bin/env bash

set -euo pipefail

package_directory="${1:-packaging/aur/docklane-bin}"
aur_repo_url="${AUR_REPO_URL:-ssh://aur@aur.archlinux.org/docklane-bin.git}"
dry_run="${AUR_DRY_RUN:-0}"

if [[ ! -f "${package_directory}/PKGBUILD" || ! -f "${package_directory}/.SRCINFO" || ! -f "${package_directory}/LICENSE" ]]; then
  echo "package directory must contain PKGBUILD, .SRCINFO, and LICENSE: ${package_directory}" >&2
  exit 2
fi

for command in git ssh; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "required command is unavailable: $command" >&2
    exit 1
  fi
done

pkgver="$(awk -F= '/^pkgver=/ { print $2; exit }' "${package_directory}/PKGBUILD")"
pkgrel="$(awk -F= '/^pkgrel=/ { print $2; exit }' "${package_directory}/PKGBUILD")"
if [[ -z "$pkgver" || -z "$pkgrel" ]]; then
  echo "failed to read pkgver/pkgrel from ${package_directory}/PKGBUILD" >&2
  exit 1
fi

work_root="$(mktemp -d)"
cleanup() {
  rm -rf -- "$work_root"
}
trap cleanup EXIT

clone_directory="${work_root}/docklane-bin"
GIT_SSH_COMMAND="${GIT_SSH_COMMAND:-ssh}"
export GIT_SSH_COMMAND

git -c init.defaultBranch=master clone --depth 1 "$aur_repo_url" "$clone_directory"

install -m 0644 "${package_directory}/PKGBUILD" "${clone_directory}/PKGBUILD"
install -m 0644 "${package_directory}/.SRCINFO" "${clone_directory}/.SRCINFO"
install -m 0644 "${package_directory}/LICENSE" "${clone_directory}/LICENSE"

git -C "$clone_directory" config user.name "${AUR_COMMIT_NAME:-lcaohoanq}"
git -C "$clone_directory" config user.email "${AUR_COMMIT_EMAIL:-hoangclw@gmail.com}"

git -C "$clone_directory" add PKGBUILD .SRCINFO LICENSE

if git -C "$clone_directory" diff --cached --quiet; then
  echo "AUR docklane-bin already at ${pkgver}-${pkgrel}; nothing to publish"
  exit 0
fi

git -C "$clone_directory" commit -m "Update to ${pkgver}-${pkgrel}"

if [[ "$dry_run" == "1" ]]; then
  echo "AUR dry run prepared commit Update to ${pkgver}-${pkgrel}"
  git -C "$clone_directory" show --stat --oneline HEAD
  exit 0
fi

git -C "$clone_directory" push origin HEAD:master
echo "Published docklane-bin ${pkgver}-${pkgrel} to the AUR"
