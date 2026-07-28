# Active integration rollback

The integration reuses the existing `proxy` network and
`docklane/data/docklane.db`. It does not remove application labels or
application networks.

## Roll back Traefik provider

Remove these arguments from `traefik/docker-compose.yml`:

```text
--providers.http.endpoint=http://docklane:4646/internal/traefik
--providers.http.pollinterval=2s
--providers.http.polltimeout=2s
```

Then recreate only Traefik:

```sh
cd /home/lch/docker/traefik
docker compose up -d --force-recreate traefik
```

Existing Docker-label routes remain available because the Docker provider is
not removed.

## Roll back Docklane service

```sh
cd /home/lch/docker/docklane
docker compose down
./bin/docklane serve
```

Both forms use the same database at
`/home/lch/docker/docklane/data/docklane.db`.

Compose shutdown also removes the restricted `docklane-probe` container.
Running the controller without `--probe-socket` preserves routing, but
`docklane doctor` reports direct upstream probing as unavailable. The
`docklane-probe-run` volume contains only the shared Unix socket and can be
removed after both Docklane services are stopped:

```sh
docker volume rm docklane-probe-run
```

After both Compose projects no longer reference it, the private control
network can be removed with:

```sh
docker network rm docklane-control
```

## Restore an automatic database migration backup

Numbered schema migrations create snapshots such as:

```text
data/backups/docklane-v<from>-before-v<to>-<timestamp>.db
```

Stop Docklane before replacing its database. Preserve the current file as an
additional rollback point, copy the selected snapshot, then restart:

```sh
docker compose stop docklane
cp data/docklane.db data/docklane-before-restore.db
cp data/backups/<selected-backup>.db data/docklane.db
chmod 600 data/docklane.db
docker compose up -d docklane
```

Use a backup only with a Docklane binary that supports that backup's schema
version.

## Roll back DNS and certificate

Restore the integration backup:

```sh
BACKUP=/home/lch/docker/docklane/data/backups/integration-20260726-1615
sudo cp "$BACKUP/lab.conf" /etc/dnsmasq.d/lab.conf
sudo cp "$BACKUP/networkd-lab-dns.conf" \
  /etc/systemd/network/20-wlan.network.d/50-lab-dns.conf
sudo cp "$BACKUP/networkd-lab-dns.conf" \
  /etc/systemd/network/20-ethernet.network.d/50-lab-dns.conf
sudo dnsmasq --test
sudo systemctl restart dnsmasq
sudo networkctl reload
sudo networkctl reconfigure wlan0
sudo resolvectl flush-caches

cp "$BACKUP/local.crt" /home/lch/docker/traefik/certs/local.crt
cp "$BACKUP/local.key" /home/lch/docker/traefik/certs/local.key
cp "$BACKUP/lab-leaf.cnf" /home/lch/docker/traefik/certs/lab-leaf.cnf
```

Restart Traefik after restoring the old certificate:

```sh
docker restart traefik
```
