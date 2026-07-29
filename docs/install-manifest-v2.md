# Installation manifest schema v2

Schema v2 is Docklane's current machine-level ownership contract. It preserves
all schema-v1 resource, rollback, private-material, and execution-journal
semantics and adds an auditable `upgradeHistory` ledger.

The default manifest remains:

```text
/var/lib/docklane/install-manifest.json
```

Ordinary install, recovery, manifest, and uninstall commands accept only the
current schema. When they encounter schema v1 they make no change and direct
the operator to:

```sh
docklane upgrade --dry-run
docklane upgrade --token <reviewed-token>
```

## Reviewed migration

The dry run strictly decodes and validates the legacy manifest, then binds the
following facts into a SHA-256 review token:

- absolute manifest path;
- installation ID and generation;
- terminal installation state;
- source and target schema versions;
- SHA-256 fingerprint of the exact source file;
- deterministic backup path;
- migration operations and blockers.

Only `installed` and `rolled_back` manifests may migrate. An applying,
rolling-back, planned, or failed manifest must first complete its existing
recovery workflow with the binary that owns that journal. Docklane never
changes an active recovery topology during schema migration.

Apply reloads the source and rejects a changed generation, schema, identity,
creation time, content fingerprint, or token. It first creates an exact
mode-`0600` backup without overwriting an existing file:

```text
install-manifest.json.schema-v1-generation-<N>.bak
```

The upgraded manifest is then atomically replaced under the existing advisory
lock. A crash before replacement leaves schema v1 and can retry the same
reviewed token; a crash after replacement leaves a complete schema-v2
generation. No Docker, DNS, TLS, service, application, or controller-database
resource is mutated.

## Upgrade history

Each migration appends:

| Field | Meaning |
| --- | --- |
| `fromSchemaVersion` | Exact legacy schema |
| `toSchemaVersion` | Exact resulting schema |
| `appliedAt` | UTC atomic-migration timestamp |
| `sourceBackup.path` | Absolute immutable backup path |
| `sourceBackup.fingerprint` | SHA-256 of both the reviewed source and backup |

History must form a continuous, strictly increasing schema chain and end at
the manifest's current schema. Timestamps must lie within the installation
lifetime, and every backup contract is validated as an absolute canonical path
plus lowercase SHA-256 fingerprint.

## Unchanged ownership contract

Schema v2 retains the schema-v1 rules documented in
[install-manifest-v1.md](./install-manifest-v1.md):

- immutable installation identity and monotonic generation;
- strict managed-versus-adopted ownership;
- exact remove, restore, and preserve rollback strategies;
- fingerprinted file backups and external object observations;
- generated-material cache and per-resource execution journal;
- token-gated install recovery and uninstall;
- unknown-field, permission, symlink, size, and newer-schema refusal.
