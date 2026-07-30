---
title: Requirements
description: Supported hosts and prerequisites for the current Docklane alpha.
sidebar:
  order: 1
---

## Supported hosts

The current alpha supports:

- Debian 12 or Arch Linux with systemd.
- Docker Engine through `/var/run/docker.sock`.
- `dnsmasq` with `systemd-resolved`.
- Linux `amd64` and `arm64`.
- Free host ports 80 and 443, unless Docklane can safely adopt the existing
  Traefik gateway.

Rootless Docker, remote engines, non-systemd resolvers, Kubernetes, public DNS,
and purchased domains are not supported.

## Privilege boundary

Docker socket access is equivalent to root-level control of the host. Review
the output of `docklane preflight` and `docklane install --dry-run` before
applying a plan.

Docklane records every owned or adopted resource in
`/var/lib/docklane/install-manifest.json` so uninstall and upgrade operations
can make ownership-aware decisions.
