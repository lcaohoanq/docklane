# Docklane Implementation Plan

Last updated: 2026-07-28

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

| Phase | Scope                                          | Status                         |
| ----- | ---------------------------------------------- | ------------------------------ |
| 0     | Product and architecture decisions             | Complete                       |
| 1     | Non-disruptive controller prototype            | Complete                       |
| 2     | Route lifecycle and reconciliation reliability | In progress                    |
| 3     | Managed Docker network and Traefik integration | In progress                    |
| 4     | Local DNS and TLS lifecycle                    | Integrated checkpoint complete |
| 5     | Installer, migration, and rollback             | In progress                    |
| 6     | Diagnostics and observability                  | Complete                       |
| 7     | UX/DX hardening and first release              | Planned                        |

## Phase 0 — Product foundation

Goal: establish the problem, boundaries, and durable technical direction.

- [X] Choose the project name: Docklane.
- [X] Define the local-only scope.
- [X] Select Traefik as the global reverse proxy.
- [X] Select Go for the controller and CLI.
- [X] Select Svelte for the embedded web UI.
- [X] Select SQLite for controller-owned state.
- [X] Choose a shared controller API for CLI and web.
- [X] Choose Traefik's HTTP provider as the target central configuration path.
- [X] Choose stable Compose project/service selectors.
- [X] Choose `docker.home.arpa` as the target namespace.
- [X] Document Docker socket access as a privileged security boundary.
- [X] Establish the rule that the active gateway is not migrated during
  prototype development.

Acceptance criteria:

- [X] Architecture distinguishes control plane from request data plane.
- [X] Project goals, non-goals, invariants, and safety boundaries are written.

## Phase 1 — Non-disruptive controller prototype

Goal: validate the core route model without modifying Traefik, DNS,
certificates, or Docker networks.

### Backend

- [X] Scaffold the Go module and `docklane` command.
- [X] Add `serve`, `version`, and help commands.
- [X] Add validated runtime configuration.
- [X] Discover running containers through the Docker Unix socket.
- [X] Read Compose project/service labels and private TCP ports.
- [X] Define and validate the route domain model.
- [X] Persist routes in SQLite.
- [X] Resolve Compose selectors to current running containers.
- [X] Reject ambiguous workload resolution.
- [X] Generate complete Traefik HTTP-provider JSON.
- [X] Omit disabled and unresolved routes from generated configuration.
- [X] Add graceful controller shutdown.

### API and CLI

- [X] Add health, container discovery, route list/create, and provider
  endpoints.
- [X] Reject unknown route-create fields.
- [X] Default omitted route scheme to `http`.
- [X] Default omitted route enabled state to `true`.
- [X] Add `docklane discover`.
- [X] Add `docklane route list`.
- [X] Add `docklane route add`.
- [X] Add `--dry-run` and `--json`.
- [X] Support `DOCKLANE_URL`.

### Web UI

- [X] Scaffold Svelte and Vite.
- [X] Display discovered containers and internal ports.
- [X] Display saved routes.
- [X] Create a route from a selected container.
- [X] Show the equivalent CLI command.
- [X] Embed production UI assets in the Go binary.
- [X] Pin TypeScript 5.9 for current `svelte-check` compatibility.

### Quality and validation

- [X] Add domain validation tests.
- [X] Add Docker selector resolution tests.
- [X] Add SQLite persistence tests.
- [X] Add Traefik rendering tests.
- [X] Add API integration tests.
- [X] Add CLI client contract tests.
- [X] Add repeatable `make setup`, `make test`, and `make build` workflows.
- [X] Pass `go test ./...`.
- [X] Pass `go vet ./...`.
- [X] Pass `svelte-check` with zero warnings.
- [X] Build the embedded production UI and Go binary.
- [X] Verify live Docker discovery against the current machine.
- [X] Verify an isolated route produces the correct Traefik upstream.
- [X] Verify the existing `https://excalidraw.docker.lab` route remains HTTP
  200.
