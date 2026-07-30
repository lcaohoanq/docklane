---
title: Plan application networking
description: Connect workloads to the shared proxy network without publishing host ports.
sidebar:
  order: 4
---

Inspect the proposed network changes:

```sh
docklane network plan
```

Apply the reviewed network plan:

```sh
docklane network apply
```

Application containers keep their HTTP listeners internal. Traefik and routed
applications share the `proxy` network, while Docklane controller traffic uses
the private `docklane-control` network.

Docklane detects the active gateway to avoid routing Traefik back to itself.
It also detects hostname collisions with Docker-label routes and will not
silently replace an existing owner.
