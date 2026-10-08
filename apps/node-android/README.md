# Cable Node for Android

Thin native Kotlin shell for phones/tablets and Android TV / Google TV / Fire TV
(minSdk 24, targetSdk 35). It hosts the browser node (`apps/website-node`, served
by the control server) in a full-screen kiosk WebView.

- Launcher + leanback launcher entry (TV banner), immersive full screen, keep-screen-on.
- Media autoplay without a user gesture (`mediaPlaybackRequiresUserGesture = false`).
- D-pad / remote keys reach the page as DOM key events. BACK never exits (goes back in
  history); **long-press BACK or MENU opens setup**.
- Optional start on boot (setup checkbox; `BOOT_COMPLETED` receiver).
- Setup screen: control server URL (scheme inferred: `http` for LAN/loopback/`.local`,
  else `https`) and browser node path (default `/node/` — must match where control serves
  website-node). QR scanning is not implemented.
- User-installed CAs are trusted, so a private control-server CA (e.g. cable-dev's
  `runtime/trust/ca.pem`) can be installed via Android Settings → Security → Install certificate.
- Main-frame load errors retry every 5 s.

## JS bridge: `window.CableAndroid`

Injected into every page the WebView loads (the node page is the only page it loads):

| method | |
| --- | --- |
| `isAvailable()` | `true` — feature detection |
| `injectInput(json)` | `{events:[…]}` or `[…]` in the Cable remote event shape (`key`/`text`/`pointer`/`wheel`, same as the desktop node `/input` body; `x`/`y` normalised 0..1). Dispatches real `MotionEvent`/`KeyEvent`s into the WebView, so input reaches cross-origin `web` presentations (iframes) at those coordinates. Returns events accepted, `-1` if invalid. |
| `openSetup()` | opens the setup screen |

Pointer mapping: `down`/`move`/`up` become a touch drag; `move` without a press is a mouse
hover; `click` is a touch tap; `wheel` is a mouse `ACTION_SCROLL` at the last pointer position.
For `web`-kind presentations the browser node, when `window.CableAndroid` exists,
forwards polled remote events to `injectInput` instead of handling them in JS — except
for the control-hosted Cable guide (`<control>/cable/?deployment=…`), which the node loads
same-origin with a guide token and which polls the remote session itself (Guide/Channel
and other remote buttons reach it without native injection).

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

```sh
export JAVA_HOME=~/.local/share/jdk ANDROID_HOME=~/.local/share/android-sdk
cd apps/node-android
./gradlew testDebugUnitTest assembleDebug
# APK: app/build/outputs/apk/debug/app-debug.apk (signed with the SDK debug key)
$ANDROID_HOME/platform-tools/adb install -r app/build/outputs/apk/debug/app-debug.apk
```

Fire TV: enable ADB debugging, `adb connect <ip>:5555`, then install as above.
WebView debugging is enabled: inspect via `chrome://inspect`.

Not verified on a device/emulator from the build host (no `/dev/kvm` access for the
build user); unit tests cover URL/config and remote-input mapping only.