- [X] Stop the temporary validation controller.

Acceptance criteria:

- [X] A route can be created through CLI and observed through the UI/API.
- [X] Recreating a container does not change the stored Compose selector.
- [X] Traefik JSON uses the current resolved container instance.
- [X] No active system or proxy configuration is changed.

## Phase 2 — Route lifecycle and reconciliation reliability

Goal: complete controller-owned route management before introducing
machine-level mutations.

### Route lifecycle

- [X] Add `GET /api/v1/routes/{id}`.
- [X] Add route update/enable/disable.
- [X] Add route deletion.
- [X] Add matching CLI commands.
- [X] Add UI edit, enable/disable, and delete flows.
- [X] Add duplicate-name conflict errors with actionable messages.
- [X] Add optimistic concurrency or revision checking for updates.

### Observed state

- [ ] Define route states: `ready`, `unresolved`, `ambiguous`, `unreachable`,
  `disabled`, and `error`.
- [X] Return desired and observed state separately.
- [X] Validate that the configured internal port belongs to the selected
  workload.
- [X] Classify the active Traefik gateway as a managed system container.
- [X] Reject and omit routes that would send the gateway back into itself.
- [ ] Probe upstream reachability with bounded timeouts.
- [ ] Support non-Compose containers without pretending their ID is durable.
- [X] Define initial behavior for scaled Compose services: report ambiguous and
  omit the route until an explicit replica policy is implemented.
- [ ] Prefer deterministic managed aliases over generated container names.

### Reconciler

- [X] Add a periodic reconciliation loop.
- [X] Subscribe to Docker events for fast refresh.
- [X] Keep periodic reconciliation as recovery from missed events.
- [X] Debounce event bursts during Compose recreation.
- [X] Make each reconciliation operation idempotent.
- [X] Record the last successful reconcile time and last error.
- [ ] Add structured controller logs.
- [ ] Add Server-Sent Events for live UI updates.

### Persistence

- [X] Introduce numbered schema migrations.
- [X] Add database backup before migrations.
- [X] Add route revision storage.
- [ ] Add observed-status storage where appropriate.
- [ ] Define import/export schema version 1.
- [ ] Add `docklane export` and `docklane import --dry-run`.

Acceptance criteria:

- [X] Full route CRUD works through API, CLI, and UI.
- [X] Container recreation repairs a route without restarting Traefik.
- [X] Missed or absent Docker events are repaired by periodic reconciliation.
- [X] Ambiguous or missing workloads are visible and never published.
- [X] All work remains isolated from the active proxy and system DNS.

## Phase 3 — Managed network and Traefik integration

Goal: let global Traefik consume Docklane routes and reach selected containers
without published host ports.

### Managed Docker network

- [ ] Define the managed network name and labels.
- [ ] Preview network create/connect/disconnect operations.
- [ ] Create the network idempotently.
- [X] Attach selected containers without removing their existing networks.
- [X] Assign a deterministic per-route network alias.
- [X] Track attachments created by Docklane.
- [X] Detach only Docklane-owned attachments when no route needs them.
- [X] Handle stopped and recreated containers.
- [X] Add integration tests using disposable Docker resources.

### Traefik provider

- [X] Add private controller/provider connectivity.
- [X] Configure Traefik HTTP-provider polling.
- [X] Keep the provider endpoint unavailable from application and non-local
  networks using host-loopback publication plus `docklane-control`.
- [X] Add provider configuration validation before publish.
- [X] Test add, update, disable, delete, and recreate behavior.
- [X] Test controller/provider outage behavior.
- [X] Persist and serve a validated last-known-good provider snapshot.
- [X] Add route readiness showing whether Traefik activated the current route
  revision, service, and an `UP` backend.

### Migration safety

- [X] Snapshot the current Traefik, DNS, certificate, and Docklane database
  state.
