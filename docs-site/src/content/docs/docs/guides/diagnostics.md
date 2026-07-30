---
title: Diagnose a route
description: Find failures across DNS, TLS, Traefik, Docker networking, and the upstream.
sidebar:
  order: 2
---

Run diagnostics for all routes:

```sh
docklane doctor
```

Target one route and request machine-readable output:

```sh
docklane doctor excalidraw
docklane doctor --json excalidraw
```

## Diagnostic layers

Docklane checks each layer separately:

1. Route configuration and workload selection.
2. Docker network attachment and internal port.
3. Generated Traefik provider configuration.
4. Traefik router, service, and backend runtime state.
5. Local wildcard DNS.
6. Certificate validity and machine trust.
7. Direct upstream reachability through the restricted probe.

The UI labels controller checks separately from the browser HTTPS probe because
they use different DNS, network, and certificate trust contexts.

## Health history

Controller health is sampled every five minutes and retained for 288 snapshots
per route. Use the timeline to distinguish a persistent configuration error
from a transient container restart.
