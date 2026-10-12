# Cuelight for Android

Thin native Kotlin shell for phones/tablets and Android TV / Google TV / Fire TV
(minSdk 24, targetSdk 35). It hosts the browser node (`apps/website-node`, served
by the control server) in a full-screen kiosk WebView.

**One-time reinstall:** Android 0.2.x installs from before the app-ID change must be uninstalled before installing the new APK; then set up and pair the screen again.

- Launcher + leanback launcher entry (TV banner), immersive full screen, keep-screen-on.
- Media autoplay without a user gesture (`mediaPlaybackRequiresUserGesture = false`).
- D-pad / remote keys reach the page as DOM key events. BACK never exits (goes back in
  history); **long-press BACK or MENU opens setup**.
- Optional start on boot (setup checkbox; `BOOT_COMPLETED` receiver). The shell also relaunches
  itself after a self-update (`MY_PACKAGE_REPLACED`). Android 10+ blocks both background starts
  unless the app may "Display over other apps" — grant it on kiosk screens (Settings → Apps →
  Special app access, or `adb shell appops set com.selfdrivingjazz.cuelight.node SYSTEM_ALERT_WINDOW allow`).
- Setup screen: control server URL (scheme inferred: `http` for LAN/loopback/`.local`,
  else `https`), browser node path (default `/node/` — must match where control serves
  website-node) and an optional server root CA SHA-256 pin. QR scanning is not implemented.
- Main-frame load errors retry every 5 s.

## TLS to the control server

