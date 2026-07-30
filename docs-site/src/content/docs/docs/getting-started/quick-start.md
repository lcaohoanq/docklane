---
title: Quick start
description: Download, verify, review, and install Docklane.
sidebar:
  order: 2
---

This guide installs `v0.1.0-alpha.2` on a supported development host.

## Download and verify

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
```

Never install an archive when its checksum fails.

## Review and install

```sh
sudo install -m 0755 docklane /usr/local/bin/docklane
docklane preflight
docklane install --dry-run
```

The dry run returns a plan token. Apply only the plan you just reviewed:

```sh
sudo docklane install --token 'COPY_THE_REVIEWED_TOKEN'
```

Open `http://127.0.0.1:4646`, choose a discovered workload, and assign its
internal HTTP port a local name.

## Verify

```sh
docklane discover
docklane doctor
curl -I https://YOUR_ROUTE.docker.home.arpa
```

Continue with the [complete installation guide](./installation/) for
distribution prerequisites, expected changes, and rollback instructions.
