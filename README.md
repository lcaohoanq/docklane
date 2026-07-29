# Docklane

Docklane is a local container gateway that replaces host port numbers with
stable HTTPS names such as `excalidraw.docker.home.arpa`.

Project documentation:

- [Architecture](./docs/architecture.md)
- [Installation manifest schema v1](./docs/install-manifest-v1.md)
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
- a restricted proxy-network-only probe sidecar for direct upstream
  reachability diagnostics over a shared Unix socket;
- authenticated, route-scoped Traefik runtime inspection for provider, router,
  service, and backend status;
- an in-browser diagnostics view with grouped controller checks, repair
  guidance, copyable JSON, and a separately labeled browser HTTPS probe;
- bounded controller-health history sampled every five minutes and retained
  for 288 snapshots per route;
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
mkdir -p data
printf '%s' '<Traefik-dashboard-password>' > data/traefik-dashboard-password
chmod 600 data/traefik-dashboard-password
docker compose up -d
docker compose ps
```

The password file is runtime-only and ignored by Git. The integrated Compose
configuration trusts the host's local Traefik CA and connects directly to
Traefik over the private control network. Set `DOCKLANE_TRAEFIK_CA_FILE` when
the CA certificate is installed at a different host path.

Open <http://127.0.0.1:4646> for the UI, or use the same API through the CLI:

```sh
./bin/docklane discover
./bin/docklane doctor
./bin/docklane doctor excalidraw
./bin/docklane doctor --json excalidraw
./bin/docklane network plan
./bin/docklane network apply
./bin/docklane preflight
./bin/docklane preflight --json
./bin/docklane install --dry-run
./bin/docklane install --dry-run --json
# Apply only if the freshly generated token exactly matches the reviewed plan:
PLAN_TOKEN=copy-the-token-from-the-reviewed-output
./bin/docklane install --token "$PLAN_TOKEN"
./bin/docklane uninstall --dry-run
./bin/docklane uninstall --dry-run --json
ROLLBACK_TOKEN=copy-the-token-from-the-reviewed-uninstall-output
./bin/docklane uninstall --token "$ROLLBACK_TOKEN"
./bin/docklane manifest init --path /absolute/path/install-manifest.json
./bin/docklane manifest validate --path /absolute/path/install-manifest.json
./bin/docklane manifest show --path /absolute/path/install-manifest.json
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

The installation manifest defaults to
`/var/lib/docklane/install-manifest.json`; override it with
`DOCKLANE_MANIFEST` or `--path`. `manifest init` creates only the ownership
record and never changes DNS, certificates, trust, Docker, Traefik, or system
services. It refuses to overwrite an existing manifest.

`docklane preflight` is read-only. It checks gateway ports, Docker access,
proxy-network compatibility, dnsmasq configuration and service state, the
system resolver's real wildcard answer, existing Traefik candidates, and
manifest state. For an existing gateway it also follows Traefik's file-provider
wiring through read-only mounts, inventories the leaf certificate and key,
checks SANs and expiry, rejects broad private-key permissions, verifies the
issuing trust anchor, and confirms that port 443 serves that exact certificate.
It also inventories the Docklane controller and restricted probe, requiring
healthy matching images, loopback-only controller publishing, isolated
networks, read-only root filesystems, restricted capabilities, and the expected
data/socket mounts.
Warnings describe work for a future reviewed install plan; blocking conflicts
return a non-zero exit status.

`docklane install --dry-run` turns the structured preflight inventory into a
tokened ownership plan without creating the manifest or changing the host.
The foundation planner covers Traefik, proxy and control networks, dnsmasq,
resolver behavior, verified TLS/trust ownership, and the complete Docklane
controller/probe runtime. Existing runtime images and TLS files are
fingerprinted; containers, networks, the probe socket volume, and the data
directory receive explicit preserve/remove ownership. The reviewed plan now
has complete resource coverage.

When a plan contains managed resources, it also carries a validated clean
installation specification. The specification pins the state, data, PKI,
Traefik, dnsmasq, and trust-anchor paths; explicit Traefik and Docklane image
references; certificate SAN/lifetime/key settings; and the full
gateway/controller/probe topology. Mutable state is constrained below the
dedicated `--managed-state-dir` (default `/var/lib/docklane`). Pure adoption
plans omit this managed contract.

The same managed plan includes a deterministic artifact bundle. It renders the
exact dnsmasq rule, Traefik dynamic TLS/dashboard configuration, and three
container specifications with SHA-256 fingerprints. PKI keys, certificates,
and dashboard credentials appear only as permission-constrained
`generatedAtApply` descriptors; dry-run never generates or prints private
material. The PKI generator creates a 3072-bit local root and leaf entirely in
memory, verifies the apex and wildcard SANs against that root, and returns the
bundle for a future atomic writer.