- [X] Inventory the test workload's current Docker-label route and networks.
- [X] Detect hostname collisions between labels and Docklane routes.
- [X] Support shadow rendering without activating routes.
- [X] Integrate one test workload first.
- [X] Document and preserve rollback to the original provider configuration.

Acceptance criteria:

- [X] A selected container is reachable through Traefik without a published
  application host port.
- [X] Route changes are observed without recreating the app or restarting
  Traefik.
- [X] Removing a route does not stop or otherwise modify the application.
- [X] Provider failure has a tested and documented recovery behavior.

## Phase 4 — Local DNS and TLS lifecycle

Goal: make `https://<name>.docker.home.arpa` resolve locally with a trusted
certificate.

### DNS

- [X] Detect dnsmasq and the active system resolver manager.
- [X] Render the wildcard `docker.home.arpa` dnsmasq fragment.
- [X] Preview exact files and service operations before apply.
- [X] Validate configuration syntax before reload.
- [X] Configure split DNS for `docker.home.arpa`.
- [X] Explicitly handle systemd-resolved DNS-over-TLS on loopback.

- [-] Verify A/AAAA and HTTPS/SVCB lookup behavior. A resolution is verified;
  broader browser/resolver combinations remain for automated tests.

- [X] Add uninstall/rollback instructions for Docklane-owned DNS changes.

### TLS

- [X] Reuse the already trusted local root CA.
- [X] Protect CA and leaf private keys with restrictive permissions.
- [X] Generate a leaf certificate for `docker.home.arpa` and
  `*.docker.home.arpa`.
- [X] Preserve the existing explicitly installed system trust anchor.
- [X] Verify Chromium trust with a fresh browser profile.
- [X] Configure Traefik to serve the wildcard certificate.
- [ ] Track expiry and add safe certificate rotation.
- [X] Add certificate rollback from the timestamped backup.

Acceptance criteria:

- [X] An arbitrary one-label hostname resolves to `127.0.0.1`.
- [X] Browser and CLI hostname validation succeed without `-k`.
- [X] The served certificate contains the intended explicit SANs.
- [X] DNS and trust changes can be removed without affecting unrelated
  configuration.

## Phase 5 — Installer, migration, and rollback

Goal: construct the global reverse proxy first, then make application opt-in
safe and repeatable.

- [X] Define installation manifest schema v1 with strict ownership, rollback,
  validation, atomic persistence, and read-only inspection.
- [X] Add `docklane install --dry-run`. The deterministic foundation plan
  covers manifest creation, Traefik adoption, proxy-network ownership,
  dnsmasq, resolver behavior, and fingerprinted TLS certificate/key/trust
  adoption. It also covers the controller, restricted probe, private control
  network, shared socket volume, and controller data directory with no pending
  resource classes.
- [-] Add `docklane install`. Token-gated adoption is implemented with durable
  planned/applying/installed state transitions; managed create/configure
  executors and rollback remain.
- [X] Add `docklane uninstall --dry-run` with reverse dependency ordering,
  adopted-resource preservation, and managed remove/restore contracts.
- [X] Define managed installation specification v1 for canonical state/PKI
  paths, image references, TLS policy, and gateway/controller/probe topology.
- [X] Render deterministic managed dnsmasq, Traefik, and container artifacts
  with content fingerprints; declare private material as apply-time-only.
- [X] Generate and verify the managed root/leaf PKI bundle entirely in memory,
  including explicit apex and wildcard SAN coverage.
- [X] Generate a 256-bit dashboard password and bcrypt Traefik users file
  entirely in memory without exposing secrets in dry-run artifacts.
- [X] Add reversible atomic file staging with mode-preserving fingerprinted
  backups, symlink refusal, failure rollback, backup-integrity checks, and
  target-drift refusal.
- [X] Derive strict managed Docker network, volume, and container create
  requests with installation-ID ownership labels and inspected-state checks.
