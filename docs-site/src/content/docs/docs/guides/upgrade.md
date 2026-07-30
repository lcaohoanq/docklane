---
title: Upgrade an installation
description: Review schema and managed-resource changes before upgrading Docklane.
sidebar:
  order: 3
---

Preview an upgrade before applying it:

```sh
docklane upgrade --dry-run
docklane upgrade --dry-run --json
```

Apply the exact reviewed plan:

```sh
sudo docklane upgrade --token 'COPY_THE_REVIEWED_TOKEN'
```

The current alpha upgrade workflow migrates installation-manifest schemas and
managed configuration. It is not yet a general-purpose binary updater.

Before upgrading, keep a copy of the current binary, release checksum, manifest,
and manifest backups. After upgrading, run `docklane doctor` and verify at least
one HTTPS route from the browser.
