# Docklane first-alpha installation guide

This guide installs Docklane's complete local gateway on one development
machine, creates an HTTPS route for a container with no published host port,
diagnoses it, and rolls the installation back.

Docklane is alpha software. Use it on a machine or VM whose DNS, trust store,
Docker networks, and ports 80/443 you are willing to let Docklane manage.

## Supported host

The first alpha supports:

- Debian 12 with systemd;
- Arch Linux with systemd;
- Docker Engine using `/var/run/docker.sock`;
- `dnsmasq` plus `systemd-resolved`;
- Linux `amd64` and `arm64`.

Other distributions, rootless Docker, remote Docker engines, non-systemd
resolvers, and purchased/public domains are not supported yet.

The installer:

- creates or adopts the `proxy` Docker network;
- creates the `docklane-control` network and probe socket volume;
- starts Traefik, the Docklane controller, and the restricted probe;
- binds only Traefik to host ports 80 and 443;
- binds the controller UI/API to `127.0.0.1:4646`;
- installs wildcard DNS for `*.docker.home.arpa`;
- creates and trusts a machine-local root CA;
- records every owned or adopted resource in
  `/var/lib/docklane/install-manifest.json`.

Docker socket access is equivalent to root-level control of the machine.
Docklane deliberately treats it as a privileged boundary.

## 1. Prepare the host

Ports 80 and 443 must be free unless an existing Traefik deployment is
compatible with Docklane's adoption checks.

On Debian 12:

```sh
sudo apt-get update
sudo apt-get install -y ca-certificates curl dnsmasq docker.io systemd-resolved
sudo systemctl enable --now docker systemd-resolved
```

On Arch Linux:

```sh
sudo pacman -Syu --needed ca-certificates curl dnsmasq docker
sudo systemctl enable --now docker systemd-resolved
```

Confirm the required services and Docker socket:

```sh
systemctl is-active systemd-resolved
sudo docker version
sudo test -S /var/run/docker.sock
```

Do not manually reconfigure `dnsmasq` for Docklane. The reviewed installer
will validate, back up, apply, and if necessary restore the platform-specific
configuration.

## 2. Download and verify the release

The commands below become available after the `v0.1.0-alpha.1` GitHub Release
is published.

```sh
set -euo pipefail

VERSION=v0.1.0-alpha.1
RELEASE_VERSION="${VERSION#v}"

case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64) ARCH=arm64 ;;
  *)
    echo "unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

BASE_URL="https://github.com/lcaohoanq/docklane/releases/download/${VERSION}"
ARCHIVE="docklane_${RELEASE_VERSION}_linux_${ARCH}.tar.gz"

curl --fail --location --remote-name "${BASE_URL}/${ARCHIVE}"
curl --fail --location --remote-name "${BASE_URL}/checksums.txt"

grep " ${ARCHIVE}$" checksums.txt | sha256sum --check --strict
tar -xzf "$ARCHIVE"
cd "docklane_${RELEASE_VERSION}_linux_${ARCH}"
./docklane version
```

Expected version output:

```text
docklane v0.1.0-alpha.1
```

Do not install an archive when its checksum fails.

## 3. Install the binary and local runtime image

The release directory includes a minimal Dockerfile. It packages the same
verified binary as the controller and probe image expected by the managed
installer:

```sh
sudo docker build --tag docklane:local .
sudo install -m 0755 docklane /usr/local/bin/docklane

docklane version
sudo docker image inspect docklane:local >/dev/null
```

No Go, Node.js, pnpm, or source checkout is required on the target machine.

## 4. Inspect before changing the machine

Preflight is read-only:

```sh
sudo docklane preflight
```

Warnings are informational. A failed check is a blocker that must be repaired
before installation. Pay particular attention to:

- an existing process on ports 80 or 443;
- an incompatible existing Traefik container;
- resolver loops or a missing `systemd-resolved` stub;
- an existing `docker.home.arpa` DNS/trust configuration;
- an existing Docklane installation manifest.

