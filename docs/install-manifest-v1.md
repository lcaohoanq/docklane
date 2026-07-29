# Installation manifest schema v1

Schema v1 is retained as the legacy contract. Current binaries inspect it only
through `docklane upgrade --dry-run`; see
[installation manifest schema v2](./install-manifest-v2.md) for the reviewed
migration and current format.

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
| `reviewedToken` | Original managed/adoption plan token required for recovery |
| `rollbackToken` | Reviewed uninstall token required to resume reverse execution |
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
contents or a reviewed prior service state. A file restore record captures an
absolute backup path and its SHA-256 fingerprint before mutation.

The safety matrix is strict:

| Ownership | Allowed rollback | Meaning |
| --- | --- | --- |
| `managed` | `remove` | Docklane created it and may delete it |
| `managed` | `restore` | Docklane changed prior state and must restore its recorded snapshot or file backup |
| `adopted` | `preserve` | It existed before Docklane and must never be removed by uninstall |

An applied or verified file/trust-anchor `restore` resource is invalid without
a recorded backup. Service and resolver restore operations instead retain the
reviewed active/inactive snapshot in their resource and execution observation.
This makes “restore the old state” a verifiable fact rather than an unrecorded
promise.

## Token-gated apply and recovery

`docklane install --token TOKEN` rebuilds the plan immediately before
apply and refuses a stale or mismatched token. The adoption-only executor
creates generation 1 as `planned`, generation 2 as `applying`, and generation
3 as `installed`. All adopted resources must already be `verified` with
`preserve` rollback. If finalization fails, the next durable generation is
`failed`; no running resource is modified or removed.

Managed apply copies the reviewed token, `managedSpecification`, and
`managedArtifacts` into the manifest before mutation, so recovery does not
depend on ambient Compose files or legacy host paths. Rendered artifacts
contain their exact content and SHA-256 fingerprint. Generated PKI and secret
artifacts contain no content or fingerprint in the plan; they declare only the
target, mode, sensitivity, and that material is generated during apply.

The apply-time materializer generates PKI and dashboard credentials in memory
and publishes them to the private recovery cache before the execution journal
is created. Replaced files receive mode-preserving backups whose SHA-256
fingerprints fit the resource backup contract; newly created files require no
backup.

The Docker workflow injects the manifest installation ID as an ownership label
on both networks, the volume, and all three containers. Each resource has its
own journal operation with Docker's exact returned ID and a normalized
inspected-state fingerprint, and rolls back in reverse dependency order. This
identity is necessary because Docker named volume creation is idempotent and
does not provide an atomic create-if-absent operation.

The host workflow records its platform, trust-store, and resolver profiles.
`arch-systemd` pins p11-kit, while `debian-systemd` pins
`update-ca-certificates`, Debian's consolidated CA bundle, and the Debian
dnsmasq service-helper validation contract. Both use systemd-resolved.
Preflight records whether dnsmasq and systemd-resolved were active, and that
reviewed state is fingerprinted into their restore contracts. Apply validates
and activates staged configuration, refreshes and verifies the trust anchor,
and probes both apex and wildcard DNS. Rollback restores journaled files before
refreshing trust and returning services to their prior states. A failed file
restore blocks service reload.

## Managed execution journal

A managed manifest may contain an `execution` object with its own schema
version, phase, and immutable ordered operation list. Every managed resource is
covered exactly once; adopted resources never enter this mutation journal.
Each operation binds its resource ID and target to a stage and records the
attempt count plus a non-secret observation such as an external object ID,
content fingerprint, backup contract, or inspected-state fingerprint.
File operations additionally bind their intended content fingerprint and mode
into the immutable operation list. Directory operations bind their ownership
marker fingerprint and mode. This exposes no file content but prevents a
restart from combining newly generated private material or directory identity
with operations from an earlier generation.

Symlink resources record the link path, desired absolute target, and exact
reviewed prior target. The resolver-stub operation uses an atomic exchange and
fingerprints both targets in its execution observation. Recovery accepts only
the reviewed prior or desired state; any third target is drift and blocks
mutation.

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
then included in reverse rollback. The install command uses this coordinator
for every managed resource. An incomplete manifest can resume only with its
persisted reviewed token; terminal installed state is idempotent.

File, directory, host, and Docker adapters provide the concrete operations.
Every materialized file and every required parent below the managed state root
has an explicit resource. Directories carry private ownership markers and use
atomic no-replace publication. Deterministic backup paths let an `applying`
checkpoint reconstruct whether a file was written or whether only backup
preparation completed. Content-and-mode snapshots detect target and backup
drift.

The workflow composition contract orders managed operations as directories,
files, host activation, Docker runtime, and verification. It requires complete
resource coverage and an explicit direct parent directory for managed files.
Rollback reverses this order.

Generated private bytes are intentionally absent from this JSON document. The
adapter therefore refuses recovery when a caller supplies bytes whose intent
fingerprint differs from the journal.

## Private material cache

Managed manifests may include a `materialCache` object before their execution
journal is created. It records schema and lifecycle state, the exact
installation-bound cache directory, an inventory fingerprint, and ordered
entries containing:

- artifact ID and final target;
- intended target mode and sensitivity;
- private cache path;
- SHA-256 content fingerprint.

The entry list never contains cached content. On disk, all payloads and the
strict descriptor are private `0600` files beneath effective-user-owned `0700`
directories. Cache load requires the selected managed artifacts, descriptor,
entry order, paths, modes, ownership, and fingerprints to agree. This allows a
restart to recover the original generated PKI and dashboard credentials
without consuming new randomness.

Cleanup uses durable `ready`, `clearing`, and `cleared` states. `clearing` and
`cleared` are valid only after execution is complete, rolled back, or failed.
The write-ahead clearing generation prevents a deleted cache from appearing
ready if the final manifest save is interrupted. Unknown or drifted cache
entries are preserved and reported rather than recursively deleted.

## Reverse planning

`docklane uninstall --dry-run` accepts only an `installed` manifest and walks
its resources in reverse order:

- `adopted` + `preserve` becomes a non-mutating `preserve` operation;
- `managed` + `remove` becomes a mutating `remove` operation;
- `managed` + `restore` becomes a mutating `restore` operation; file restores
  require the recorded backup path and SHA-256 fingerprint, while service
  restores use the recorded prior-state snapshot.

The resulting token changes with the installation ID, manifest generation,
manifest path, rollback operations, or blockers. Apply persists this token as
`rollbackToken` before mutation, then walks the immutable installed execution
journal in reverse. The same token resumes a `rolling_back` manifest.

Rollback-only file steps use the installed intent fingerprint and mode plus
the operation observation; generated private bytes are not recreated. Docker
steps require the recorded Engine identity and inspected-state fingerprint.
Host steps reconstruct their reviewed service snapshot from execution
observations even after earlier resource checkpoints have been cleared.
Non-empty controller data is retained after its ownership marker is removed.
The final private manifest remains in `rolled_back` state as an audit
tombstone.

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
