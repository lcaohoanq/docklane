---
title: Configuration
description: Runtime paths and environment overrides used by Docklane.
sidebar:
  order: 2
---

Docklane favors explicit local paths and safe defaults.

| Setting | Default | Purpose |
| --- | --- | --- |
| Controller address | `127.0.0.1:4646` | UI and controller API. |
| Docker socket | `/var/run/docker.sock` | Local Docker Engine access. |
| Manifest | `/var/lib/docklane/install-manifest.json` | Managed-resource ownership. |
| Base domain | `docker.home.arpa` | Local DNS and TLS namespace. |
| Proxy network | `proxy` | Traefik-to-application traffic. |
| Control network | `docklane-control` | Private controller-to-Traefik traffic. |

Use `DOCKLANE_MANIFEST` to override the manifest path for development or
explicit recovery operations. Use `DOCKLANE_TRAEFIK_CA_FILE` when the trusted
Traefik CA certificate is installed at a non-default host path.

Do not expose the controller address on an untrusted interface.
