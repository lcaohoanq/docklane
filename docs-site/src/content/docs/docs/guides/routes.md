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

The web UI at `http://127.0.0.1:4646` shows the same discovery result, including
containers that are not route candidates. It separates workloads available for
routing from a read-only system/unavailable inventory. Choose an eligible
workload, select one of its declared internal ports, and assign a local name.

The UI uses stable paths: `/routes` opens saved routes and `/containers` opens
Docker discovery. Refreshing the browser and using Back or Forward preserves
the selected view.

Common HTTP ports are suggested, but the selected port must be declared by the
container. Choose `https` only when the upstream itself serves TLS.

Prefer Compose `expose` when an image does not already declare its internal
listener. It adds Docker port metadata without publishing the port on the host:

```yaml
services:
  actual_server:
    image: docker.io/actualbudget/actual-server:latest
    expose:
      - "5006"
```

You do not need `ports: ["5006:5006"]` for Docklane. If the image already has
`EXPOSE 5006`, the Compose `expose` entry is optional, though keeping it can
make the deployment's routing intent explicit. Neither form starts a listener;
the application must bind the declared port on `0.0.0.0` inside the container.

Docklane does not offer route creation for its controller, probe, or gateway
system workloads, or for containers that declare no TCP ports. These
containers remain visible so discovery still reflects Docker accurately. Port
metadata does not identify the application protocol, so infrastructure
containers with a declared TCP port can opt out explicitly:

```yaml
services:
  database:
    labels:
      com.docklane.route: "false"
```

The controller enforces the same eligibility rules for UI and API requests;
hiding the UI action is not the security boundary.

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
