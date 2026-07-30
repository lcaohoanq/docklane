---
title: Control plane
description: Persistence, reconciliation, and provider publication.
sidebar:
  order: 2
---

Route configuration is stored in SQLite with revision-checked updates.
Reconciliation runs after mutations, on debounced Docker lifecycle events, and
periodically for recovery.

For each enabled route the controller:

1. Resolves the durable Compose selector.
2. Validates the declared internal port and rejects gateway self-routing.
3. Ensures the owned proxy-network attachment and deterministic alias.
4. Builds a complete Traefik router, service, and backend configuration.
5. Validates and publishes the candidate provider document.
6. Inspects Traefik runtime state until the matching revision is ready.

Invalid routes are reported but omitted from the provider document. A persisted
last-known-good snapshot protects unrelated routes from a bad candidate.
