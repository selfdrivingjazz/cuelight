#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: install.sh /path/to/cable-player /path/to/cable-minimal-node /path/to/provisioned-node.env" >&2
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
if ! id cable >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/cable --shell /usr/sbin/nologin cable
fi
install -d -o root -g root -m 0755 /etc/cable /usr/bin
install -d -o cable -g cable -m 0750 /var/lib/cable /var/lib/cable/player
install -o root -g root -m 0755 "$player" /usr/bin/cable-player
install -o root -g root -m 0755 "$minimal" /usr/bin/cable-minimal-node
install -o root -g root -m 0644 "$(dirname "$0")/cable-mpv.service" /etc/systemd/system/cable-mpv.service
install -o root -g root -m 0644 "$(dirname "$0")/cable-minimal-node.service" /etc/systemd/system/cable-minimal-node.service
install -o root -g cable -m 0640 "$config" /etc/cable/minimal-node.env
systemctl daemon-reload
systemctl enable cable-mpv.service cable-minimal-node.service
systemctl restart cable-mpv.service
systemctl restart cable-minimal-node.service
