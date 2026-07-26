# Docklane Implementation Plan

Last updated: 2026-07-26

This file is the project task tracker. Architecture and design rationale live
in [architecture.md](./architecture.md).

## Status legend

- `[x]` Complete and verified
- `[-]` Implemented partially or still being validated
- `[ ]` Not started
- `[!]` Blocked or requires an explicit decision

## Definition of done

A task is complete only when:

1. implementation and relevant tests are present;
2. failure behavior is defined;
3. user-facing behavior is documented;
4. machine-level changes have preview and rollback paths;
5. the active local gateway still passes its smoke checks.

## Current summary

| Phase | Scope | Status |
| --- | --- | --- |
| 0 | Product and architecture decisions | Complete |
| 1 | Non-disruptive controller prototype | Complete |
| 2 | Route lifecycle and reconciliation reliability | In progress |
| 3 | Managed Docker network and Traefik integration | In progress |
| 4 | Local DNS and TLS lifecycle | Integrated checkpoint complete |
| 5 | Installer, migration, and rollback | Planned |
| 6 | Diagnostics and observability | Planned |
| 7 | UX/DX hardening and first release | Planned |

## Phase 0 — Product foundation

Goal: establish the problem, boundaries, and durable technical direction.

- [x] Choose the project name: Docklane.
- [x] Define the local-only scope.
- [x] Select Traefik as the global reverse proxy.
- [x] Select Go for the controller and CLI.
- [x] Select Svelte for the embedded web UI.
- [x] Select SQLite for controller-owned state.
- [x] Choose a shared controller API for CLI and web.
- [x] Choose Traefik's HTTP provider as the target central configuration path.
- [x] Choose stable Compose project/service selectors.
- [x] Choose `docker.home.arpa` as the target namespace.
- [x] Document Docker socket access as a privileged security boundary.
- [x] Establish the rule that the active gateway is not migrated during
  prototype development.

Acceptance criteria:

- [x] Architecture distinguishes control plane from request data plane.
- [x] Project goals, non-goals, invariants, and safety boundaries are written.

## Phase 1 — Non-disruptive controller prototype

Goal: validate the core route model without modifying Traefik, DNS,
certificates, or Docker networks.

### Backend

- [x] Scaffold the Go module and `docklane` command.
- [x] Add `serve`, `version`, and help commands.
- [x] Add validated runtime configuration.
- [x] Discover running containers through the Docker Unix socket.
- [x] Read Compose project/service labels and private TCP ports.
- [x] Define and validate the route domain model.
- [x] Persist routes in SQLite.
- [x] Resolve Compose selectors to current running containers.
- [x] Reject ambiguous workload resolution.
- [x] Generate complete Traefik HTTP-provider JSON.
- [x] Omit disabled and unresolved routes from generated configuration.
- [x] Add graceful controller shutdown.

### API and CLI

- [x] Add health, container discovery, route list/create, and provider
  endpoints.
- [x] Reject unknown route-create fields.
- [x] Default omitted route scheme to `http`.
- [x] Default omitted route enabled state to `true`.
- [x] Add `docklane discover`.
- [x] Add `docklane route list`.
- [x] Add `docklane route add`.
- [x] Add `--dry-run` and `--json`.
- [x] Support `DOCKLANE_URL`.

### Web UI

- [x] Scaffold Svelte and Vite.
- [x] Display discovered containers and internal ports.
- [x] Display saved routes.
- [x] Create a route from a selected container.
- [x] Show the equivalent CLI command.
- [x] Embed production UI assets in the Go binary.
- [x] Pin TypeScript 5.9 for current `svelte-check` compatibility.

### Quality and validation

- [x] Add domain validation tests.
- [x] Add Docker selector resolution tests.
- [x] Add SQLite persistence tests.
- [x] Add Traefik rendering tests.
- [x] Add API integration tests.
- [x] Add CLI client contract tests.
- [x] Add repeatable `make setup`, `make test`, and `make build` workflows.
- [x] Pass `go test ./...`.
- [x] Pass `go vet ./...`.
- [x] Pass `svelte-check` with zero warnings.
- [x] Build the embedded production UI and Go binary.
- [x] Verify live Docker discovery against the current machine.
- [x] Verify an isolated route produces the correct Traefik upstream.
- [x] Verify the existing `https://excalidraw.docker.lab` route remains HTTP
  200.
- [x] Stop the temporary validation controller.

Acceptance criteria:

- [x] A route can be created through CLI and observed through the UI/API.
- [x] Recreating a container does not change the stored Compose selector.
- [x] Traefik JSON uses the current resolved container instance.
- [x] No active system or proxy configuration is changed.

## Phase 2 — Route lifecycle and reconciliation reliability

Goal: complete controller-owned route management before introducing
machine-level mutations.

### Route lifecycle

- [x] Add `GET /api/v1/routes/{id}`.
- [x] Add route update/enable/disable.
- [x] Add route deletion.
- [x] Add matching CLI commands.
- [x] Add UI edit, enable/disable, and delete flows.
- [x] Add duplicate-name conflict errors with actionable messages.
- [x] Add optimistic concurrency or revision checking for updates.

