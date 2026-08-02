# Docklane Implementation Plan

Last updated: 2026-07-29

This file is the project task tracker. Architecture and design rationale live
in the
[architecture documentation](../docs-site/src/content/docs/docs/architecture/system-overview.md).

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
| 3     | Managed Docker network and Traefik integration | Complete                       |
| 4     | Local DNS and TLS lifecycle                    | Integrated checkpoint complete |
| 5     | Installer, migration, and rollback             | In progress                    |
| 6     | Diagnostics and observability                  | Complete                       |
| 7     | UX/DX hardening and first release              | First alpha published          |

## Next execution order

The disposable-VM lifecycle gate, application opt-in workflow, installation
schema migration, route readiness gate, and first public alpha are complete.
The post-alpha priority is to tighten the shortest path from a running
container to a usable local URL based on real user feedback:

1. **Create a route and use it**
   - recommend a route name and likely internal HTTP port;
   - explain workload, hostname, and port conflicts at the point of action;
   - keep the URL disabled until DNS, Traefik, and the backend are reachable;
   - show the equivalent CLI command for every UI route mutation.
2. **Post-alpha distribution**
   - finish guided onboarding and accessibility work;
   - freeze public configuration and API schemas;
   - expand provenance, packaging, and operator guides.

Automatic certificate lifecycle maintenance is intentionally deferred until
after the first alpha. Existing diagnostics still report certificate expiry,
and the installer preserves certificate backups.

The Debian and Arch reference harnesses remain available for regression and
release-candidate qualification.

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
- [X] Keep complete container inventory while suppressing route actions for
  system, opted-out, and portless workloads.
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

- [X] Define reconciliation states: `ready`, `unresolved`, `ambiguous`,
  `disabled`, and `error`. Upstream unreachability is reported as bounded
  readiness/diagnostic evidence instead of a stale durable route state.
- [X] Return desired and observed state separately.
- [X] Validate that the configured internal port belongs to the selected
  workload.
- [X] Classify the active Traefik gateway as a managed system container.
- [X] Reject and omit routes that would send the gateway back into itself.
- [X] Probe upstream reachability from the restricted proxy-network sidecar
  with bounded timeouts.
- [ ] Support non-Compose containers without pretending their ID is durable.
- [X] Define initial behavior for scaled Compose services: report ambiguous and
  omit the route until an explicit replica policy is implemented.
- [X] Prefer deterministic managed aliases over generated container names.

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

- [X] Define the managed network name and ownership labels.
- [X] Preview network create/connect/disconnect operations.
- [X] Create the network idempotently.
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
- [ ] Post-alpha: add reviewed certificate rotation. Expiry is already reported
  by `docklane doctor`; automated lifecycle work is not on the first-alpha
  critical path.
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
- [X] Add `docklane install` with token-gated adoption and managed execution,
  durable material/execution checkpoints, automatic same-token resume, and
  terminal private-material cleanup.
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
- [X] Add automatic Arch/Debian host-profile selection. The Debian profile
  uses `update-ca-certificates`, the Debian trust bundle and anchor directory,
  the package service-helper validator, and
  `/etc/dnsmasq.d/docklane.conf`. The Arch profile manages its package-default
  `/etc/dnsmasq.conf`; both profiles bind dnsmasq explicitly to loopback for
  compatibility with systemd-resolved.
- [X] Inventory the `/etc/resolv.conf` target and journal an atomic,
  drift-checked switch to systemd-resolved's local stub, restoring the exact
  prior symlink on rollback and uninstall.
- [X] Add transactional dnsmasq validation, trust refresh/verification,
  service-state restoration, resolver cache flush, DNS verification, drift
  refusal, and failure rollback.
- [X] Add a validated per-resource execution journal and recovery coordinator
  with write-ahead apply/rollback checkpoints, exact workflow matching,
  generation-conflict refusal, and both crash windows covered by tests.
- [X] Journal every materialized managed file and add content/mode-bound file
  adapters with deterministic backups, restart inspection, remove/restore
  preconditions, sensitive-buffer clearing, and drift-safe retry/rollback.
- [X] Persist generated PKI and credential material in an atomic,
  installation-bound private cache with strict descriptor/inventory
  validation, effective-user ownership checks, restart rehydration, and
  write-ahead terminal cleanup.
- [X] Record managed state directories explicitly and add journal-backed,
  atomic no-replace creation with ownership markers, restart reconstruction,
  drift-safe removal, and nested reverse cleanup.
- [X] Enforce complete managed workflow composition in dependency order:
  directories, files, host activation, Docker runtime, then verification.
  Integration coverage proves cached-file rollback precedes directory cleanup.
- [X] Add per-resource journal adapters for host service/resolver activation
  and all managed Docker networks, volume, and containers with exact prior
  state/Engine identity, drift refusal, and reverse rollback.
- [X] Support Docker 20.10 by creating containers on one network and attaching
  additional endpoints transactionally; attach the controller to the default
  bridge so its `127.0.0.1:4646` binding works while control traffic remains on
  the private network.
- [X] Add token-gated `docklane uninstall` with reverse journal execution,
  adopted-resource preservation, same-token interruption recovery, generated
  file rollback without secret regeneration, persistent-data retention, and a
  private audit tombstone.
- [X] Record every file, directory, trust entry, Docker network, volume,
  container, resolver behavior, and service state created or changed by
  Docklane.
- [X] Add read-only preflight checks for ports 80/443, Docker access and proxy
  network compatibility, dnsmasq, resolver conflicts, manifest state, and
  existing Traefik.
