---
title: Installation manifest schema v2
description: Current ownership and rollback record used by Docklane.
sidebar:
  order: 3
---

The v2 manifest at `/var/lib/docklane/install-manifest.json` is the source of
truth for resources Docklane owns or has adopted.

It records:

- Schema version and generation.
- Installation state and selected runtime image.
- Managed files, services, containers, networks, volumes, certificates, and
  trust-store entries.
- Whether each resource was created or adopted.
- Backup locations required for rollback.

Updates are written atomically. Before migrating a v1 manifest, Docklane creates
a generation-numbered backup:

```text
install-manifest.json.schema-v1-generation-N.bak
```

Validate or inspect a manifest without applying machine changes:

```sh
docklane manifest validate --path /var/lib/docklane/install-manifest.json
docklane manifest show --path /var/lib/docklane/install-manifest.json
```

Do not edit an active manifest by hand. Use a matching Docklane binary to plan
upgrade or recovery operations.
