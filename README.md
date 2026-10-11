# Cuelight

Cuelight is an open-source control plane and player runtime for sending media to physical screens.

The first supported path is a Raspberry Pi-class Linux player controlled through a stable HTTP API. Cuelight publishes immutable signed releases, assigns them to players, streams or caches content-addressed media, supervises playback, and reports what is actually running.

## Status

Early foundation work. No stable release or API compatibility guarantee yet.

## Versions and releases

`VERSION` at the repository root is the release version (currently `0.2.0`). Change it
only with `node scripts/set-version.mjs X.Y.Z`, which also rewrites every workspace
`package.json` (the Android build reads `VERSION` directly); `node scripts/check-version.mjs
[--expect vX.Y.Z]` fails CI when they disagree. Go binaries get it at link time
(`-X github.com/jmilldotdev/cuelight/internal/buildinfo.Version=…`, unstamped builds say
`0.0.0-dev`) and print it with `--version`; `cuelight-control` logs it at startup and
reports it in `GET /health` (`{"status":"ok","version":"0.2.0"}`). Vite builds see it as
`__CUELIGHT_VERSION__`.

`scripts/build-release.sh 0.2.0 OUTDIR` builds controller-web and website-node, embeds
them into `cuelight-control`, and runs GoReleaser (`.goreleaser.yaml`, local snapshot
mode, nothing published) to produce:

- `cuelight-control_<v>_{linux,darwin}_{amd64,arm64}.tar.gz` and `cuelight-control_<v>_{amd64,arm64}.deb`
- `cuelight_<v>_{linux,darwin}_{amd64,arm64}.tar.gz` (operator CLI)
- `cuelight-player_<v>_linux_{amd64,arm64}.tar.gz` (appliance bundle: `cuelight-player`,
  `cuelight-minimal-node`, `install.sh`, `systemd/`) and `cuelight-player_<v>_{amd64,arm64}.deb`

Sign them with `go run ./cmd/cuelight-release manifest --version <v> --dir OUTDIR --out
OUTDIR/cuelight-release.json --key-file <release key>` and check with `cuelight-release
verify --manifest OUTDIR/cuelight-release.json --dir OUTDIR`. Published releases are built,
signed, and uploaded by `.github/workflows/release.yml`; see [RELEASING.md](RELEASING.md).

## Install a server

A server is one `cuelight-control` binary (API, controller web UI at `/`, browser-node app
at `/node/`, migrations — all embedded) plus PostgreSQL (17 is tested). `cuelight-control
init` generates everything else once:

```sh
cuelight-control init --dir DIR --hostname NAME [--hostname …] [--ip ADDR …] \
  --public-url https://NAME:8443 [--network-name "Name"] [--database-url postgres://…] \
  [--listen :8443] [--tls-cert chain.pem --tls-key key.pem] [--trusted-network CIDR …]
```

It writes `DIR/cuelight-control.env` (every `CUELIGHT_CONTROL_*` path), the node-client CA
(`DIR/pki/node-ca*.pem`), the release publisher key and seal key (`DIR/pki/`), `DIR/cas`,
`DIR/tmp`, and — unless `--tls-cert/--tls-key` point at an existing chain such as
`tailscale cert` output — a TLS server certificate: an ECDSA P-256 root valid 10 years
(`DIR/tls/server-root.pem`) and an ECDSA P-256 leaf for the given names/IPs and the
public URL host, valid 825 days, stored with the root as the chain `DIR/tls/server.pem`.
Files are `0600`, directories `0700`. It refuses to overwrite existing material
(`--exists-ok` turns "already initialized" into a no-op). Browsers and operator machines
must trust `server-root.pem`; screens get it through `cuelight-player provision --ca-file`
or a join link's `ca=sha256:` pin. Smart-TV browsers usually cannot add roots, so a
browser-node TV needs a publicly trusted certificate (`--tls-cert`).

Start with `set -a; . DIR/cuelight-control.env; set +a; cuelight-control serve`, then mint
the first operator token with `cuelight-control admin token create --scopes …` (same
environment; prints the secret once) and paste it into the controller at `/` — or skip
tokens on a private network (see "Operator access").

