---
title: Installation manifest schema v1
description: Legacy manifest format retained for upgrade and recovery reference.
sidebar:
  order: 4
  badge:
    text: Legacy
    variant: caution
---

Schema v1 was used by the first public alpha. New installations use
[schema v2](./install-manifest-v2/).

Docklane can read a valid v1 manifest during a reviewed upgrade, create a
generation-numbered backup, and write the migrated v2 representation
atomically.

Keep the original v1 manifest and generated backup until the upgraded
installation and uninstall plan have both been verified.