- Publicly trusted certificates (e.g. `tailscale cert`) need nothing.
- User-installed CAs are trusted, so a private CA (e.g. cuelight-dev's `runtime/trust/ca.pem`)
  can be installed via Android Settings → Security → Install certificate.
- **Root pin** (LAN controls with a self-signed ECDSA root): enter the
  SHA-256 of the root certificate's DER in setup — the `ca=sha256:<hex>` value of the control's
  join link, or `openssl x509 -in root.pem -outform DER | sha256sum`. Accepted spellings:
  `sha256:<hex>`, bare hex, colon-separated (openssl `-fingerprint`). Requires an `https://` URL.
  The pin applies only to the server URL's host and replaces system/user trust for it:
  `network_security_config` is static, so the shell handles it in
  `WebViewClient.onReceivedSslError`. WebView only exposes the leaf of a rejected chain, so the
  shell opens its own TLS connection to that host/port that trusts only a chain containing a
  certificate with the pinned digest and validating (signatures, dates) up to it, plus hostname
  verification; WebView proceeds only if its leaf equals the leaf that connection validated
  (cached per host/port/pin; anything else is cancelled). The server must send the root in its
  chain (Cuelight control does when its chain file ends in the self-signed root). APK downloads
  from that host use the same pinned trust. Changing the pin in setup clears WebView's SSL
  decisions and reloads the node page.

## Self-update

The browser node (website-node) reports `CuelightAndroid.appVersion()` as `softwareVersion`
with `updateMethod: android`; when control assigns a newer version it calls
`installUpdate(JSON.stringify({version, apkUrl, apkSha256}))`. The shell:

1. ignores the request (`ignored` event) when it is malformed (`reason: "invalid"`), the version
   is not strictly newer than its versionName (`not-newer`; semver precedence identical to the
   control's `internal/updates.CompareVersions`), another install is running (`in-progress`), or
   the same version failed/was declined less than 10 minutes ago (`recently-failed`);
2. downloads `apkUrl` into `cacheDir/updates/` (≤ 512 MiB; pinned trust for the server host),
   keeping the file only if its SHA-256 equals `apkSha256`, and checks the APK's package name and
   versionName match;
3. if "Install unknown apps" is not allowed for Cuelight (Android 8+), opens that settings page
   (`permission-required`) and continues when the user returns (fails if still not granted);
4. installs through a `PackageInstaller` session. On Android 12+ the session asks for no user
   action, which Android honours for self-updates (`UPDATE_PACKAGES_WITHOUT_USER_ACTION`);
   otherwise the system confirmation screen is shown (`confirming`). Android refuses APKs not
   signed with the installed app's key — debug-key installs cannot take release updates.

Each step is reported to the page as
`window.dispatchEvent(new CustomEvent('cuelight-android-update', {detail}))` with
`detail = {version, status, reason?, message?}`, `status` ∈ `ignored`, `downloading`,
`permission-required`, `confirming`, `installing`, `installed`, `failed`. `installUpdate` returns
`true` if an install started. The process is replaced by the install; the new version relaunches
(see the boot/relaunch note above) and its page reports the new `appVersion()`.

## JS bridge: `window.CuelightAndroid`

Injected into every page the WebView loads (the node page is the only page it loads):

| method | |
| --- | --- |
| `isAvailable()` | `true` — feature detection |
| `injectInput(json)` | `{events:[…]}` or `[…]` in the Cuelight remote event shape (`key`/`text`/`pointer`/`wheel`, same as the desktop node `/input` body; `x`/`y` normalised 0..1). Dispatches real `MotionEvent`/`KeyEvent`s into the WebView, so input reaches cross-origin `web` presentations (iframes) at those coordinates. Returns events accepted, `-1` if invalid. |
| `openSetup()` | opens the setup screen |
| `appVersion()` | the shell's versionName (from the repo `VERSION`) |
| `installUpdate(json)` | self-update (see "Self-update" above); returns `true` if an install started |

Pointer mapping: `down`/`move`/`up` become a touch drag; `move` without a press is a mouse
hover; `click` is a touch tap; `wheel` is a mouse `ACTION_SCROLL` at the last pointer position.
For `web`-kind presentations the browser node, when `window.CuelightAndroid` exists,
forwards polled remote events to `injectInput` instead of handling them in JS — except
for app access pages (web media with `appAccess`), which the node opens with an app token
in the URL fragment and which poll `/app/remote/events` themselves (no native injection).

## Toolchain setup (user-local, no sudo)

```sh
mkdir -p ~/.local/share/jdk ~/.local/share/android-sdk/cmdline-tools
curl -L "https://api.adoptium.net/v3/binary/latest/17/ga/linux/x64/jdk/hotspot/normal/eclipse" | tar xz -C ~/.local/share/jdk --strip-components=1
curl -Lo /tmp/clt.zip https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip
unzip -q /tmp/clt.zip -d /tmp/clt && mv /tmp/clt/cmdline-tools ~/.local/share/android-sdk/cmdline-tools/latest
export JAVA_HOME=~/.local/share/jdk ANDROID_HOME=~/.local/share/android-sdk
yes | $ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager --licenses
$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager "platform-tools" "platforms;android-35" "build-tools;35.0.0" "build-tools;34.0.0"
```

## Build, test, install

versionName comes from the repo-root `VERSION` file (the build fails without it); versionCode
is `major*10000 + minor*100 + patch`.

```sh
export JAVA_HOME=~/.local/share/jdk ANDROID_HOME=~/.local/share/android-sdk
cd apps/node-android
./gradlew testDebugUnitTest assembleDebug
# APK: app/build/outputs/apk/debug/app-debug.apk (signed with the SDK debug key)
$ANDROID_HOME/platform-tools/adb install -r app/build/outputs/apk/debug/app-debug.apk
```

Release (the artifact published as `Cuelight-<v>-android.apk`): signing settings are read from
gradle properties (`-P…`) or the environment — `CUELIGHT_ANDROID_KEYSTORE` (keystore path) and
`CUELIGHT_ANDROID_KEYSTORE_PASSWORD` (store and key password; key alias `cuelight`).

```sh
CUELIGHT_ANDROID_KEYSTORE=~/.config/sdj/cuelight/android-release.jks \
CUELIGHT_ANDROID_KEYSTORE_PASSWORD="$(cat ~/.config/sdj/cuelight/android-release.password)" \
  ./gradlew assembleRelease
# APK: app/build/outputs/apk/release/Cuelight-<v>-android.apk
$ANDROID_HOME/build-tools/35.0.0/apksigner verify --print-certs app/build/outputs/apk/release/Cuelight-*-android.apk
```

Without a keystore the release APK is built unsigned as `Cuelight-<v>-android-unsigned.apk`.

Fire TV: enable ADB debugging, `adb connect <ip>:5555`, then install as above.
WebView debugging is enabled: inspect via `chrome://inspect`.

### Emulator without `/dev/kvm` access for the build user

The build user is not in group `kvm`, but Docker can pass the device through:

```sh
sdkmanager "emulator" "system-images;android-35;google_apis;x86_64"
ANDROID_AVD_HOME=/tmp/avd avdmanager create avd -n test -k "system-images;android-35;google_apis;x86_64" -d pixel_5
docker run --rm --network host --device /dev/kvm -e ANDROID_AVD_HOME=/tmp/avd -e HOME=/tmp/avd/home \
  -v $ANDROID_HOME:$ANDROID_HOME -v /tmp/avd:/tmp/avd ubuntu:24.04 bash -c \
  'apt-get update -qq && apt-get install -y -qq libpulse0 libx11-6 libgl1 libnss3 libxcomposite1 libxcursor1 libxi6 libxtst6 libasound2t64 libxdamage1 libxkbfile1 libdbus-1-3 libxrandr2 libxkbcommon0 libxkbcommon-x11-0 libegl1 >/dev/null;
   mkdir -p /tmp/avd/home && chown 1000:1000 /tmp/avd/home;
   exec setpriv --reuid=1000 --regid=1000 --groups=$(stat -c %g /dev/kvm) '$ANDROID_HOME'/emulator/emulator -avd test -no-window -no-audio -gpu swiftshader_indirect -no-snapshot -port 5580'
adb -s emulator-5580 …   # the emulator reaches the host as 10.0.2.2
```

Verified there (Android 15 emulator, release-signed builds): pinned-root page load (rejected
without the pin), pinned APK download, sha256 mismatch → `failed`, unknown-sources prompt →
grant → install, silent self-update (Android 12+), confirmation-screen path, relaunch after
update with "Display over other apps" granted (blocked without it). Not verified on Fire TV /
Android < 12 hardware.
