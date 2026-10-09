#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: install.sh /path/to/cuelight-player /path/to/cuelight-minimal-node /path/to/provisioned-node.env" >&2
  exit 2
fi
player=$1
minimal=$2
config=$3
if [ ! -x "$player" ] || [ ! -x "$minimal" ]; then
  echo "player and minimal node binaries must be executable" >&2
  exit 1
fi
if [ ! -f "$config" ]; then
  echo "provisioned node environment file is required" >&2
  exit 1
fi
if ! id cuelight >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/cuelight --shell /usr/sbin/nologin cuelight
fi
install -d -o root -g root -m 0755 /etc/cuelight /usr/bin
install -d -o cuelight -g cuelight -m 0750 /var/lib/cuelight /var/lib/cuelight/player
install -o root -g root -m 0755 "$player" /usr/bin/cuelight-player
install -o root -g root -m 0755 "$minimal" /usr/bin/cuelight-minimal-node
install -o root -g root -m 0644 "$(dirname "$0")/cuelight-mpv.service" /etc/systemd/system/cuelight-mpv.service
install -o root -g root -m 0644 "$(dirname "$0")/cuelight-minimal-node.service" /etc/systemd/system/cuelight-minimal-node.service
install -o root -g cuelight -m 0640 "$config" /etc/cuelight/minimal-node.env
systemctl daemon-reload
systemctl enable cuelight-mpv.service cuelight-minimal-node.service
systemctl restart cuelight-mpv.service
systemctl restart cuelight-minimal-node.service
