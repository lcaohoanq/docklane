# Docklane

Docklane is a local container gateway that replaces host port numbers with
stable HTTPS names such as `excalidraw.docker.home.arpa`.

Project documentation:

- [Architecture](./docs/architecture.md)
- [Implementation plan and task tracker](./docs/plans.md)

The Phase 1 prototype provides:

- discovery of running Docker and Compose containers;
- durable route selectors based on Compose project and service labels;
- route persistence in SQLite;
- one controller API shared by the CLI and Svelte UI;
- complete Traefik HTTP-provider JSON generated from current container state;
- a Svelte UI embedded in the Go binary.

The current integrated checkpoint additionally provides:

- route edit, enable, disable, and delete through API, CLI, and UI;
- revision-checked updates that reject stale browser or CLI writes;
- declared-port validation that keeps invalid upstreams out of Traefik;
- active-gateway detection that prevents reverse-proxy self-routing loops;
- Docker-label hostname collision detection with disabled-route shadow
  migration support;
- numbered SQLite migrations with a protected pre-migration snapshot;
- ownership-tracked proxy-network attachment and safe detachment for enabled
  routes without host ports;
- deterministic `docklane-route-<id>` network aliases that survive Compose
  container recreation;
- observed `ready`, `disabled`, `unresolved`, `ambiguous`, and `error` states;
- immediate reconciliation after mutations, debounced Docker lifecycle events,
  and periodic recovery reconciliation;
- controller reconciliation health and last-error reporting;
- validated Traefik provider documents with a persisted last-known-good
  snapshot and provider source/error reporting in controller health;
- a managed Docklane container on a private `docklane-control` network shared
  only with Traefik;
- Traefik HTTP-provider polling every two seconds;
- wildcard DNS for `*.docker.home.arpa`;
- a trusted leaf certificate covering `docker.home.arpa` and
  `*.docker.home.arpa`.

The active integration is installed on this machine. Docklane owns its
container and provider configuration, while existing Docker-label routes and
application networks remain in place.

## Build

Requirements:

- Go 1.26 or newer
- pnpm
- Docker Engine

```sh
make setup
make test
make build
```

The binary is written to `bin/docklane`.

## Run Docklane

For isolated source development:

```sh
./bin/docklane serve
```

For the integrated service:

```sh
docker compose up -d
docker compose ps
```

Open <http://127.0.0.1:4646> for the UI, or use the same API through the CLI:

```sh
./bin/docklane discover
./bin/docklane doctor
./bin/docklane doctor excalidraw
./bin/docklane doctor --json excalidraw
./bin/docklane network plan
./bin/docklane network apply
./bin/docklane route add excalidraw \
  --project excalidraw \
  --service excalidraw \
  --port 80
./bin/docklane route list
./bin/docklane route edit 1 --name canvas
./bin/docklane route disable 1
./bin/docklane route enable 1
./bin/docklane route delete 1
```

Use `--dry-run` on `route add` to validate and print the route without saving
it. Set `DOCKLANE_URL` when the controller is not at
`http://127.0.0.1:4646`.

`docklane doctor` checks controller, reconciliation, provider, and Docker
discovery health. Supplying a route ID, name, or full hostname also checks the
saved route, workload selector, declared upstream port, shared network, local
network alias, local DNS, TCP 80/443, HTTP-to-HTTPS redirect, trusted
certificate/SAN/expiry, and final HTTPS response. The probes are read-only and
run from the CLI machine so their network and trust perspective matches the
browser.

Current API endpoints:

- `GET /api/v1/health`
- `GET /api/v1/containers`
- `GET /api/v1/network/plan`
- `POST /api/v1/network/apply`
- `GET /api/v1/routes`
- `POST /api/v1/routes`
- `GET /api/v1/routes/{id}`
- `PUT /api/v1/routes/{id}`
- `DELETE /api/v1/routes/{id}`
- `GET /internal/traefik`

Pass `networkAliases=true` to the containers endpoint when diagnostics need
verified aliases on the configured proxy network. Ordinary UI discovery omits
the additional Docker inspect calls.

`GET /api/v1/health` includes a `provider` object. Its `source` is `live`,
`last-known-good`, `awaiting-first-poll`, or `unavailable`. A
`last-known-good` response means Docklane could not render current Docker
state, but Traefik is still receiving the most recently validated complete
configuration.

## Safety boundary

The controller reads Docker state and manages selected proxy-network
attachments through `/var/run/docker.sock`. Route creation writes desired
state and attachment ownership to Docklane's SQLite database.

`docklane network plan` is read-only and shows network creation, application
connects, owned disconnects, and alias repairs. Each plan has a content token;
apply rejects the request if Docker or desired route state changed after the
preview. The CLI requires `--yes` when the reviewed plan contains a
destructive disconnect.

The current container bind-mounts the Docker socket with a read-only filesystem
flag, but Docker API access through that socket still grants host-level
container authority. The integrated deployment attaches routed workloads to
the shared `proxy` network without removing their existing networks. Docklane
records each attachment it creates and disconnects only those recorded
attachments after no enabled route needs them. Pre-existing network membership
is never claimed or removed.

The active `proxy` network predates Docklane and remains externally owned.
Networks created explicitly through Docklane use ownership labels and can be
distinguished from compatible external networks without relabeling either.

Docklane administration is published only on host loopback, and Traefik
reaches its provider through the separate private `docklane-control` network.
Certificate and DNS installation sources live under `ops/`, and the active
rollback procedure is documented in [ops/rollback.md](./ops/rollback.md).
