---
title: Security boundaries
description: Understand privileged operations and deliberately constrained components.
sidebar:
  order: 3
---

## Docker access

Access to `/var/run/docker.sock` grants root-equivalent control. Treat the
Docklane controller as privileged software and expose its API only on the local
loopback interface.

## Restricted probe

Direct upstream reachability checks run through a dedicated probe sidecar. The
probe is attached only to the proxy network and communicates through a scoped
Unix socket; it does not receive the Docker socket.

## Ownership

Machine-level changes are planned before application and recorded in the
installation manifest. Uninstall and upgrade operations distinguish resources
created by Docklane from resources it adopted.

## Fail closed

Ambiguous, unresolved, invalid, or self-referential routes are excluded from
the generated Traefik configuration. The last validated provider snapshot
remains available when a new candidate is unsafe.
