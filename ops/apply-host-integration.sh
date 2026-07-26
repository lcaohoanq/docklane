#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "This script must run as root." >&2
  exit 1
fi

project_dir=/home/lch/docker
docklane_dir="$project_dir/docklane"
traefik_dir="$project_dir/traefik"
ca_dir=/var/lib/traefik-lab-ca
root_ca="$ca_dir/rootCA.pem"
root_ca_key="$ca_dir/rootCA-key.pem"
root_ca_serial="$ca_dir/rootCA.srl"
certificate="$traefik_dir/certs/local.crt"
private_key="$traefik_dir/certs/local.key"
leaf_config="$traefik_dir/certs/lab-leaf.cnf"
dnsmasq_config=/etc/dnsmasq.d/lab.conf
wlan_dropin=/etc/systemd/network/20-wlan.network.d/50-lab-dns.conf
ethernet_dropin=/etc/systemd/network/20-ethernet.network.d/50-lab-dns.conf
temporary_dir="$(mktemp -d /tmp/docklane-host-integration.XXXXXX)"

cleanup() {
  case "$temporary_dir" in
    /tmp/docklane-host-integration.*)
      rm -rf "$temporary_dir"
      ;;
  esac
}
trap cleanup EXIT INT TERM
umask 077

if [ ! -r "$root_ca" ] || [ ! -r "$root_ca_key" ]; then
  echo "Expected CA files were not found in $ca_dir:" >&2
  ls -la "$ca_dir" >&2
  exit 1
fi
test -r "$leaf_config"
test -r "$docklane_dir/ops/dnsmasq-lab.conf"
test -r "$docklane_dir/ops/systemd-networkd-lab-dns.conf"

openssl req \
  -new \
  -newkey rsa:2048 \
  -nodes \
  -keyout "$temporary_dir/local.key" \
  -out "$temporary_dir/local.csr" \
  -config "$leaf_config"

openssl x509 \
  -req \
  -in "$temporary_dir/local.csr" \
  -CA "$root_ca" \
  -CAkey "$root_ca_key" \
  -CAserial "$root_ca_serial" \
  -CAcreateserial \
  -out "$temporary_dir/local.crt" \
  -days 365 \
  -sha256 \
  -extensions leaf_extensions \
  -extfile "$leaf_config"

openssl x509 \
  -in "$temporary_dir/local.crt" \
  -noout \
  -checkhost excalidraw.docker.home.arpa
openssl x509 \
  -in "$temporary_dir/local.crt" \
  -noout \
  -checkhost dockhand.lab

certificate_uid="$(stat -c %u "$certificate")"
certificate_gid="$(stat -c %g "$certificate")"
key_uid="$(stat -c %u "$private_key")"
key_gid="$(stat -c %g "$private_key")"

install -m 0600 -o "$key_uid" -g "$key_gid" \
  "$temporary_dir/local.key" "$private_key"
install -m 0644 -o "$certificate_uid" -g "$certificate_gid" \
  "$temporary_dir/local.crt" "$certificate"

install -m 0644 "$docklane_dir/ops/dnsmasq-lab.conf" "$dnsmasq_config"
install -d -m 0755 "$(dirname "$wlan_dropin")"
install -d -m 0755 "$(dirname "$ethernet_dropin")"
install -m 0644 \
  "$docklane_dir/ops/systemd-networkd-lab-dns.conf" "$wlan_dropin"
install -m 0644 \
  "$docklane_dir/ops/systemd-networkd-lab-dns.conf" "$ethernet_dropin"

dnsmasq --test
systemctl restart dnsmasq
networkctl reload

for interface_path in /sys/class/net/wl* /sys/class/net/en* /sys/class/net/eth*; do
  if [ -e "$interface_path" ]; then
    networkctl reconfigure "${interface_path##*/}" || true
  fi
done

resolvectl flush-caches
touch "$docklane_dir/data/host-integration-applied"
echo "Docklane host DNS and TLS integration applied."
