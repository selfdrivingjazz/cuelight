#!/bin/sh
# First install of a Cuelight appliance from an unpacked
# cuelight-player_<version>_linux_<arch>.tar.gz bundle (run as root from the
# bundle directory). Later versions arrive as signed updates applied by
# cuelight-updater.service, which switches /opt/cuelight/current.
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: install.sh /path/to/provisioned-node.env" >&2
  exit 2
fi
config=$1
bundle=$(cd "$(dirname "$0")" && pwd)
for file in cuelight-player cuelight-minimal-node; do
  if [ ! -x "$bundle/$file" ]; then
    echo "bundle is missing executable $file" >&2
    exit 1
  fi
done
for unit in cuelight-mpv.service cuelight-minimal-node.service cuelight-updater.service cuelight-updater.path; do
  if [ ! -f "$bundle/systemd/$unit" ]; then
    echo "bundle is missing systemd/$unit" >&2
    exit 1
  fi
done
if [ ! -f "$config" ]; then
  echo "provisioned node environment file is required" >&2
  exit 1
fi
version=$("$bundle/cuelight-player" --version | awk '{print $2}')
case "$version" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "cannot determine the bundle version (got '$version')" >&2; exit 1 ;;
esac

if ! id cuelight >/dev/null 2>&1; then
  useradd --system --user-group --home-dir /var/lib/cuelight --no-create-home --shell /usr/sbin/nologin cuelight
fi
install -d -o root -g root -m 0755 /etc/cuelight /opt/cuelight /opt/cuelight/versions
install -d -o cuelight -g cuelight -m 0750 /var/lib/cuelight /var/lib/cuelight/player /var/lib/cuelight/updates

target=/opt/cuelight/versions/$version
rm -rf "$target.tmp"
install -d -o root -g root -m 0755 "$target.tmp" "$target.tmp/systemd"
install -o root -g root -m 0755 "$bundle/cuelight-player" "$bundle/cuelight-minimal-node" "$target.tmp/"
install -o root -g root -m 0644 "$bundle"/systemd/* "$target.tmp/systemd/"
rm -rf "$target"
mv "$target.tmp" "$target"
ln -sfn "versions/$version" /opt/cuelight/current.next
mv -T /opt/cuelight/current.next /opt/cuelight/current
# Binaries used to live in /usr/bin; the units now run /opt/cuelight/current.
rm -f /usr/bin/cuelight-minimal-node
ln -sfn /opt/cuelight/current/cuelight-player /usr/bin/cuelight-player

for unit in cuelight-mpv.service cuelight-minimal-node.service cuelight-updater.service cuelight-updater.path; do
  install -o root -g root -m 0644 "$bundle/systemd/$unit" "/etc/systemd/system/$unit"
done
install -o root -g cuelight -m 0640 "$config" /etc/cuelight/minimal-node.env
systemctl daemon-reload
systemctl enable cuelight-mpv.service cuelight-minimal-node.service cuelight-updater.path
systemctl restart cuelight-mpv.service
systemctl restart cuelight-minimal-node.service
systemctl start cuelight-updater.path
