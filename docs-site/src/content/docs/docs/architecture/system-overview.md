---
title: System overview
description: Components, trust boundaries, and data flow inside Docklane.
sidebar:
  order: 1
---

```mermaid
flowchart LR
  Browser["Browser / curl"] --> DNS["dnsmasq + system resolver"]
  Browser --> Traefik["Traefik"]

  CLI["docklane CLI"] --> Controller["Docklane controller"]
  UI["Svelte UI"] --> Controller

  Controller --> DB[("SQLite")]
  Controller --> Docker["Docker Engine"]
  Controller --> DNS
  Controller --> Traefik

  Traefik --> Apps["Application containers"]
```

The controller is the source of route intent and observed state. Traefik owns
the live HTTP data plane. Docker and host configuration are privileged external
systems reconciled through explicit plans and ownership records.

The UI is compiled into the Go binary, so the controller and interface ship as
one versioned artifact.
