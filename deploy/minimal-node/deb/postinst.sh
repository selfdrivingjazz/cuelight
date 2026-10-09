#!/bin/sh
# cuelight-player (appliance) .deb postinst. The package unpacks binaries into
# /opt/cuelight/versions/<version>; /opt/cuelight/current selects the running
# version and is also flipped by `cuelight-player update apply`, so it is
# managed here rather than shipped in the package.
set -eu

# The versions/<v> directory this package ships (semver, unlike the Debian version).
version=$(dpkg-query -L cuelight-player | sed -n 's#^/opt/cuelight/versions/\([^/]*\)/cuelight-player$#\1#p' | head -n 1)
if [ -z "$version" ]; then
  echo "cuelight-player: cannot determine the packaged version directory" >&2
  exit 1
fi
if ! getent passwd cuelight >/dev/null; then
  useradd --system --user-group --home-dir /var/lib/cuelight --no-create-home --shell /usr/sbin/nologin cuelight
fi
install -d -o root -g root -m 0755 /opt/cuelight /opt/cuelight/versions /etc/cuelight
install -d -o cuelight -g cuelight -m 0750 /var/lib/cuelight /var/lib/cuelight/player /var/lib/cuelight/updates

# Never move `current` backwards past a newer version the updater installed.
current=$(readlink /opt/cuelight/current 2>/dev/null || true)
running=${current##*/}
if [ -z "$current" ] || [ ! -x "/opt/cuelight/$current/cuelight-player" ] || dpkg --compare-versions "$version" gt "$running"; then
  ln -sfn "versions/$version" /opt/cuelight/current.tmp
  mv -T /opt/cuelight/current.tmp /opt/cuelight/current
fi

if [ -d /run/systemd/system ]; then
  systemctl daemon-reload
  systemctl enable cuelight-mpv.service cuelight-minimal-node.service cuelight-updater.path >/dev/null
  if [ -f /etc/cuelight/minimal-node.env ]; then
    systemctl restart cuelight-mpv.service cuelight-minimal-node.service
    systemctl start cuelight-updater.path
  else
    cat <<'EOF'
cuelight-player is installed but not provisioned. Next:
  sudo -u cuelight cuelight-player provision --control-url https://<control>:8443 --token <enrollment token> ...
  then write /etc/cuelight/minimal-node.env (see README "Install a screen") and
  sudo systemctl start cuelight-mpv cuelight-minimal-node cuelight-updater.path
EOF
  fi
fi
