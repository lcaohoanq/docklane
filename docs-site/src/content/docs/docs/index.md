---
title: Docklane documentation
description: Install, operate, and understand the Docklane local container gateway.
---

Docklane replaces local host port numbers with stable HTTPS names such as
`excalidraw.docker.home.arpa`.

## Start here

1. Check the [host requirements](./getting-started/requirements/).
2. Follow the [quick start](./getting-started/quick-start/).
3. Learn how to [create and operate routes](./guides/routes/).
4. Use [diagnostics](./guides/diagnostics/) when a route is not ready.

:::caution[Alpha software]
Docklane changes machine-level DNS, certificate trust, Docker networks, and
ports 80/443. Use it on a development machine or VM where those changes are
acceptable.
:::

## What Docklane manages

- Discovery of Docker and Compose workloads.
- Durable route selectors and reconciliation.
- A complete Traefik HTTP-provider document.
- Local wildcard DNS for `*.docker.home.arpa`.
- A machine-local certificate authority and trusted HTTPS certificate.
- An installation manifest that records owned and adopted resources.

Docklane does not expose applications to the public internet and does not
replace Docker Compose.
