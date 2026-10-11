# Releasing Cuelight

A release is a GitHub release `v<version>` on `jmilldotdev/cuelight` holding every
artifact plus `cuelight-release.json`, the manifest signed with the vendor release key.
Control servers, appliances, the desktop app, and the Android shell trust only manifests
that verify against the keys compiled into `internal/updates`.

## Cut a release

```sh
node scripts/set-version.mjs 0.3.0          # VERSION + every package.json (Android reads VERSION)
node scripts/check-version.mjs
git commit -am "release 0.3.0"
git push origin main                         # wait for ci to go green
git tag v0.3.0 && git push origin v0.3.0     # starts .github/workflows/release.yml
```

A version with a `-` suffix (`0.3.0-rc.1`) is published as a GitHub prerelease.

You can also start a release from the Actions tab or `gh workflow run release.yml -f
version=0.3.0 [-f skip_mac=true]`; it builds the selected branch (default `main`) and
creates the tag on that commit when publishing. `skip_mac=true` leaves out the macOS app
(for when the Apple secrets are unavailable); desktop Macs then cannot update to that
version.

## What CI does

`.github/workflows/ci.yml` (pushes to `main`, pull requests; Linux only): `go build`,
`go vet`, `go test` against a PostgreSQL 17 service (`CUELIGHT_POSTGRES_TEST_DSN`),
`goreleaser check`, `check-version.mjs`, `pnpm install --frozen-lockfile`, the
`@cuelight/api-client` generated-types drift check, `pnpm -r typecheck`, `pnpm -r test`.

`.github/workflows/release.yml` (tags `v*`, or manual):

1. **prepare** — the tag (or `version` input) must equal `VERSION` and every
   package.json (`check-version.mjs --expect`). `scripts/client-builds.sh` then decides
   which client jobs (3–6) run: a client is rebuilt only when something it is built from
   (the desktop app, the Go packages in its bundled player, `go.mod`/`go.sum`, the
   lockfile; or `apps/node-android`) changed since the newest release that shipped it.
   Version-only `package.json` bumps don't count. A server-only release therefore
   finishes in ~4 minutes and contains no client builds; screens keep the build from
   the release that last shipped one, and control still reports them as current.
   `-f rebuild_clients=true` forces every client build.
2. **go** — `scripts/build-release.sh`: web apps embedded into `cuelight-control`,
   GoReleaser archives and `.deb`s for control, CLI, and the player appliance bundle.
3. **desktop-mac** (`macos-15`) — sidecars for arm64 and x64, then one electron-builder run
   producing `zip` + `dmg` for both architectures and a single `latest-mac.yml`, signed
   with the Developer ID certificate and notarized. Fails if any Apple secret is missing
   unless `skip_mac`.
4. **desktop-windows** — NSIS x64 installer + `latest.yml` (unsigned: SmartScreen warns on
   first install).
5. **desktop-linux** — AppImage + deb on `ubuntu-24.04` (x64, `latest-linux.yml`) and
   `ubuntu-24.04-arm` (arm64, `latest-linux-arm64.yml`).
6. **android** — `assembleRelease` signed with the release keystore →
   `Cuelight-<v>-android.apk`.
7. **publish** — collects every artifact into one flat `dist/`, removes electron-builder
   by-products, signs `dist/cuelight-release.json` (`cuelight-release manifest`, which
   refuses unknown file names), verifies it against the files, and runs `gh release
   create v<v>` with everything.

Check a published release locally:

```sh
gh release download v0.3.0 -R jmilldotdev/cuelight -D /tmp/cuelight-0.3.0
go run ./cmd/cuelight-release verify --manifest /tmp/cuelight-0.3.0/cuelight-release.json --dir /tmp/cuelight-0.3.0
```

**Actions minutes.** The repo is private, so macOS minutes bill at 10× and Windows at 2×.
A release that rebuilds clients costs roughly one macOS job (signing + notarization,
~15–25 min → 150–250 billed minutes) plus one Windows job; server-only releases skip
both. CI stays on Linux for that reason. Use `skip_mac=true` for test releases.

**Actions storage.** Each job hands its files to `publish` as a run artifact (up to ~1 GB per
release, mostly the mac zip+dmg). The published GitHub release holds the files, so `publish`
deletes the run's artifacts as its last step, and every upload sets `retention-days: 1` so a
failed run's artifacts expire the next day. Release assets do not count against Actions
storage; the pnpm/Go caches have their own free 10 GB per-repo limit.

## GitHub secrets

| Secret | Contents |
| --- | --- |
| `CUELIGHT_RELEASE_SIGNING_KEY` | PKCS#8 PEM of the Ed25519 release key |
| `CUELIGHT_ANDROID_KEYSTORE_BASE64` | `base64` of the Android release keystore (`.jks`, alias `cuelight`) |
| `CUELIGHT_ANDROID_KEYSTORE_PASSWORD` | store and key password of that keystore |
| `CSC_LINK` | `base64` of the Developer ID Application certificate + private key (`.p12`) |
| `CSC_KEY_PASSWORD` | password of that `.p12` |
| `APPLE_API_KEY` | text of the App Store Connect API key (`AuthKey_<id>.p8`) |
| `APPLE_API_KEY_ID` | that key's Key ID |
| `APPLE_API_ISSUER` | the App Store Connect Issuer ID (UUID) |

