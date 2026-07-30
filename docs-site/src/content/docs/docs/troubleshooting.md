---
title: Troubleshooting
description: Resolve common installation and route failures by layer.
---

Start with:

```sh
docklane doctor
docklane preflight
```

## Route remains unresolved

Confirm the Compose project and service are running and that exactly one
container matches the saved selector. Recreate the service, then rerun
`docklane discover`.

## Route remains publishing or verifying

Check Traefik and controller health, then inspect the targeted report:

```sh
docklane doctor --json ROUTE_NAME
docker compose ps
```

Confirm the workload declares the selected internal port and shares the proxy
network.

## Name does not resolve

Verify `systemd-resolved` and `dnsmasq` are active. Do not replace Docklane's
managed resolver files by hand; use preflight and the installation manifest to
identify the owned configuration and backup.

## Browser rejects the certificate

Compare controller TLS checks with the browser probe. A passing controller
check does not prove the browser uses the same trust store. Restart the browser
after trust-store changes and verify the certificate covers
`*.docker.home.arpa`.

## Ports 80 or 443 are busy

Identify the listener before installation:

```sh
sudo ss -lntp '( sport = :80 or sport = :443 )'
```

Docklane adopts only a compatible Traefik gateway. Stop or reconfigure other
listeners explicitly rather than forcing the install plan.
