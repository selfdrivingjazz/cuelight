# Cable

Cable is an open-source control plane and player runtime for sending media to physical screens.

The first supported path is a Raspberry Pi-class Linux player controlled through a stable HTTP API. Cable publishes immutable signed releases, assigns them to players, streams or caches content-addressed media, supervises playback, and reports what is actually running.

## Status

Early foundation work. No stable release or API compatibility guarantee yet.

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

Sections (left rail; `⌘K`/`Ctrl+K` jumps to any screen, media, playlist, deployment, or
profile by name; every entity has its own URL and the top bar shows its breadcrumb):

- **Overview** (`/`): controller health, on-air/attention counts, a screen wall, recent events.
- **Screens** (`/screens`): every display with its expected/captured picture. A screen
  (`/screens/{id}`) has **Now playing** (screenshot capture), **Assignment** (one media
  picker over files, web pages, and Cable guides; remote-control and QR toggles), **Remote** (phone link +
  QR), **Events**, and **System** (capabilities, revoke/unregister). **Pair a screen**
  (`/pair`) claims a browser screen's code or mints a desktop enrollment token.
- **Library**: media (`/library/media`, sortable by date added, name, size, or type via
  `?sort=`/`?order=`, cursor-paginated server-side), playlists, and raw files/uploads.
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
- **Programming**: deployments → channels → blocks/programs and playlists.
- **Releases** (`/releases`, read-only audit of the signed releases control publishes per
  assignment), **Activity**, and **Settings** (theme, operator session).

Frontend checks, from the repository root:

```sh
pnpm --filter @cable/controller-web typecheck
pnpm --filter @cable/controller-web test
pnpm --filter @cable/controller-web build
pnpm --filter @cable/controller-web e2e
```

## Web pages and phone remote

Web pages are media items (`kind: web`) in the one media library
(**Library → Media**, `POST /media {kind:"web", url}` or
`cable media add-web --url URL`). Assign any media by `mediaId`
(`PUT /nodes/{id}/assignment`, `cable assignment set --media-id`); control
publishes or reuses the signed release itself (file → `media`, web → `web`,
playlist → `media-schedule`). A `web` release shows a live website on a desktop
(Electron) node or a browser node (in an iframe); appliance nodes reject it.
Playlists may hold web media too; such schedules require the `web` feature.
Web thumbnails are 1920×1080 viewport captures taken by the controller with
headless Chromium on create, on `POST /media/{id}/refresh`, and every 6 hours
(`CABLE_CONTROL_WEB_CAPTURE_BROWSER`, otherwise the first `chromium`/`google-chrome`
on `PATH`; without one only node captures are used). A node screenshot taken
while a screen shows the page replaces the thumbnail when newer.

With **Allow remote control** on, the node opens a remote session for `web`
presentations. The phone page at `/remote/{session}` has a **Trackpad**
mode with a relative cursor, tap to click, two-finger tap for right click,
hold-then-drag, two-finger or strip scrolling, a keyboard, and
Back/Forward/Reload. On `web` releases the player forwards these events to the
desktop renderer, which injects them as native input. Get a phone link for a
node's active session on the screen's **Remote** tab (`/screens/{id}/remote`) or with
`cable node remote-link NODE_ID`; **Show QR** also draws one in the corner of the output.

## Hosted Cable guide

The Cable TV guide (`apps/cable-web`) is a web page served by control at
`https://<public origin>/cable/` (set `CABLE_CONTROL_CABLE_WEB_DIR` to its `dist`; the
dev stack builds and sets it on `sdj cable-dev apply`). Every deployment has a Cable
media item (`Cable · <name>`, kept in sync on rename, deleted with the deployment);
assigning it publishes a web release for `/cable/?deployment=<id>`. The node mints a
24-hour guide token (`POST /api/v1/player/cable-guide-tokens`, or
`/api/v1/browser-node/player/cable-guide-tokens`), allowed only while its assignment
targets that deployment's Cable item, and opens the page with `#token=<token>`. The
guide reads `/api/v1/cable-guide/presentation` (computed live from the deployment's
channels and blocks, so edits show without publishing), `/api/v1/cable-guide/media/{assetId}`,
and the node's remote session through `/api/v1/cable-guide/remote/events`; tokens stop
working when the node is reassigned, revoked, or unregistered. Because the guide
polls the remote session itself, the phone remote's Guide, channel, number and arrow
keys drive it on desktop, browser and Android nodes; nodes don't inject native input
into guide pages.

## Browser and smart-TV nodes

Any modern browser can be a node: control serves the `apps/website-node` build at
`https://<public origin>/node/` (set `CABLE_CONTROL_WEB_NODE_DIR` to its `dist`;
the dev stack does this and rebuilds it on `sdj cable-dev apply`).

1. On the TV (Samsung Tizen/LG webOS browser, Fire TV Silk, or any desktop
   browser), open `https://<public origin>/node/`. In dev:
   `https://cuelight.example.ts.net:8454/node/` (the TV must be on the
   tailnet, or use any origin with a certificate the TV trusts). Bookmark it or set it
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
| hosted Cable guide (`web`) | same-origin iframe with a guide token; the guide handles remote events itself |

Remote input: media keys (Enter/Space, volume, mute) control playback. A plain
browser can't send input into a cross-origin iframe, so `web` presentations ignore
remote events unless the Android WebView shell (`window.CableAndroid`) is present.
The build targets ES2019 with ES modules, which means Chromium 61+ or Safari 11+
(roughly Tizen 5+/2019 and webOS 5+/2020 TVs).

## License

Apache-2.0.