### Apple (macOS signing and notarization)

Needs an Apple Developer Program membership (Account Holder or Admin for the certificate).

1. **Developer ID Application certificate.** On a Mac, Xcode → Settings → Accounts →
   Manage Certificates → `+` → *Developer ID Application* (or create it at
   developer.apple.com → Certificates with a CSR from Keychain Access). In Keychain
   Access, under *My Certificates*, select "Developer ID Application: … (TEAMID)" together
   with its private key, *Export 2 items…* as `cuelight-developer-id.p12`, and set a strong
   password.
2. **App Store Connect API key** (notarization). App Store Connect → Users and Access →
   Integrations → App Store Connect API → Team Keys → `+`, role *Developer*. Download
   `AuthKey_<KEYID>.p8` (only downloadable once) and note the **Key ID** and the
   **Issuer ID** shown above the key list.
3. Store them:

   ```sh
   R=jmilldotdev/cuelight
   base64 -i cuelight-developer-id.p12 | tr -d '\n' | gh secret set CSC_LINK -R $R
   gh secret set CSC_KEY_PASSWORD -R $R          # paste the .p12 password
   gh secret set APPLE_API_KEY -R $R < AuthKey_KEYID.p8
   gh secret set APPLE_API_KEY_ID -R $R --body KEYID
   gh secret set APPLE_API_ISSUER -R $R --body 00000000-0000-0000-0000-000000000000
   ```

   Then delete the local `.p12` export (keep the `.p8` in the password manager with the
   IDs). Re-export and update `CSC_LINK`/`CSC_KEY_PASSWORD` when the certificate expires
   (5 years); revoke and replace the API key if it leaks.

### Release signing key (custody and rotation)

The Ed25519 key signs every `cuelight-release.json`; anyone holding it can ship code to
every screen. It lives offline at `~/.config/sdj/cuelight/release-signing.key` (owner's
machine, `0600`, backed up in the password manager) and in the
`CUELIGHT_RELEASE_SIGNING_KEY` secret. Never commit it or paste it into a log.
Its public half is compiled into `internal/updates` (`trustedKeys`, id `cuelight-2026a`),
and `release.yml` signs with `--key-id cuelight-2026a`.

Clients only accept key ids they were built with, so rotation takes two releases:

1. Generate the new key and its public value:

   ```sh
   openssl genpkey -algorithm ed25519 -out ~/.config/sdj/cuelight/release-signing-2027a.key
   openssl pkey -in ~/.config/sdj/cuelight/release-signing-2027a.key -pubout -outform DER | tail -c 32 | base64
   ```

2. Add `"cuelight-2027a": "<base64>"` to `trustedKeys` next to the old id and ship a
   release still signed with the old key. Wait until the fleet runs it.
3. Switch `CUELIGHT_RELEASE_SIGNING_KEY` (`gh secret set CUELIGHT_RELEASE_SIGNING_KEY -R
   jmilldotdev/cuelight < new.key`) and `--key-id` in `release.yml`; release.
4. Once nothing older than step 2 remains, drop the old id from `trustedKeys`.

If the key leaks, do the same immediately and remove the old id in the step 2 release;
nodes that never install it must be updated by hand.

### Android keystore (custody)

`~/.config/sdj/cuelight/android-release.jks` (alias `cuelight`) with its password in
`android-release.password`, backed up in the password manager; CI has it as
`CUELIGHT_ANDROID_KEYSTORE_BASE64` / `CUELIGHT_ANDROID_KEYSTORE_PASSWORD`
(`base64 -w0 android-release.jks | gh secret set CUELIGHT_ANDROID_KEYSTORE_BASE64 -R
jmilldotdev/cuelight`). Android only installs updates signed with the same key, so losing
it means every Android screen must uninstall and reinstall. It cannot be rotated in place.

## Offline servers

A control server without internet access cannot poll the GitHub feed. On a connected
machine:

```sh
gh release download v0.3.0 -R jmilldotdev/cuelight -D cuelight-0.3.0
```

Copy the directory to the server (it must contain `cuelight-release.json`; include the
artifacts your screens need, or all of them) somewhere the control account can read, then
run the import as that account with the same environment as `serve` (Debian package
layout shown):

```sh
sudo -u cuelight-control sh -c 'set -a; . /var/lib/cuelight-control/cuelight-control.env; exec cuelight-control updates import /srv/cuelight-0.3.0'
```

It verifies the signature against the compiled-in keys and every file against the
manifest, stores the artifacts, and is safe to re-run to add missing files. Set
`CUELIGHT_CONTROL_UPDATE_CHECK=off` on such servers. Alternatively upload the files on the
controller's **Software → Import a release** page.
