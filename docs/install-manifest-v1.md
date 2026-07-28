# Installation manifest schema v1

Docklane's installation manifest is the durable ownership boundary for
machine-level changes. It is a standalone JSON file so install, recovery, and
uninstall remain possible before the controller database exists or after the
controller has stopped.

The default path is:

```text
/var/lib/docklane/install-manifest.json
```

The file is mode `0600`, is replaced atomically, and is protected by an
advisory lock. Readers reject symbolic links, non-regular files, unknown JSON
fields, files larger than 4 MiB, unsupported schema versions, and invalid
ownership/rollback combinations.

## Top-level fields

| Field | Meaning |
| --- | --- |
| `schemaVersion` | Exact schema version; v1 readers refuse other versions |
| `installationId` | Immutable UUID v4 for one installation |
| `generation` | Monotonic revision used to reject concurrent stale writes |
| `productVersion` | Docklane version that last wrote the manifest |
| `state` | `planned`, `applying`, `installed`, `rolling_back`, `rolled_back`, or `failed` |
| `createdAt`, `updatedAt` | UTC lifecycle timestamps |
| `settings` | Installation-wide base domain and proxy network |
| `managedSpecification` | Optional validated clean-install contract; omitted for pure adoption |
| `managedArtifacts` | Optional reviewed config/container artifacts and apply-time private-material descriptors |
| `resources` | Exact owned or adopted machine resources |

## Resource contract

Supported v1 resource kinds are:

- `file`
- `directory`
- `trust_anchor`
- `docker_network`
- `docker_volume`
- `docker_container`
- `system_service`
- `resolver_rule`

Every resource has a stable logical ID, exact target, ownership, state, and
rollback strategy. Optional SHA-256 fingerprints bind records to exact
contents. A restore record captures an absolute backup path and its SHA-256
fingerprint before mutation.

The safety matrix is strict:

| Ownership | Allowed rollback | Meaning |
| --- | --- | --- |
| `managed` | `remove` | Docklane created it and may delete it |
| `managed` | `restore` | Docklane replaced prior state and must restore its recorded backup |
| `adopted` | `preserve` | It existed before Docklane and must never be removed by uninstall |

An applied or verified `restore` resource is invalid without a recorded
backup. This makes “restore the old state” a verifiable fact rather than an
unrecorded promise.

## Token-gated adoption

`docklane install --token TOKEN` rebuilds the plan immediately before
apply and refuses a stale or mismatched token. The adoption-only executor
creates generation 1 as `planned`, generation 2 as `applying`, and generation
3 as `installed`. All adopted resources must already be `verified` with
`preserve` rollback. If finalization fails, the next durable generation is
`failed`; no running resource is modified or removed.

Plans containing a managed resource are rejected before manifest creation
until the corresponding create/configure and remove/restore executors exist.
Future managed apply copies the reviewed `managedSpecification` into the
manifest together with `managedArtifacts`, so recovery does not depend on
ambient Compose files or legacy host paths. Rendered artifacts contain their
exact content and SHA-256 fingerprint. Generated PKI and secret artifacts
contain no content or fingerprint in the plan; they declare only the target,
mode, sensitivity, and that material is generated during apply.

The apply-time materializer now exists but is not enabled by the install
command. It generates all PKI and dashboard credentials in memory and passes
the resulting file bundle to a reversible atomic stager. Replaced files receive
mode-preserving backups whose SHA-256 fingerprints fit the resource backup
contract below; newly created files require no backup. Managed apply remains
blocked until these results are journaled into manifest generations together
with the independently tested host and Docker transactions.

The Docker executor likewise remains disconnected from the command while its
transaction is tested independently. It injects the manifest installation ID
as an ownership label on both networks, the volume, and all three containers;
records Docker's exact returned IDs and inspected state; and rolls back in
reverse dependency order. This identity is necessary because Docker named
volume creation is idempotent and does not provide an atomic
create-if-absent operation.

