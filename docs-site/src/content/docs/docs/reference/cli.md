---
title: CLI reference
description: Common Docklane commands and their operational purpose.
sidebar:
  order: 1
---

| Command | Purpose |
| --- | --- |
| `docklane version` | Print the embedded release version. |
| `docklane discover` | List discovered Docker workloads. |
| `docklane doctor [route]` | Diagnose all routes or one named route. |
| `docklane network plan` | Preview proxy-network changes. |
| `docklane network apply` | Apply the network plan. |
| `docklane app guide PROJECT/SERVICE` | Show application-specific routing guidance. |
| `docklane app enable PROJECT/SERVICE` | Enable routing for a Compose workload. |
| `docklane app disable NAME` | Disable an application route. |
| `docklane preflight` | Check the host without changing it. |
| `docklane install --dry-run` | Produce a reviewed installation plan and token. |
| `docklane install --token TOKEN` | Apply the exact installation plan. |
| `docklane uninstall --dry-run` | Produce an ownership-aware rollback plan. |
| `docklane uninstall --token TOKEN` | Apply the exact rollback plan. |
| `docklane upgrade --dry-run` | Preview supported installation migrations. |
| `docklane upgrade --token TOKEN` | Apply the exact upgrade plan. |
| `docklane manifest init` | Initialize a manifest at an explicit path. |
| `docklane manifest validate` | Validate a manifest without changing it. |
| `docklane manifest show` | Print normalized manifest content. |

Commands that support JSON use `--json`. Plan-based machine changes require a
fresh token so a changed host cannot silently reuse stale approval.

Run `docklane COMMAND --help` from the matching release binary for the complete
set of flags.
