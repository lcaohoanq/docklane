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
manifest so recovery does not depend on ambient Compose files or legacy host
paths.

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
