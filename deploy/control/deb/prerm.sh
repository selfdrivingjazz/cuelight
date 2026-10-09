#!/bin/sh
# cuelight-control .deb prerm: stop the service on removal (upgrades restart it in postinst).
set -eu

if [ "${1:-}" = "remove" ] && [ -d /run/systemd/system ]; then
  systemctl disable --now cuelight-control.service >/dev/null 2>&1 || true
fi
