#!/usr/bin/env bash

set -euo pipefail

version="${1:-}"
checksums_file="${2:-}"
package_directory="${3:-packaging/aur/docklane-bin}"

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  echo "usage: $0 vMAJOR.MINOR.PATCH[-PRERELEASE] CHECKSUMS.txt [PACKAGE_DIRECTORY]" >&2
  exit 2
fi

if [[ -z "$checksums_file" || ! -f "$checksums_file" ]]; then
  echo "checksums file is required: $checksums_file" >&2
  exit 2
fi

if [[ ! -f "${package_directory}/PKGBUILD" ]]; then
  echo "PKGBUILD not found in ${package_directory}" >&2
  exit 2
fi

upstream_version="${version#v}"
pkgver="${upstream_version//-/_}"
amd64_archive="docklane_${upstream_version}_linux_amd64.tar.gz"
arm64_archive="docklane_${upstream_version}_linux_arm64.tar.gz"

checksum_for() {
  local archive="$1"
  local checksum

  checksum="$(awk -v archive="$archive" '$2 == archive { print $1; found=1 } END { exit !found }' "$checksums_file")" || {
    echo "missing checksum for ${archive} in ${checksums_file}" >&2
    exit 1
  }
  if [[ ! "$checksum" =~ ^[0-9a-f]{64}$ ]]; then
    echo "invalid sha256 for ${archive}: ${checksum}" >&2
    exit 1
  fi
  printf '%s\n' "$checksum"
}

amd64_checksum="$(checksum_for "$amd64_archive")"
arm64_checksum="$(checksum_for "$arm64_archive")"

pkgbuild_path="${package_directory}/PKGBUILD"
srcinfo_path="${package_directory}/.SRCINFO"

cat >"$pkgbuild_path" <<EOF
# Maintainer: lcaohoanq <hoangclw@gmail.com>

pkgname=docklane-bin
pkgver=${pkgver}
pkgrel=1
pkgdesc='Local HTTPS gateway for Docker containers using Traefik'
arch=('x86_64' 'aarch64')
url='https://github.com/lcaohoanq/docklane'
license=('Apache-2.0')
depends=('ca-certificates-utils' 'dnsmasq' 'docker' 'systemd')
provides=("docklane=\${pkgver}")
conflicts=('docklane')
options=('!strip' '!debug')

_upstream_version='${upstream_version}'

source_x86_64=(
  "\${pkgname}-\${pkgver}-x86_64.tar.gz::\${url}/releases/download/v\${_upstream_version}/docklane_\${_upstream_version}_linux_amd64.tar.gz"
)
source_aarch64=(
  "\${pkgname}-\${pkgver}-aarch64.tar.gz::\${url}/releases/download/v\${_upstream_version}/docklane_\${_upstream_version}_linux_arm64.tar.gz"
)

sha256sums_x86_64=(
  '${amd64_checksum}'
)
sha256sums_aarch64=(
  '${arm64_checksum}'
)

package() {
  local release_arch

  case "\$CARCH" in
    x86_64) release_arch=amd64 ;;
    aarch64) release_arch=arm64 ;;
  esac

  cd "\${srcdir}/docklane_\${_upstream_version}_linux_\${release_arch}"

  install -Dm755 docklane "\${pkgdir}/usr/bin/docklane"
  install -Dm644 LICENSE \\
    "\${pkgdir}/usr/share/licenses/\${pkgname}/LICENSE"
  install -Dm644 README.md \\
    "\${pkgdir}/usr/share/doc/\${pkgname}/README.md"
}
EOF

cat >"$srcinfo_path" <<EOF
pkgbase = docklane-bin
	pkgdesc = Local HTTPS gateway for Docker containers using Traefik
	pkgver = ${pkgver}
	pkgrel = 1
	url = https://github.com/lcaohoanq/docklane
	arch = x86_64
	arch = aarch64
	license = Apache-2.0
	depends = ca-certificates-utils
	depends = dnsmasq
	depends = docker
	depends = systemd
	provides = docklane=${pkgver}
	conflicts = docklane
	options = !strip
	options = !debug
	source_x86_64 = docklane-bin-${pkgver}-x86_64.tar.gz::https://github.com/lcaohoanq/docklane/releases/download/v${upstream_version}/docklane_${upstream_version}_linux_amd64.tar.gz
	sha256sums_x86_64 = ${amd64_checksum}
	source_aarch64 = docklane-bin-${pkgver}-aarch64.tar.gz::https://github.com/lcaohoanq/docklane/releases/download/v${upstream_version}/docklane_${upstream_version}_linux_arm64.tar.gz
	sha256sums_aarch64 = ${arm64_checksum}

pkgname = docklane-bin
EOF

echo "Updated ${package_directory} for docklane-bin ${pkgver}-1"
