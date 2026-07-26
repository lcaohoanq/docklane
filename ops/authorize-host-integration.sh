#!/bin/sh

echo "Docklane needs administrator access to:"
echo "  - sign the docker.home.arpa leaf certificate with the existing local CA"
echo "  - update dnsmasq and systemd-resolved routing"
echo

log_file=/home/lch/docker/docklane/data/host-integration.log
sudo /home/lch/docker/docklane/ops/apply-host-integration.sh >"$log_file" 2>&1
status=$?
cat "$log_file"

if [ "$status" -ne 0 ]; then
  echo
  echo "Docklane host integration failed with status $status."
  printf "Press Enter to close this window..."
  read -r _
fi

exit "$status"