The Arch host transaction pins p11-kit and systemd-resolved capabilities,
snapshots dnsmasq/resolver service state, validates and activates the staged
configuration, refreshes and verifies the trust anchor, and probes both apex
and wildcard DNS. Its rollback restores the file transaction before refreshing
trust and returning services to their prior state. Service drift blocks file
rollback, while a failed file restore blocks service reload. This transaction
also remains disconnected until each successful step and rollback contract is
journaled durably.

## Managed execution journal

A managed manifest may contain an `execution` object with its own schema
version, phase, and immutable ordered operation list. Every managed resource is
covered exactly once; adopted resources never enter this mutation journal.
Each operation binds its resource ID and target to a stage and records the
attempt count plus a non-secret observation such as an external object ID,
content fingerprint, backup contract, or inspected-state fingerprint.
File operations additionally bind their intended content fingerprint and mode
into the immutable operation list. This exposes no file content but prevents a
restart from combining newly generated private material with files from an
earlier generation.

Before an external apply, the next manifest generation records `applying`.
After successful inspection it records `applied`. Rollback similarly records
`rolling_back` before mutation and `rolled_back` after inspection. Recovery
handles the two ambiguous crash windows by inspecting first:

- already applied advances without creating again;
- absent after interrupted apply may be retried behind a new checkpoint;
- already rolled back advances without deleting or restoring again;
- ownership or configuration drift records `failed` and performs no further
  mutation.

All compare-and-swap generation conflicts stop before the associated mutation.
An apply error is also inspected because the external API may have completed
the action before its response was lost. Any observed success is journaled and
then included in reverse rollback. This coordinator is currently an
independently tested composition boundary; managed command wiring still waits
for concrete operation adapters.

The file adapter is the first concrete operation adapter. Every materialized
file now has an explicit managed resource, including separate resolver
configuration, PKI keys/certificates, trust anchor, Traefik configuration, and
dashboard credentials. Deterministic backup paths let an `applying` checkpoint
reconstruct whether the file was written or whether only backup preparation
completed. Content-and-mode snapshots detect target and backup drift.

Generated private bytes are intentionally absent from this JSON document. The
adapter therefore refuses recovery when a caller supplies bytes whose intent
fingerprint differs from the journal. A future private material cache must
durably rehydrate the exact generated bundle before managed install can be
enabled across process restarts.

## Reverse planning

`docklane uninstall --dry-run` accepts only an `installed` manifest and walks
its resources in reverse order:

- `adopted` + `preserve` becomes a non-mutating `preserve` operation;
- `managed` + `remove` becomes a mutating `remove` operation;
- `managed` + `restore` becomes a mutating `restore` operation and requires
  the recorded backup path and SHA-256 fingerprint.

The resulting token changes with the installation ID, manifest generation,
manifest path, rollback operations, or blockers. Uninstall apply remains
disabled until the mutation executors verify those contracts.

## Example

```json
{
  "schemaVersion": 1,
  "installationId": "018f5e52-4f22-4a6e-8ad8-d3e4450d1957",
  "generation": 1,
  "productVersion": "dev",
  "state": "planned",
  "createdAt": "2026-07-28T04:00:00Z",
  "updatedAt": "2026-07-28T04:00:00Z",
  "settings": {
    "baseDomain": "docker.home.arpa",
    "proxyNetwork": "proxy"
  },
  "resources": [
    {
      "id": "dnsmasq-config",
      "kind": "file",
      "target": "/etc/dnsmasq.d/docklane.conf",
      "ownership": "managed",
      "state": "planned",
      "rollback": "restore"
    },
    {
      "id": "proxy-network",
      "kind": "docker_network",
      "target": "proxy",
      "ownership": "adopted",
      "state": "verified",
      "rollback": "preserve"
    }
  ]
}
```

Create an empty planned manifest without modifying host resources:

```sh
docklane manifest init
docklane manifest validate
docklane manifest show
docklane manifest show --json
```

Use `--path` or `DOCKLANE_MANIFEST` for a non-default location. Initialization
refuses to overwrite an existing manifest.
