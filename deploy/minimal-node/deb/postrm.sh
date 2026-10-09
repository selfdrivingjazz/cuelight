#!/bin/sh
# cuelight-player (appliance) .deb postrm: on purge remove updater-installed
# versions and the current link; provisioned identity in /var/lib/cuelight stays.
set -eu

if [ "${1:-}" = "purge" ]; then
  rm -rf /opt/cuelight/versions /opt/cuelight/current
  rmdir /opt/cuelight 2>/dev/null || true
fi
