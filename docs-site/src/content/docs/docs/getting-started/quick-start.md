---
title: Quick start
description: Download, verify, review, and install Docklane.
sidebar:
  order: 2
---

This guide installs `v0.1.0-alpha.2` on a supported development host.

## Install on Arch Linux from the AUR

Install the release binary and its host dependencies with an AUR helper:

```sh
yay -S docklane-bin
sudo systemctl enable --now docker systemd-resolved
docklane version
```

The AUR package installs the `docklane` command and required packages. It does
not apply Docklane's DNS, certificate, Traefik, or container configuration.
Continue to [Review and install](#review-and-install) to complete setup.

## Install from the release archive

Use the portable release archive on Debian, or on Arch Linux when you do not
want to use an AUR helper:

```sh
set -euo pipefail
VERSION=v0.1.0-alpha.2
RELEASE_VERSION="${VERSION#v}"

case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64) ARCH=arm64 ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac

BASE_URL="https://github.com/lcaohoanq/docklane/releases/download/${VERSION}"
ARCHIVE="docklane_${RELEASE_VERSION}_linux_${ARCH}.tar.gz"

curl --fail --location --remote-name "${BASE_URL}/${ARCHIVE}"
curl --fail --location --remote-name "${BASE_URL}/checksums.txt"
grep " ${ARCHIVE}$" checksums.txt | sha256sum --check --strict
tar -xzf "${ARCHIVE}"
cd "docklane_${RELEASE_VERSION}_linux_${ARCH}"
./docklane version
sudo install -m 0755 docklane /usr/local/bin/docklane
```

Never install an archive when its checksum fails.

## Review and install

```sh
sudo docklane preflight
sudo docklane install --dry-run
```

Running the review commands with `sudo` gives Docklane access to the Docker
socket without adding your account to the `docker` group. The dry run returns
a plan token. Apply only the plan you just reviewed:

```sh
sudo docklane install --token 'COPY_THE_REVIEWED_TOKEN'
```

Open `http://127.0.0.1:4646`, choose an eligible discovered workload, and
assign its internal HTTP port a local name. System workloads and containers
without declared TCP ports remain visible but do not offer route creation.

## Verify

```sh
docklane discover
docklane doctor
curl -I https://YOUR_ROUTE.docker.home.arpa
```

Continue with the [complete installation guide](./installation/) for
distribution prerequisites, expected changes, and rollback instructions.