**Docker Compose** (PostgreSQL 17 + control, built from this checkout):

```sh
cd deploy/compose
cp .env.example .env    # set POSTGRES_PASSWORD, CUELIGHT_HOSTNAME, CUELIGHT_IPS, CUELIGHT_PORT
docker compose up -d --build --wait
docker compose run --rm admin-token
docker compose cp control:/var/lib/cuelight-control/tls/server-root.pem .
```

The one-shot `init` service fills the `control` volume on first start. The image
(`Dockerfile`) is Alpine with only `cuelight-control`; web thumbnail capture needs a
Chromium and is unavailable in it.

**Debian/Ubuntu package** (`cuelight-control_<v>_<arch>.deb`: `/usr/bin/cuelight-control`,
user `cuelight-control`, state `/var/lib/cuelight-control`, unit `cuelight-control.service`
that starts once the env file exists):

```sh
sudo apt install ./cuelight-control_0.2.0_amd64.deb postgresql
sudo -u postgres psql -c "CREATE ROLE cuelight LOGIN PASSWORD 'PW'" -c "CREATE DATABASE cuelight OWNER cuelight"
sudo -u cuelight-control cuelight-control init --dir /var/lib/cuelight-control \
  --hostname NAME --ip ADDR --public-url https://ADDR:8443 --network-name "Name" \
  --database-url 'postgres://cuelight:PW@127.0.0.1:5432/cuelight?sslmode=disable'
sudo systemctl start cuelight-control
sudo -u cuelight-control sh -c 'set -a; . /var/lib/cuelight-control/cuelight-control.env; exec cuelight-control admin token create --scopes security_admin,node_enroll,node_read,node_write,assignment_read,assignment_write,media_read,media_write,local_media_read,release_read,release_write'
```

**Tarball** (`cuelight-control_<v>_<os>_<arch>.tar.gz`, also macOS): unpack anywhere, run
`init` as the account that will run the server, and supervise `cuelight-control serve`
with the env file loaded (`systemd/cuelight-control.service` is the reference unit).

### Install a screen

- **Appliance** (Linux + MPV): unpack `cuelight-player_<v>_linux_<arch>.tar.gz` and run
  `sudo ./install.sh node.env`, or install `cuelight-player_<v>_<arch>.deb`; both use
  `/opt/cuelight/versions/<v>` with `/opt/cuelight/current` and take signed updates
  from control (`deploy/minimal-node/`).