### Observed state

- [ ] Define route states: `ready`, `unresolved`, `ambiguous`, `unreachable`,
  `disabled`, and `error`.
- [x] Return desired and observed state separately.
- [x] Validate that the configured internal port belongs to the selected
  workload.
- [x] Classify the active Traefik gateway as a managed system container.
- [x] Reject and omit routes that would send the gateway back into itself.
- [ ] Probe upstream reachability with bounded timeouts.
- [ ] Support non-Compose containers without pretending their ID is durable.
- [x] Define initial behavior for scaled Compose services: report ambiguous and
  omit the route until an explicit replica policy is implemented.
- [ ] Prefer deterministic managed aliases over generated container names.

### Reconciler

- [x] Add a periodic reconciliation loop.
- [x] Subscribe to Docker events for fast refresh.
- [x] Keep periodic reconciliation as recovery from missed events.
- [x] Debounce event bursts during Compose recreation.
- [x] Make each reconciliation operation idempotent.
- [x] Record the last successful reconcile time and last error.
- [ ] Add structured controller logs.
- [ ] Add Server-Sent Events for live UI updates.

### Persistence

- [x] Introduce numbered schema migrations.
- [x] Add database backup before migrations.
- [x] Add route revision storage.
- [ ] Add observed-status storage where appropriate.
- [ ] Define import/export schema version 1.
- [ ] Add `docklane export` and `docklane import --dry-run`.

Acceptance criteria:

- [x] Full route CRUD works through API, CLI, and UI.
- [x] Container recreation repairs a route without restarting Traefik.
- [x] Missed or absent Docker events are repaired by periodic reconciliation.
- [x] Ambiguous or missing workloads are visible and never published.
- [x] All work remains isolated from the active proxy and system DNS.

## Phase 3 — Managed network and Traefik integration

Goal: let global Traefik consume Docklane routes and reach selected containers
without published host ports.

### Managed Docker network

- [ ] Define the managed network name and labels.
- [ ] Preview network create/connect/disconnect operations.
- [ ] Create the network idempotently.
- [x] Attach selected containers without removing their existing networks.
- [ ] Assign a deterministic per-route network alias.
- [x] Track attachments created by Docklane.
- [x] Detach only Docklane-owned attachments when no route needs them.
- [x] Handle stopped and recreated containers.
- [x] Add integration tests using disposable Docker resources.

### Traefik provider

- [x] Add private controller/provider connectivity.
- [x] Configure Traefik HTTP-provider polling.
- [x] Keep the provider endpoint unavailable from application and non-local
  networks using host-loopback publication plus `docklane-control`.
- [ ] Add provider configuration validation before publish.
- [x] Test add, update, disable, delete, and recreate behavior.
- [ ] Test controller/provider outage behavior.
- [ ] Decide and implement last-known-good or file-provider fallback.
- [ ] Add route status showing whether Traefik loaded the desired revision.

### Migration safety

- [x] Snapshot the current Traefik, DNS, certificate, and Docklane database
  state.
- [x] Inventory the test workload's current Docker-label route and networks.
- [ ] Detect hostname collisions between labels and Docklane routes.
- [x] Support shadow rendering without activating routes.
- [x] Integrate one test workload first.
- [x] Document and preserve rollback to the original provider configuration.

Acceptance criteria:

- [x] A selected container is reachable through Traefik without a published
  application host port.
- [x] Route changes are observed without recreating the app or restarting
  Traefik.
- [x] Removing a route does not stop or otherwise modify the application.
- [ ] Provider failure has a tested and documented recovery behavior.

## Phase 4 — Local DNS and TLS lifecycle

Goal: make `https://<name>.docker.home.arpa` resolve locally with a trusted
certificate.

### DNS

- [x] Detect dnsmasq and the active system resolver manager.
- [x] Render the wildcard `docker.home.arpa` dnsmasq fragment.
- [x] Preview exact files and service operations before apply.
- [x] Validate configuration syntax before reload.
- [x] Configure split DNS for `docker.home.arpa`.
- [x] Explicitly handle systemd-resolved DNS-over-TLS on loopback.
- [-] Verify A/AAAA and HTTPS/SVCB lookup behavior. A resolution is verified;
  broader browser/resolver combinations remain for automated tests.
- [x] Add uninstall/rollback instructions for Docklane-owned DNS changes.

### TLS

- [x] Reuse the already trusted local root CA.
- [x] Protect CA and leaf private keys with restrictive permissions.
- [x] Generate a leaf certificate for `docker.home.arpa` and
  `*.docker.home.arpa`.
- [x] Preserve the existing explicitly installed system trust anchor.
- [x] Verify Chromium trust with a fresh browser profile.
- [x] Configure Traefik to serve the wildcard certificate.
- [ ] Track expiry and add safe certificate rotation.
- [x] Add certificate rollback from the timestamped backup.

