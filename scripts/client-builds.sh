#!/usr/bin/env bash
# Decides which client builds a release needs. A client is rebuilt only when
# something it is built from changed since the newest published release that
# contains that client's artifacts; otherwise screens keep that earlier build,
# which control still reports as current for them.
#
# Prints GitHub Actions outputs: mac=, windows=, linux=, android= (true|false).
# Needs full history with tags and `gh` (GH_TOKEN). FORCE=true rebuilds all.
set -euo pipefail
cd "$(dirname "$0")/.."

repo=${GITHUB_REPOSITORY:-jmilldotdev/cuelight}
force=${FORCE:-false}

# Go packages compiled into the desktop app's bundled player, plus the app itself.
mapfile -t player_dirs < <(go list -deps -f '{{if not .Standard}}{{.Dir}}{{end}}' ./cmd/cuelight-player | sed "s|^$PWD/||" | grep -v '^/')
desktop_paths=(apps/node-desktop go.mod go.sum pnpm-lock.yaml "${player_dirs[@]}")
android_paths=(apps/node-android)

# "tag<TAB>asset" for every asset of every published release, newest first.
assets=$(gh api "repos/$repo/releases?per_page=100" --jq '.[] | select(.draft | not) | .tag_name as $t | .assets[] | "\($t)\t\(.name)"')

# Newest release tag, strictly before HEAD in history, with an asset matching the pattern.
base_for() {
  local pattern=$1 tag name
  while IFS=$'\t' read -r tag name; do
    [[ $name =~ $pattern ]] || continue
    git rev-parse -q --verify "refs/tags/$tag" >/dev/null || continue
    [ "$(git rev-parse "$tag^{commit}")" != "$(git rev-parse HEAD)" ] || continue
    git merge-base --is-ancestor "$tag" HEAD || continue
    echo "$tag"
    return
  done <<<"$assets"
}

# True when any path changed since the base tag. A package.json whose only
# change is its "version" line (every release bumps it) does not count.
changed_since() {
  local base=$1 file
  shift
  [ -n "$base" ] || { echo true; return; }
  while IFS= read -r file; do
    if [[ $file == */package.json ]] && ! git diff -U0 "$base" HEAD -- "$file" | grep -E '^[+-] ' | grep -vq '"version"'; then
      continue
    fi
    echo true
    return
  done < <(git diff --name-only "$base" HEAD -- "$@")
  echo false
}

decide() {
  local name=$1 pattern=$2 base result
  shift 2
  if [ "$force" = true ]; then
    result=true
  else
    base=$(base_for "$pattern")
    result=$(changed_since "$base" "$@")
    echo "$name: last shipped in ${base:-<none>}, rebuild=$result" >&2
  fi
  echo "$name=$result"
}

decide mac '^latest-mac\.yml$' "${desktop_paths[@]}"
decide windows '^latest\.yml$' "${desktop_paths[@]}"
decide linux '^latest-linux\.yml$' "${desktop_paths[@]}"
decide android '-android\.apk$' "${android_paths[@]}"