- **Desktop app** (macOS/Windows/Linux): see "Desktop app: multiple networks" below.
- **Any browser or smart TV**: open `https://<public origin>/node/` (see "Browser and
  smart-TV nodes").

## Operator access

Operators authenticate in one of two ways:

- **Token**: `Authorization: Bearer cuelight_admin_…` from
  `cuelight-control admin token create --scopes …`. Scopes limit what it can do and
  every action is attributed to the token's actor. The controller asks for one at `/`.
- **Trusted network**: with `CUELIGHT_CONTROL_TRUSTED_NETWORKS` (comma-separated CIDRs,
  IPv4 and IPv6; `init --trusted-network CIDR`, repeatable; default empty = off), a
  request with **no** `Authorization` header whose TCP peer address is inside one of
  those ranges is the built-in **Trusted network** operator with every scope. The
  controller then opens straight into the app (Settings → Operator session shows
  `Trusted network (ADDRESS)` and still accepts a token), and `cuelight` CLI commands
  work without `--token`. `GET /api/v1/operator/me` reports `authMethod: token|network`.

A request that carries a token is always judged by that token, even from a trusted
address: a read-only token stays read-only, and a bad one is a 401. Network trust
never applies to screens (mTLS), browser screens, app tokens, or phone-remote links.

Because a trusted browser needs no credential, control refuses network-trusted
requests a third-party page could trigger: 403 when `Sec-Fetch-Site` is present and
not `same-origin`/`none`, or when `Origin` is present and is not the origin of
`CUELIGHT_CONTROL_PUBLIC_URL` (without a public URL: the request's own scheme and
host). Requests without those headers (curl, the CLI) pass. The operator API never
sends CORS headers; only `/api/v1/app/*` and `/api/v1/openapi.yaml` do.

Before enabling it:

- **Everyone in those ranges is a full operator**, including `security_admin`: they
  can mint and revoke tokens, enroll and unregister screens, and change any
  assignment. Anything running in a browser at control's origin on a trusted device —
  including HTML you upload as media — acts as that operator. Trust only networks
  where every device and person is an operator.
- **Trust is the TCP peer address**; `X-Forwarded-For` is ignored. Behind a reverse
  proxy or port forwarder (`tailscale serve`, nginx, Docker's userland proxy) every
  request arrives from the proxy's address, so trusting it trusts whoever can reach
  the proxy. Configure trusted networks only on a listener clients reach directly.

On a dedicated LAN, for example the Mars College network:

```sh
cuelight-control init --dir /var/lib/cuelight-control --hostname cuelight.mars \
  --ip 10.0.0.5 --public-url https://cuelight.mars:8443 --listen 10.0.0.5:8443 \
  --trusted-network 10.0.0.0/8 --trusted-network fd00::/8
```

On a tailnet, listen on the machine's tailnet address and trust
`100.64.0.0/10,fd7a:115c:a1e0::/48`. Existing installs add
`CUELIGHT_CONTROL_TRUSTED_NETWORKS=…` to the env file and restart.

## Development

Requires Go 1.27.

```sh
go fmt ./...
go vet ./...
go test ./...
./scripts/check.sh
```

## Control application

The React control application lives in `apps/controller-web`; its shared layout,
navigation, theme tokens, and UI primitives live in `packages/controller-ui`.

Sections (left rail; `⌘K`/`Ctrl+K` jumps to any screen, media, playlist, or
profile by name; every entity has its own URL and the top bar shows its breadcrumb):

- **Overview** (`/`): controller health, on-air/attention counts, a screen wall, recent events.
- **Screens** (`/screens`): every display with its expected/captured picture. A screen
  (`/screens/{id}`) has **Now playing** (screenshot capture), **Assignment** (one media
  picker over files, web pages, and playlists; remote-control and QR toggles), **Remote** (phone link +
  QR), **Events**, and **System** (capabilities, revoke/unregister). **Pair a screen**
  (`/pair`) claims a browser screen's code or mints a desktop join link.
- **Library**: media (`/library/media`, sortable by date added, name, size, or type via
  `?sort=`/`?order=`, cursor-paginated server-side), playlists (`/api/v1/playlists`,
  `media_read`/`media_write`; ordered file or web media items with per-item durations),
  and raw files/uploads.
  Every image or video gets a persisted 640px JPEG poster (`media.thumbnail`, served from
  `GET /media/{id}/thumbnail?v={digest}` as immutable): the controller generates it for
  PNG/JPEG/GIF at ingest and backfills on startup; for formats it cannot decode (video,
  WebP) the first operator UI to show the item captures a frame once and uploads it with
  `PUT /media/{id}/thumbnail`. Cards and tiles only ever load posters; the original video
  downloads when **Play video** is pressed on its detail page.
- **Profiles** (`/profiles`, under Operate): a profile is a saved set of screen
  assignments (screen → media + delivery, remote control, QR; `settings` holds future
  per-screen options). **Activate** (`POST /profiles/{id}/activate`) assigns every
  screen in the profile at once and leaves other screens untouched;
  `GET /profiles/active` reports the last-activated profile and which of its screens
  have drifted (`assignment_changed`, `unassigned`, `node_missing`).
- **Releases** (`/releases`, read-only audit of the signed releases control publishes per
  assignment), **Activity**, and **Settings** (theme, operator session).

Frontend checks, from the repository root:

```sh
pnpm --filter @cuelight/controller-web typecheck
pnpm --filter @cuelight/controller-web test
pnpm --filter @cuelight/controller-web build
pnpm --filter @cuelight/controller-web e2e
```

## Web pages and phone remote

Web pages are media items (`kind: web`) in the one media library
(**Library → Media**, `POST /media {kind:"web", url}` or
`cuelight media add-web --url URL`). Assign any media by `mediaId`
(`PUT /nodes/{id}/assignment`, `cuelight assignment set --media-id`); control
publishes or reuses the signed release itself (file → `media`, web → `web`,
playlist → `media-schedule`). A `web` release shows a live website on a desktop
(Electron) node or a browser node (in an iframe); appliance nodes reject it.
Playlists may hold web media too; such schedules require the `web` feature.
Web thumbnails are 1920×1080 viewport captures taken by the controller with
headless Chromium on create, on `POST /media/{id}/refresh`, and every 6 hours
(`CUELIGHT_CONTROL_WEB_CAPTURE_BROWSER`, otherwise the first `chromium`/`google-chrome`
on `PATH`; without one only node captures are used). A node screenshot taken
while a screen shows the page replaces the thumbnail when newer.

With **Allow remote control** on, the node opens a remote session for `web`
presentations. The phone page at `/remote/{session}` has a **Trackpad**
mode with a relative cursor, tap to click, two-finger tap for right click,
hold-then-drag, two-finger or strip scrolling, a keyboard, and
Back/Forward/Reload. On `web` releases the player forwards these events to the
desktop renderer, which injects them as native input. Get a phone link for a
node's active session on the screen's **Remote** tab (`/screens/{id}/remote`) or with
`cuelight node remote-link NODE_ID`; **Show QR** also draws one in the corner of the output.

Remote settings stick to the screen: an assignment that omits `remoteInput`/`showQr`
(the bulk assign, `cuelight play`, `cuelight assignment set` without remote flags) keeps
the screen's current values, and the remote session — with every phone already
connected — carries over while remote control stays on. Turn it off explicitly with
**Allow remote control** unticked, `remoteInput: false`, or `--no-remote` (which also
ends connected phones; they must scan again after it is turned back on).
Remote sessions live in control's memory, so after a control restart or upgrade a
screen's session is recreated on demand (by its app page, `remote-link`, or the node)
and phones connected before the restart scan the new code once.

## Web apps with app access

A web media item can be given **app access** (`appAccess: true` on `POST /media` or
`PATCH /media/{id}`; web media only, default off; the **Cuelight app** toggle in the
library). Its signed `web` release then carries `appAccess: true` and `mediaId`.
When presenting it, the node mints a 24-hour app token (`POST /api/v1/player/app-tokens`
for desktop/appliance nodes, `POST /api/v1/browser-node/player/app-tokens` for browser
nodes, body `{mediaId}`, allowed only while its assignment targets that item) and opens
the page with `#cuelight-token=<token>&cuelight-api=<encoded API base>` appended to any
existing fragment. If minting fails the presentation fails. Tokens stop working when
the node is reassigned, revoked, or unregistered; the page renews with
`POST /api/v1/app/token:renew`.

The page calls the read-only `/api/v1/app/*` endpoints with `Authorization: Bearer
<token>` (CORS `*`, no cookies): `app/context`, `app/media` (+ `/{id}`, `/content`,
`/thumbnail`; the last two also accept `?access_token=`), `app/playlists`, and
`app/remote/events` (long-poll the phone remote's events). Nodes don't inject native
input or draw their remote QR on app-access pages; the page handles remote input itself.
The spec is served at `GET /api/v1/openapi.yaml`. An integration server uses a normal
operator token with `media_read` (plus `media_write` to create its web media item).

Cuelight Cable, the cable-TV guide, now lives in `projects/cuelight-cable` as an
example app built on this contract.

## Browser and smart-TV nodes

Any modern browser can be a node: control serves the `apps/website-node` build embedded
in its binary at `https://<public origin>/node/`. Local builds embed whatever
`pnpm build:web` (or `node scripts/embed-web.mjs` after building an app) copied into
`internal/control/webui/assets/`; a binary built without it serves a "web assets not
built" page there. The dev stack rebuilds and embeds it on `sdj cuelight-dev apply`.

1. On the TV (Samsung Tizen/LG webOS browser, Fire TV Silk, or any desktop
   browser), open `https://<public origin>/node/` (the TV must reach that origin with a
   certificate it trusts). Bookmark it or set it
   as the browser home page.
2. The screen shows a code such as `K7QM-4XPD` and a QR code for
   `/pair?code=…`. Enter the code and a name on the controller's **Pair a screen** page
   (or `POST /api/v1/browser-nodes/claims {"code","name"}`).
3. The TV saves a 30-day bearer credential in `localStorage`, renews it after 15
   days, and appears in node lists with `nodeClass: "browser"`. Assign media to
   it like any other node. Revoke it with `POST /api/v1/browser-nodes/{id}/revoke`
   or by unregistering the node. The TV notices within one assignment poll (about
   25 s) and shows a new code.

Press any remote button or tap once after loading to go full screen and turn on
sound. Browsers block both until the user interacts; until then video plays muted.
The screen wake lock is used where the browser supports it.

| Presentation | Browser node |
| --- | --- |
| `media`, `media-schedule` | `<video>`/`<img>`; formats depend on the browser's codecs |
| `web` | iframe; sites that send `X-Frame-Options`/`frame-ancestors` stay blank |
| app-access `web` | iframe with `#cuelight-token=…`; the page handles remote events itself |

Remote input: media keys (Enter/Space, volume, mute) control playback. A plain
browser can't send input into a cross-origin iframe, so `web` presentations ignore
remote events unless the Android WebView shell (`window.CuelightAndroid`) is present.
The build targets ES2019 with ES modules, which means Chromium 61+ or Safari 11+
(roughly Tizen 5+/2019 and webOS 5+/2020 TVs).

## Desktop app: multiple networks

The Electron app (`apps/node-desktop`, macOS/Windows/Linux) turns a computer into a
node, and one app can belong to several Cuelight networks at once — e.g. a laptop on
both a home tailnet control and a venue LAN control.

**Joining.** On a controller's **Pair a screen** page, *Create join link* mints a
one-time enrollment token and shows `cuelight://join?v=1&url=…&token=…[&ca=sha256:…]&name=…`
(copy button + QR). Opening the link launches (or focuses) Cuelight, which asks for
confirmation and this computer's name on that network, then runs
`cuelight-player provision --control-url … [--ca-sha256 …]` with the token in the
environment. You can also paste the link into the app. Control needs
`CUELIGHT_CONTROL_PUBLIC_URL` to emit links; `CUELIGHT_CONTROL_NETWORK_NAME` names the
network (default: the public URL host). `ca` is included when the last certificate in
`CUELIGHT_CONTROL_TLS_CERT_PATH` is self-signed — serve the full chain (leaf + root)
so the player can pin it; publicly trusted chains (e.g. `tailscale cert`) omit it and
the player uses system roots. Recovery tokens have no link: use *Advanced: recover or
enroll with a token* (control URL, token, optional CA SHA-256 fingerprint).

**Layout.** `settings.json` in the app's user-data directory holds preferences and the
membership list (`{id, name, controlUrl, displayBinding, pinned?, enabled}`); each membership
has `networks/<id>/{identity,state,logs}` and its own `cuelight-player` sidecar with its
own loopback renderer endpoint and token. A pre-multi-network install migrates its
`identity/`, `state/`, `logs/` into `networks/default` on first launch. Removing a
network stops its sidecar and deletes its directory (after confirmation).

**Desired state.** `enabled` records whether a network should run: Start/Stop in the
setup window toggle it, newly joined networks are enabled, and every app launch —
including the restart after an update — starts all enabled networks. *Open Cuelight at
login* (`launchAtLogin`) is a single login entry for the app. Settings from builds with the
old `autoStart` flag migrate once: `autoStart: true` becomes `launchAtLogin: true` with every
network enabled; `false` leaves networks disabled until started.

**Displays and arbitration.** Each membership binds to `shared` (the primary display)
or a specific display; bindings to a disconnected display fall back to the primary.
Cuelight opens one output window per display in use. When several networks share a
display, the one whose sidecar most recently reported an assignment change
(`POST /v1/assignment`, ordered by its `changedAt`) is on air; the others are on
standby — their views stay loaded but hidden and muted, so switching is instant. A
**pin** (tray menu or setup window) keeps a network on its display regardless of newer
assignments elsewhere. Sidecars poll `GET /v1/output` and report `outputStandby` in their
capability report, so each controller can see when its screen is showing another
network. Esc on an output hides it until something new goes on air.

**Updates.** When control targets a newer version, the sidecar downloads and verifies the
platform's `latest*.yml` and referenced files into its state directory and calls
`POST /v1/software-update {version, feedDir}`. The app serves that directory on loopback
to electron-updater (generic provider) and downloads it. Because an operator targeted that
version, a staged update installs after a 10 s countdown ("Updating to X — restarting in
N s" in the setup window and tray) whether or not something is on air; the restart takes a
few seconds and enabled networks start again in the new version. Requests not newer than
the running or already-staged version, or arriving during the countdown, are ignored.

**Packaging.** `pnpm --filter @cuelight/node-desktop dist` cross-compiles every sidecar
and runs electron-builder: macOS `zip` + `dmg` per architecture (arm64 and x64 rather than
universal: half the download, and electron-updater picks the matching zip from
`latest-mac.yml`), hardened runtime with `build/entitlements.mac.plist`, the bundled
`cuelight-player` signed with the app, notarization when `APPLE_API_KEY`/`APPLE_ID`/
`APPLE_KEYCHAIN_PROFILE` credentials are present (unsigned local builds still work);
Windows NSIS; Linux AppImage + deb. The placeholder `generic` publish URL only makes
electron-builder emit `latest*.yml`; the app sets its feed at runtime.

## Software updates

Every Cuelight release is a set of artifacts plus `cuelight-release.json`, a manifest
signed with the vendor release key. Control mirrors releases and decides which version
each screen should run; screens verify the signature themselves, so a control server can
choose among signed versions but never supply code.

**Getting releases onto control** (all three verify against the keys compiled into
`cuelight-control`; an artifact is stored in CAS only when its sha256 and size match):

- *Feed*: control fetches `CUELIGHT_CONTROL_UPDATE_FEED_URL` (default: the latest GitHub
  release's `cuelight-release.json`) 30 s after start and every
  `CUELIGHT_CONTROL_UPDATE_CHECK_INTERVAL` (default `6h`), downloading the artifacts from
  the same base URL. `CUELIGHT_CONTROL_UPDATE_CHECK=off` disables it. Failures never stop
  control; the last error shows on the **Software** page (and *Check now* retries).
- *Offline / CLI*: `cuelight-control updates import DIR` on the server (same env file as
  `serve`; needs `CUELIGHT_CONTROL_DATABASE_URL` and `CUELIGHT_CONTROL_CAS_PATH`), where DIR
  holds `cuelight-release.json` and any of its artifacts. Re-running is idempotent and fills
  in missing files; it exits non-zero if a file does not match the manifest.
- *Upload*: **Software → Import a release** in the controller (choose
  `cuelight-release.json` plus the release files), i.e. `POST /api/v1/software/releases`
  as streamed `multipart/form-data` with the envelope as the first part.

**Rolling out.** The Software page lists every screen's running version, newest installable
release, target, and status (`current`, `outdated`, `updating`, `failed`, `unknown`,
`unsupported`); screen tiles and screen pages show *Out of date*, *Updating*, *Update failed*,
and *Standby* (the screen's display is showing another network) badges. *Update* /
*Cancel* per screen, or *Update all outdated*, set targets (`PUT|DELETE
/api/v1/software/nodes/{id}/target`, `POST /api/v1/software/rollouts`, scope `node_write`).
Control refuses a target below the version a screen runs. A target reaches the screen as
`Assignment.software` (even without content assigned); appliances and the desktop app
download the signed envelope and artifacts from `/api/v1/player/software/*` over mTLS.
Browser nodes load the website-node build control serves and reload when control's
`X-Cuelight-Version` response header differs from their build (once per version, never
while showing a pairing code); in the Android shell they also hand a newer signed APK to
`CuelightAndroid.installUpdate`. A banner on every controller page announces an imported
release newer than the server itself.

**Get the app.** `/updates/{version}/{name}` serves the artifacts of imported releases
without authentication, so the Software page's download links (macOS, Windows, Linux,
Android) work on a LAN with no internet access.

## License

Apache-2.0.