Acceptance criteria:

- [x] An arbitrary one-label hostname resolves to `127.0.0.1`.
- [x] Browser and CLI hostname validation succeed without `-k`.
- [x] The served certificate contains the intended explicit SANs.
- [x] DNS and trust changes can be removed without affecting unrelated
  configuration.

## Phase 5 — Installer, migration, and rollback

Goal: construct the global reverse proxy first, then make application opt-in
safe and repeatable.

- [ ] Define a versioned installation manifest.
- [ ] Add `docklane install --dry-run`.
- [ ] Add `docklane install`.
- [ ] Add `docklane uninstall --dry-run`.
- [ ] Add `docklane uninstall`.
- [ ] Record every file, trust entry, Docker network, container, and service
  created by Docklane.
- [ ] Add preflight checks for ports 80/443, Docker access, dnsmasq, resolver
  conflicts, and existing Traefik.
- [ ] Support adopting a compatible existing global Traefik deployment.
- [ ] Refuse unsafe adoption with a precise explanation.
- [ ] Add `docklane app enable` for opt-in network attachment and routing.
- [ ] Add `docklane app disable` with safe detach behavior.
- [ ] Export copy-paste Compose guidance without editing user files.
- [ ] Add interrupted-install recovery.
- [ ] Add upgrade and schema migration flow.
- [ ] Exercise install/rollback in a disposable VM before host rollout.

Acceptance criteria:

- [ ] A clean machine can install the full local gateway from one reviewed
  plan.
- [ ] An existing compatible Traefik can be adopted without losing routes.
- [ ] A failed installation returns the machine to its recorded prior state.
- [ ] Application projects require no published HTTP port and no Traefik
  labels.

## Phase 6 — Diagnostics and observability

Goal: explain failures by layer instead of presenting a generic browser error.

- [ ] Add `docklane doctor`.
- [ ] Add `docklane doctor <route>`.
- [ ] Check DNS resolution.
- [ ] Check TCP listeners on ports 80 and 443.
- [ ] Check certificate chain, trust, expiry, and SAN match.
- [ ] Check Traefik router and provider state.
- [ ] Check workload selector resolution.
- [ ] Check shared-network membership and alias resolution.
- [ ] Check upstream port reachability from Traefik's network.
- [ ] Check final HTTPS response and redirect behavior.
- [ ] Provide one actionable repair suggestion per failed layer.
- [ ] Add machine-readable `--json` diagnostic output.
- [ ] Add a UI diagnostics view.
- [ ] Add health history with bounded retention.

Acceptance criteria:

- [ ] DNS, TLS, Traefik 404, network, and upstream failures are distinguishable.
- [ ] The Excalidraw route can be diagnosed end to end with one command.
- [ ] Diagnostics are read-only unless an explicit repair command is invoked.

## Phase 7 — UX/DX hardening and first release

Goal: make Docklane pleasant, predictable, and distributable.

### UX

- [ ] Add guided onboarding with dependency and safety explanations.
- [ ] Recommend a route name and likely HTTP port.
- [ ] Explain conflicts and ambiguous workload matches clearly.
- [ ] Show equivalent CLI commands for UI mutations.
- [ ] Add confirmation only for meaningful machine-level changes.
- [ ] Add accessible keyboard and screen-reader behavior.
- [ ] Add responsive and cross-browser UI checks.

### DX and release

- [ ] Freeze configuration schema version 1.
- [ ] Freeze API version 1.
- [ ] Add shell completions.
- [ ] Add man page and command reference.
- [ ] Add deterministic release builds.
- [ ] Add Linux package or installer artifact.
- [ ] Add checksums and release provenance.
- [ ] Add CI for Go, Svelte, integration tests, and packaging.
- [ ] Document supported Linux resolver/trust-store combinations.
- [ ] Write upgrade, backup, restore, and uninstall guides.
- [ ] Choose and add a project license.
- [ ] Tag the first alpha release.

Acceptance criteria:

- [ ] A new user can install, route an app, diagnose it, and uninstall from the
  documented workflow.
- [ ] Release artifacts reproduce from source.
- [ ] No known operation can silently overwrite unrelated host configuration.

## Integrated milestone

The first complete end-to-end milestone is:

```text
docklane install --dry-run
docklane install
docklane discover
docklane route add excalidraw \
  --project excalidraw \
  --service excalidraw \
  --port 80
docklane doctor excalidraw
```

Expected result:

```text
https://excalidraw.docker.home.arpa -> HTTP 200
```

The application has no published host HTTP port, Traefik does not require a
restart, the browser trusts the certificate, and all Docklane-owned changes
have a tested rollback.

## Deferred ideas

These are intentionally outside the first-release critical path:

- optional Docker socket proxy;
- route aliases and multiple hostnames per service;
- path-prefix routing;
- HTTP middleware templates;
- local OAuth or access policies;
- multiple local base domains;
- remote Docker contexts;
- raw TCP/UDP routing;
- Kubernetes support;
- plugin system.
