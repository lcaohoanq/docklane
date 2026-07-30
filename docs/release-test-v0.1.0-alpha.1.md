# v0.1.0-alpha.1 clean-VM release test

Date: 2026-07-29

This is the release-candidate acceptance record for the first Docklane alpha.
It follows the
[`v0.1.0-alpha.1` installation guide](https://github.com/lcaohoanq/docklane/blob/v0.1.0-alpha.1/docs/first-alpha.md)
using only a release archive on the target
machine.

## Environment

- Proxmox VM 129 restored from snapshot `docklane-clean`
- Debian 12.13, systemd, Linux `amd64`
- 20 GB disk
- Docker Engine 20.10.24
- `dnsmasq` installed and initially inactive
- `systemd-resolved` active
- no `proxy` network, Docklane manifest, managed containers, or cached
  `traefik:v3.7` image

## Artifact path

The candidate was built with:

```sh
./ops/build-release.sh v0.1.0-alpha.1 <empty-output-directory>
sha256sum --check checksums.txt
```

Only the Linux `amd64` archive and `checksums.txt` were transferred to the VM.
The archive checksum passed, `docklane version` reported
`v0.1.0-alpha.1`, and the included Dockerfile successfully produced
`docklane:local`.

## Blocker found and fixed

The first clean attempt failed while creating the managed gateway:

```text
No such image: traefik:v3.7
```

Container creation previously assumed every managed image was cached. The
Docker backend now recognizes Docker's explicit missing-image response, pulls
the exact image, and retries creation once. It does not retry unrelated
container-creation failures. Regression coverage exercises the API sequence.

The failed attempt also verified automatic rollback: the manifest reached
`rolled_back`, all applied host and Docker operations were reversed, and the
Traefik image remained absent.

## Passing rehearsal

After restoring `docklane-clean` again and rebuilding the candidate:

1. Debian prerequisites installed successfully.
2. The archive checksum and embedded version passed.
3. `docklane:local` built from the packaged Dockerfile.
4. Preflight found no blockers.
5. The reviewed install plan covered 27 managed resources.
6. Token-gated installation completed with no cached Traefik image.
7. `docklane doctor` passed.
8. A disposable `nginx:alpine` container started without a published port.
9. `docklane app enable docklane-demo --name demo` waited until ready.
10. `https://demo.docker.home.arpa` returned HTTP 200 with trusted TLS.
11. `docklane doctor demo` passed every control-plane, Docker, Traefik,
    upstream, DNS, TCP, redirect, certificate, and HTTPS check.
12. Docker inspection confirmed the application had no host port mapping.

Private installation state, the root CA key, and the leaf private key were
owned by root with mode `0600`.

## Uninstall verification

The demo route was disabled before rendering and applying the token-gated
uninstall plan. Uninstall:

- removed owned Traefik, controller, and probe containers;
- removed owned `proxy` and control networks and the probe volume;
- removed managed DNS and trust-anchor files;
- restored `/etc/resolv.conf` to the prior non-stub target;
- restored `dnsmasq` to its prior inactive state;
- detached only Docklane's owned endpoint from the demo container;
- retained the running user-owned demo container;
- retained non-empty route data and the generation-116 audit manifest.

## Documentation correction

The rehearsal showed that `docklane preflight` is a fresh-install
compatibility command, not a post-install health command. The installation
guide now uses `docklane doctor` for post-install verification and states the
distinction explicitly.

Result: **PASS**.
