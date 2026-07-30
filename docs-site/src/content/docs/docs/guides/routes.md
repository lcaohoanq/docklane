---
title: Create and manage routes
description: Assign stable local HTTPS names to Docker workloads.
sidebar:
  order: 1
---

A route maps one local hostname to one Compose workload and internal listener.
It stores a durable selector instead of an ephemeral container ID.

## Discover workloads

```sh
docklane discover
```

The web UI at `http://127.0.0.1:4646` shows the same discovery result. Choose a
workload, select one of its declared internal ports, and assign a local name.

Common HTTP ports are suggested, but the selected port must be declared by the
container. Choose `https` only when the upstream itself serves TLS.

## Lifecycle

Routes can be edited, disabled, enabled, and deleted through the UI or CLI.
Each update carries a revision so stale browser or CLI writes are rejected.

An enabled route moves through reconciliation, publishing, verification, and
ready states. A route that cannot resolve to exactly one workload is omitted
from Traefik instead of guessing an upstream.

## Application networking

Docklane can attach a workload to the shared `proxy` network and assigns a
deterministic network alias. It records attachments that it owns and removes
only those attachments during disable or uninstall operations.
