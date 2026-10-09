#!/bin/sh
# Builds the Go release artifacts for one version into OUTDIR:
#
#   scripts/build-release.sh 0.2.0 /tmp/cuelight-dist
#
# 1. checks VERSION and every package.json agree with the requested version,
# 2. builds controller-web + website-node and embeds them into cuelight-control,
# 3. runs GoReleaser (local snapshot mode, nothing is published),
# 4. copies the archives and .debs into OUTDIR under the names
#    `cuelight-release manifest` classifies.
#
# Needs go, node, pnpm (with `pnpm install` done). GoReleaser comes from
# $GORELEASER, else the pinned copy under ~/.local/share/sdj/tools, else
# `go run` of the pinned version.
set -eu

goreleaser_version=v2.18.2

if [ "$#" -ne 2 ]; then
  echo "usage: scripts/build-release.sh VERSION OUTDIR" >&2
  exit 2
fi
version=$1
out=$2
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

node scripts/check-version.mjs --expect "$version"

pnpm build:web

pinned="$HOME/.local/share/sdj/tools/goreleaser/${goreleaser_version#v}/goreleaser"
if [ -n "${GORELEASER:-}" ]; then
  set -- "$GORELEASER"
elif [ -x "$pinned" ]; then
  set -- "$pinned"
else
  set -- go run "github.com/goreleaser/goreleaser/v2@$goreleaser_version"
fi
CUELIGHT_VERSION=$version "$@" release --snapshot --clean

mkdir -p "$out"
found=0
for artifact in dist/*_"$version"_*.tar.gz dist/*_"$version"_*.deb; do
  [ -f "$artifact" ] || continue
  cp "$artifact" "$out/"
  echo "$out/$(basename "$artifact")"
  found=$((found + 1))
done
if [ "$found" -eq 0 ]; then
  echo "build-release: GoReleaser produced no artifacts for $version" >&2
  exit 1
fi
