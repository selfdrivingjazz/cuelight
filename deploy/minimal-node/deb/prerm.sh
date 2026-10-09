#!/bin/sh
# cuelight-player (appliance) .deb prerm: stop units on removal; upgrades keep them running.
set -eu

if [ "${1:-}" = "remove" ] && [ -d /run/systemd/system ]; then
  systemctl disable --now cuelight-updater.path cuelight-minimal-node.service cuelight-mpv.service >/dev/null 2>&1 || true
fi
