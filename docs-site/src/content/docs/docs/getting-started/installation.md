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

The [`docklane-bin`](https://aur.archlinux.org/packages/docklane-bin) AUR
package installs the Docklane release binary and declares its required host
packages:

```sh
yay -S docklane-bin
sudo systemctl enable --now docker systemd-resolved
docklane version
```

Do not run `yay` with `sudo`. Review the PKGBUILD when prompted, as you would
for any AUR package. If you do not use an AUR helper, install the prerequisites
with `pacman` and follow the [release archive instructions](./quick-start/#install-from-the-release-archive).

Installing the AUR package is only the package-installation phase. You must
still review and apply Docklane's managed installation below before the UI,
local DNS, HTTPS certificates, and gateway are available.

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
sudo docklane preflight --json
sudo docklane install --dry-run --json
```

The plan reports blockers, pending operations, detected runtime state, and the
resources Docklane will manage. Running these commands with `sudo` permits
Docker socket inspection without adding the user to the `docker` group. A
token binds approval to that exact plan.

```sh
sudo docklane install --token 'COPY_THE_REVIEWED_TOKEN'
```

After installation:

```sh
docklane doctor
```

Open `http://127.0.0.1:4646` to use the controller. The controller UI and API
listen only on `127.0.0.1:4646`. Traefik is the only managed service that binds
host ports 80 and 443.