- [X] Add dependency-ordered Docker apply and reverse rollback with conflict,
  failure-injection, configuration-drift, and retry coverage.
- [X] Pin the Arch p11-kit/systemd-resolved host profile and render an exact
  route-only resolver drop-in as a reviewed managed artifact.
- [X] Add transactional dnsmasq validation, trust refresh/verification,
  service-state restoration, resolver cache flush, DNS verification, drift
  refusal, and failure rollback.
- [X] Add a validated per-resource execution journal and recovery coordinator
  with write-ahead apply/rollback checkpoints, exact workflow matching,
  generation-conflict refusal, and both crash windows covered by tests.
- [X] Journal every materialized managed file and add content/mode-bound file
  adapters with deterministic backups, restart inspection, remove/restore
  preconditions, sensitive-buffer clearing, and drift-safe retry/rollback.
- [ ] Persist generated PKI and credential material in a private recoverable
  cache so a restarted process can rehydrate the journaled file intent.
- [ ] Add `docklane uninstall`.
- [-] Record every file, trust entry, Docker network, container, and service
  created by Docklane. File and trust-anchor ownership are complete; Docker,
  directory, resolver behavior, and service observations remain.
- [X] Add read-only preflight checks for ports 80/443, Docker access and proxy
  network compatibility, dnsmasq, resolver conflicts, manifest state, and
  existing Traefik.
- [X] Inventory Traefik certificate wiring, SANs, expiry, private-key
  permissions, issuing trust anchor, and the exact certificate served on 443.
- [X] Inventory the controller/probe runtime, image identity, health, network
  isolation, port exposure, security settings, mounts, and related storage.
- [X] Support adopting a compatible existing global Traefik deployment.
- [X] Refuse unsafe adoption with a precise explanation.
- [ ] Add `docklane app enable` for opt-in network attachment and routing.
- [ ] Add `docklane app disable` with safe detach behavior.
- [ ] Export copy-paste Compose guidance without editing user files.
- [-] Add interrupted-install recovery. The generic journal and recovery state
  machine are complete; concrete managed resource adapters and command resume
  wiring remain.
- [ ] Add upgrade and schema migration flow.
- [ ] Exercise install/rollback in a disposable VM before host rollout.

Acceptance criteria:

- [ ] A clean machine can install the full local gateway from one reviewed
  plan.
- [X] An existing compatible Traefik can be adopted without losing routes.
- [ ] A failed installation returns the machine to its recorded prior state.
- [ ] Application projects require no published HTTP port and no Traefik
  labels.

## Phase 6 — Diagnostics and observability

Goal: explain failures by layer instead of presenting a generic browser error.

- [X] Add `docklane doctor`.
- [X] Add `docklane doctor <route>`.
- [X] Check DNS resolution.
- [X] Check TCP listeners on ports 80 and 443.
- [X] Check certificate chain, trust, expiry, and SAN match.
- [X] Check Traefik router and provider state.
- [X] Check workload selector resolution.
- [X] Check shared-network membership and alias resolution.
- [X] Check upstream port reachability from Traefik's network.
- [X] Check final HTTPS response and redirect behavior.
- [X] Provide one actionable repair suggestion per failed layer.
- [X] Add machine-readable `--json` diagnostic output.
- [X] Add a UI diagnostics view.
- [X] Add health history with bounded retention.

Acceptance criteria:

- [X] DNS, TLS, Traefik 404, network, and upstream failures are distinguishable.
- [X] The Excalidraw route can be diagnosed end to end with one command.
- [X] Diagnostics are read-only unless an explicit repair command is invoked.

## Phase 7 — UX/DX hardening and first release

Goal: make Docklane pleasant, predictable, and distributable.

### UX

- [X] Keep new and updated route links disabled while Docklane reconciles and
  Traefik publishes/verifies them.
- [X] Poll route-scoped readiness and replace transient raw Traefik 404s with
  publishing progress and a bounded diagnostic timeout.
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