Now render the complete installation plan:

```sh
sudo docklane install --dry-run
```

This command does not generate private keys, create containers, or change host
files. Review every `managed`, `adopted`, and `block` entry. Copy the token
printed on the first plan line.

## 5. Apply the reviewed plan

```sh
INSTALL_TOKEN='<token from the fresh dry run>'
sudo docklane install --token "$INSTALL_TOKEN"
```

The token binds the exact preflight inventory and plan. If machine state
changes after the dry run, Docklane refuses the token. Run a new dry run,
review the difference, and use its new token.

If the command is interrupted, run the same command with the same token.
Docklane resumes its journaled operation instead of starting another
installation.

Verify the installed gateway:

```sh
sudo docklane preflight
docklane doctor
docklane discover
```

Open the controller UI at <http://127.0.0.1:4646>.

## 6. Create the first route

Start a disposable web container without `-p` or `ports:`:

```sh
sudo docker run --detach --name docklane-demo nginx:alpine
```

Create the route and wait for it to become reachable:

```sh
docklane app enable docklane-demo --name demo
```

Expected URL:

```text
https://demo.docker.home.arpa
```

Verify from the command line:

```sh
curl --fail --show-error --head https://demo.docker.home.arpa/
docklane doctor demo
```

Then open <https://demo.docker.home.arpa> in the browser. Chromium-based
browsers use the tested system-trust path. Restart a browser that was already
running when the local CA was installed.

For a Compose application, prefer its stable project/service identity:

```sh
docklane discover
docklane app guide PROJECT/SERVICE
docklane app enable PROJECT/SERVICE
```

The application must listen on its container interface and declare the
internal TCP port with Docker image `EXPOSE` or Compose `expose`. It does not
need a host `ports:` mapping or Traefik labels.

## 7. Diagnose a route

Do not start by restarting Traefik. Ask Docklane which layer failed:

```sh
docklane doctor demo
docklane doctor --json demo
```

Common outcomes:

- **publishing:** Traefik has not loaded the latest provider document yet;
- **Traefik 404:** the router is not active;
- **Bad Gateway:** the selected internal port, scheme, listener address, or
  proxy-network path is wrong;
- **certificate error:** the browser has not loaded the installed local CA;
- **DNS failure:** the host is bypassing the `systemd-resolved` stub.

`docklane app enable` waits up to 30 seconds and returns a diagnostic command
when the route does not become ready.

## 8. Back up alpha state

Before experimenting with upgrades, copy:

```text
/var/lib/docklane/install-manifest.json
/var/lib/docklane/data/
/var/lib/docklane/backups/
```

Preserve ownership and permissions. The manifest contains the ownership and
rollback contract; the data directory contains route and health history.
Private keys under `/var/lib/docklane` must remain private.

The `docklane upgrade` command migrates an older installation-manifest schema.
It is not yet a general product or container-image updater. The first alpha
does not promise in-place upgrades between arbitrary builds.

## 9. Review and uninstall

First disable application routing if desired:

```sh
docklane app disable demo
```

Preview the exact reverse operation:

```sh
sudo docklane uninstall --dry-run
```

Review which resources will be removed, restored, or preserved. Then apply
the displayed token:

```sh
UNINSTALL_TOKEN='<token from the fresh uninstall dry run>'
sudo docklane uninstall --token "$UNINSTALL_TOKEN"
```

Use the same token to resume an interrupted uninstall. Docklane preserves
adopted resources, retains non-empty controller data, and leaves the private
manifest as an audit tombstone.

After successful uninstall, the release binary and local image are no longer
managed by the manifest. Remove them only when you no longer need them:

```sh
sudo docker image rm docklane:local
sudo rm /usr/local/bin/docklane
```

The disposable demo can also be removed:

```sh
sudo docker rm --force docklane-demo
```

Do not manually delete retained `/var/lib/docklane` data until you have
decided that its routes, backups, and audit record are no longer needed.
