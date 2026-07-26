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
- numbered SQLite migrations with a protected pre-migration snapshot;
- observed `ready`, `disabled`, `unresolved`, `ambiguous`, and `error` states;
- immediate reconciliation after mutations, debounced Docker lifecycle events,
  and periodic recovery reconciliation;
- controller reconciliation health and last-error reporting;
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

Current API endpoints:

- `GET /api/v1/health`
- `GET /api/v1/containers`
- `GET /api/v1/routes`
- `POST /api/v1/routes`
- `GET /api/v1/routes/{id}`
- `PUT /api/v1/routes/{id}`
- `DELETE /api/v1/routes/{id}`
- `GET /internal/traefik`

## Safety boundary

The controller currently reads Docker state through `/var/run/docker.sock`.
Route creation writes only to Docklane's own SQLite database.

The current container mounts the Docker socket read-only. Automatic network
attachment is therefore not enabled yet; a routed application must already be
on the shared `proxy` network. Docklane administration is published only on
host loopback, and Traefik reaches its provider through the separate private
`docklane-control` network. Certificate and DNS installation sources live
under `ops/`, and the active rollback procedure is documented in
[ops/rollback.md](./ops/rollback.md).
