# AUR package: docklane-bin

Tracked packaging sources for the Arch User Repository package
[`docklane-bin`](https://aur.archlinux.org/packages/docklane-bin).

The package installs the verified GitHub release binaries. Host integration
remains an explicit Docklane operation after install.

## Update packaging for a release

After release artifacts exist (locally or from GitHub), update the tracked
packaging sources in the same release PR when practical:

```sh
./ops/bump-aur-docklane-bin.sh v0.1.0-alpha.3 dist/checksums.txt
```

This rewrites `PKGBUILD` and `.SRCINFO` with the upstream version, Arch
`pkgver` (hyphens become underscores), `pkgrel=1`, and both architecture
checksums.

Tagged releases still bump from the release `checksums.txt` in CI before
pushing to the AUR, so an outdated tracked copy does not block publishing.
Commit the bumped packaging afterward so `make aur-check` and local review
stay aligned with the live AUR package.

Validate on Arch before publishing:

```sh
cd packaging/aur/docklane-bin
makepkg --verifysource
makepkg --cleanbuild --syncdeps
namcap PKGBUILD docklane-bin-*.pkg.tar.zst
```

## Publish to the AUR

Manual publish from a machine that can SSH to `aur.archlinux.org`:

```sh
./ops/publish-aur-docklane-bin.sh packaging/aur/docklane-bin
```

Dry run (prepare the AUR commit without pushing):

```sh
AUR_DRY_RUN=1 ./ops/publish-aur-docklane-bin.sh
```

## GitHub Actions

Tagged releases run `publish-aur` after the GitHub Release job. Configure the
repository secret `AUR_SSH_PRIVATE_KEY` with an OpenSSH private key whose
matching public key is registered on the AUR account (`lcaohoanq`).

Prefer a dedicated key used only for AUR publishing (not your daily login
key). Add the `.pub` file under AUR **My Account**, then store the private key
as the GitHub Actions secret.

Until that secret exists, the job skips AUR publishing and the rest of the
release pipeline still succeeds.

Optional overrides:

| Variable | Purpose |
| --- | --- |
| `AUR_REPO_URL` | Override the AUR git URL |
| `AUR_COMMIT_NAME` / `AUR_COMMIT_EMAIL` | Commit identity on the AUR repo |
| `AUR_DRY_RUN=1` | Prepare commit without `git push` |