- [X] Inventory Traefik certificate wiring, SANs, expiry, private-key
  permissions, issuing trust anchor, and the exact certificate served on 443.
- [X] Inventory the controller/probe runtime, image identity, health, network
  isolation, port exposure, security settings, mounts, and related storage.
- [X] Support adopting a compatible existing global Traefik deployment.
- [X] Refuse unsafe adoption with a precise explanation.
- [X] Add `docklane app enable` with deterministic workload selection,
  duplicate-domain refusal, idempotent re-enable, and bounded readiness wait.
- [X] Add `docklane app disable` with idempotent route disable and
  ownership-safe attachment cleanup.
- [X] Export copy-paste Compose guidance without editing user files.
- [X] Add interrupted-install recovery across private material, filesystem,
  host, and Docker stages with immutable topology and same-token command resume.
- [X] Add token-reviewed installation upgrade and schema migration with
  terminal-state gating, exact source fingerprinting, immutable private
  backup, generation-safe atomic replacement, and schema-v2 audit history.
- [X] Exercise clean install, interruption recovery, rollback, and uninstall in
  disposable Debian and Arch VMs before managed host rollout. Clean install,
  a no-published-port HTTPS route, reviewed uninstall, and same-token recovery
  are proven on both distributions.

Disposable VM evidence:

- Debian 12, VM 129, snapshot `docklane-ready`: managed install, trusted HTTPS
  route without a published application port, and token-gated uninstall
  completed. Resolver and dnsmasq state were restored and application data was
  retained with Docklane ownership released. A `SIGKILL` while the resolver
  symlink operation was journaled as applying resumed with the same token and
  completed on attempt two.
- Arch Linux cloud image, VM 130, snapshot `docklane-arch-ready`: provisioned
  by cloud-init with 2 vCPU, 2 GiB RAM, and a 12 GiB disk. The same lifecycle
  completed against Docker 20-compatible runtime behavior. Uninstall removed
  managed containers and networks, left systemd-resolved active, restored the
  package-default `/etc/dnsmasq.conf`, retained SQLite data, and released its
  ownership marker. Journal-directed `SIGKILL` interruptions in the file,
  host-service, and Docker stages each resumed with the reviewed token and
  completed on attempt two. A missing-image Docker error automatically rolled
  all preceding host and Docker mutations back to the recorded baseline.

Acceptance criteria:

- [X] A clean machine can install the full local gateway from one reviewed
  plan.
- [X] An existing compatible Traefik can be adopted without losing routes.
- [X] A failed installation returns the machine to its recorded prior state.
- [X] Application projects require no published HTTP port and no Traefik
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
- [X] Recommend an available route name and conservatively rank conventional
  HTTP/HTTPS container ports without guessing among unusual listeners.
- [X] Explain hostname, declared-port, and ambiguous workload conflicts at the
  point of action.
- [ ] Show equivalent CLI commands for UI mutations.
- [ ] Add confirmation only for meaningful machine-level changes.
- [ ] Add accessible keyboard and screen-reader behavior.
- [ ] Add responsive and cross-browser UI checks.

### DX and release

- [ ] Freeze configuration schema version 1.
- [ ] Freeze API version 1.
- [ ] Add shell completions.
- [ ] Add man page and command reference.
- [X] Add deterministic, byte-compared Linux `amd64` and `arm64` release
  builds with normalized metadata and linker-injected versions.
- [X] Add a portable Linux installer artifact: versioned tarballs contain the
  binary, license, README, and minimal controller/probe image Dockerfile.
- [X] Publish version-matched `amd64`/`arm64` controller and probe images to
  Docker Hub from the exact verified release binaries.
- [X] Automate Docker Hub publication for release tags; the repository
  operator supplies the write-only `DOCKERHUB_TOKEN` GitHub Actions secret.
- [-] Add checksums and release provenance. SHA-256 manifests are complete;
  signed provenance remains a post-alpha hardening option.
- [X] Add CI for Go, Svelte, integration tests, binary builds, embedded assets,
  and reproducible release packaging.
- [X] Document the supported Debian/Arch systemd-resolved, dnsmasq, and
  platform trust-store combinations in the first-alpha guide.
- [-] Write upgrade, backup, restore, and uninstall guides. First-alpha
  backup, schema-upgrade boundaries, interruption recovery, and reviewed
  uninstall are documented; a general product upgrade path is not supported
  yet.
- [X] License the project under Apache License 2.0.
- [X] Tag and publish
  [`v0.1.0-alpha.1`](https://github.com/lcaohoanq/docklane/releases/tag/v0.1.0-alpha.1)
  with verified `amd64`/`arm64` archives and SHA-256 checksums.

Acceptance criteria:

- [X] A new user can install, route an app, diagnose it, and uninstall from the
  documented workflow. Rehearsed from `docklane-clean` on Debian 12 VM 129.
- [X] Release artifacts reproduce from source.
- [X] No known operation can silently overwrite unrelated host configuration;
  the clean-VM rehearsal verified token-gated apply and exact-state rollback.

## Integrated milestone

The first complete end-to-end milestone is:

```text
docklane install --dry-run
docklane install --token <reviewed-install-token>
docklane discover
docklane route add excalidraw \
  --project excalidraw \
  --service excalidraw \
  --port 80
docklane doctor excalidraw
docklane uninstall --dry-run
docklane uninstall --token <reviewed-uninstall-token>
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
