#!/bin/sh
# cuelight-control .deb postinst: service user, state directory, unit.
set -eu

state=/var/lib/cuelight-control
if ! getent passwd cuelight-control >/dev/null; then
  useradd --system --user-group --home-dir "$state" --no-create-home --shell /usr/sbin/nologin cuelight-control
fi
install -d -o cuelight-control -g cuelight-control -m 0700 "$state"

if [ -d /run/systemd/system ]; then
  systemctl daemon-reload
  systemctl enable cuelight-control.service >/dev/null
  if [ -f "$state/cuelight-control.env" ]; then
    systemctl restart cuelight-control.service
  else
    cat <<EOF
cuelight-control is installed but not initialized. Next:
  sudo -u cuelight-control cuelight-control init --dir $state --hostname <name> [--ip <addr>] --public-url https://<name>:8443 --database-url postgres://...
  sudo systemctl start cuelight-control
EOF
  fi
fi
