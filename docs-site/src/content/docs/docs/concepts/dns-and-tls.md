---
title: Local DNS and TLS
description: Why Docklane uses docker.home.arpa and a machine-local certificate authority.
sidebar:
  order: 2
---

Docklane uses one namespace for both DNS and TLS:

```text
*.docker.home.arpa
```

`home.arpa` is reserved for non-unique residential and local naming. Docklane
configures local wildcard resolution and creates a certificate covering
`docker.home.arpa` and its first-level wildcard.

The generated root CA is local to the managed host. Trust is installed into the
supported host trust store so browsers and command-line clients can validate
the gateway certificate.

Docklane does not obtain public certificates, publish public DNS records, or
expose routes to the internet.
