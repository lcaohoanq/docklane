---
title: Failure handling
description: How Docklane reports unsafe or incomplete state without guessing.
sidebar:
  order: 3
---

Observed route state is separate from saved configuration. A route can be:

- `ready`: the requested revision is active in Traefik.
- `disabled`: intentionally excluded.
- `unresolved`: no workload matches the selector.
- `ambiguous`: more than one workload matches.
- `error`: validation, publication, or runtime verification failed.

Readiness adds transitional states for reconciliation, publishing, and
verification. Clients poll by route revision so an old successful result cannot
make a newer configuration appear ready.

Diagnostics group actionable checks by Docker, DNS, TLS, Traefik, network, and
upstream layer. Suggested repairs explain the next safe observation or command
instead of automatically changing an unrelated subsystem.
