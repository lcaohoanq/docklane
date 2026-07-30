---
title: Installation
description: Prepare a supported Linux host and install Docklane safely.
sidebar:
  order: 3
---

## Prepare Debian 12

```sh
sudo apt-get update
sudo apt-get install -y ca-certificates curl dnsmasq docker.io systemd-resolved
sudo systemctl enable --now docker systemd-resolved
```

## Prepare Arch Linux

```sh
sudo pacman -Syu --needed ca-certificates curl dnsmasq docker
sudo systemctl enable --now docker systemd-resolved
```

Confirm the required services and socket:

```sh
systemctl is-active systemd-resolved
sudo docker version
sudo test -S /var/run/docker.sock
```

Do not manually configure `dnsmasq` for Docklane. The installer validates,
backs up, applies, and can restore the platform-specific configuration.

## Review the plan

```sh
docklane preflight --json
docklane install --dry-run --json
```

The plan reports blockers, pending operations, detected runtime state, and the
resources Docklane will manage. A token binds approval to that exact plan.

```sh
sudo docklane install --token 'COPY_THE_REVIEWED_TOKEN'
```

After installation:

```sh
docker compose ps
docklane doctor
```

The controller UI and API listen only on `127.0.0.1:4646`. Traefik is the only
managed service that binds host ports 80 and 443.