Apply-time materialization now produces the complete ten-file bundle in
memory. The dashboard password uses 256 bits of entropy and URL-safe encoding;
Traefik receives only a bcrypt users file while the controller receives the
mode-`0600` raw password file. A reversible file stager writes each target by
temporary file, file sync, atomic rename, and directory sync. Existing regular
files receive fingerprinted, mode-preserving backups, and any failure restores
earlier replacements and removes files created by that transaction. Managed
rollback first verifies that staged content and permissions have not changed,
so it will not overwrite a later external edit.

The Docker transaction layer now derives strict create requests for the proxy
and private control networks, probe socket volume, and probe/controller/gateway
containers. Every object receives managed, schema, role, and installation-ID
labels. It refuses pre-existing names, verifies inspected topology, mounts,
ports, commands, and security settings after creation and startup, and records
the exact returned object IDs. Failures remove containers, volume, and networks
in reverse dependency order. Rollback compares current objects with their
post-create snapshots and refuses deletion after configuration or ownership
drift; volatile running/health changes do not prevent cleanup.

The Arch and Debian host-integration profiles are transactional and selected
automatically (or explicitly with `--host-profile`). Their reviewed artifacts
include the dnsmasq wildcard rule, platform-native trust anchor, and an exact
systemd-resolved route-only domain drop-in (customizable with
`--managed-resolver-config`). Debian uses `update-ca-certificates` and binds
dnsmasq explicitly to `127.0.0.1`; Arch uses p11-kit. Apply snapshots dnsmasq
and systemd-resolved state, validates effective dnsmasq configuration,
refreshes the selected trust store, activates both services,
flushes caches, then verifies apex/wildcard loopback DNS and the installed CA.
Rollback refuses service drift, restores files first, refreshes trust, and
returns both services to their exact prior active/inactive states. Managed
`docklane install --token TOKEN` reruns preflight and planning, then
applies only if the supplied token exactly matches the fresh plan. Adoption
records verified resources without changing them. Managed installation uses
durable private-material and per-resource execution journals across files,
directories, host integration, and Docker. Repeating the command with the
original token resumes an interrupted managed installation.

`docklane uninstall --dry-run` reads the installed ownership manifest and
renders its exact inverse in reverse dependency order. Adopted resources are
shown as non-mutating `preserve`; Docklane-managed resources become `remove`
or fingerprint-backed `restore` operations according to their recorded
rollback contract. A deterministic token binds the preview to the manifest
installation ID and generation. `docklane uninstall --token TOKEN` executes
that inverse through the installed journal and resumes with the same token
after interruption. Adopted resources are untouched. Non-empty controller data
is retained after its Docklane ownership marker is released, and the rolled
back manifest remains as a private audit tombstone.

`docklane doctor` checks controller, reconciliation, provider, and Docker
discovery health. Supplying a route ID, name, or full hostname also checks the
saved route, workload selector, declared upstream port, shared network, local
network alias, local DNS, TCP 80/443, HTTP-to-HTTPS redirect, trusted
certificate/SAN/expiry, and final HTTPS response. The probes are read-only and
use the relevant perspective: browser-facing checks run from the CLI machine,
while direct upstream checks run from the shared proxy network.
Traefik runtime checks use its authenticated API to verify that the HTTP
provider has produced the expected router, service, and `UP` backend.
The web UI uses the route readiness endpoint to keep a hostname non-clickable
while Docklane reconciles and Traefik activates it. The link unlocks only after
the router, service, and an `UP` backend are confirmed; a 30-second wait turns
into a diagnostic prompt instead of sending the browser to a transient 404.

Current API endpoints:

- `GET /api/v1/health`
- `GET /api/v1/containers`
- `GET /api/v1/network/plan`
- `POST /api/v1/network/apply`
- `GET /api/v1/routes`
- `POST /api/v1/routes`
- `GET /api/v1/routes/{id}`
- `GET /api/v1/routes/{id}/readiness`
- `GET /api/v1/routes/{id}/upstream-probe`
- `GET /api/v1/routes/{id}/traefik-runtime`
- `GET /api/v1/diagnostics/routes/{id}`
- `GET /api/v1/diagnostics/routes/{id}/history`
- `PUT /api/v1/routes/{id}`
- `DELETE /api/v1/routes/{id}`
- `GET /internal/traefik`

Pass `networkAliases=true` to the containers endpoint when diagnostics need
verified aliases on the configured proxy network. Ordinary UI discovery omits
the additional Docker inspect calls.

Manual diagnoses and periodic controller samples are stored in SQLite.
`--health-history-interval` and `--health-history-limit` control cadence and
the hard per-route retention cap.

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
