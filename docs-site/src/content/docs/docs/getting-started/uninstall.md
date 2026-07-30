---
title: Uninstall
description: Review and roll back resources managed by Docklane.
sidebar:
  order: 4
---

Preview the uninstall:

```sh
docklane uninstall --dry-run
docklane uninstall --dry-run --json
```

Apply only the token returned by the plan you reviewed:

```sh
sudo docklane uninstall --token 'COPY_THE_REVIEWED_TOKEN'
```

Docklane removes resources it owns and restores adopted host configuration
from recorded backups. It does not remove unrelated application networks or
configuration that it did not create.

Keep the installation manifest and backups until the rollback has completed
and the host DNS, certificate trust, and Docker gateway have been verified.
