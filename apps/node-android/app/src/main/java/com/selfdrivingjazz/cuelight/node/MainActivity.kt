package com.selfdrivingjazz.cuelight.node

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.graphics.Bitmap
import android.os.Build
import android.os.Bundle
import android.os.SystemClock
import android.view.InputDevice
import android.view.KeyCharacterMap
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.View
import android.webkit.SslErrorHandler
import android.net.http.SslCertificate
import android.net.http.SslError
import android.net.Uri
import android.view.WindowManager
import android.webkit.JavascriptInterface
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import org.json.JSONObject
import java.util.concurrent.Executors

/**
 * Full-screen kiosk WebView hosting the browser node served by the control server.
 *
 * JS bridge `window.CuelightAndroid`:
 *  - `isAvailable()` → true
 *  - `injectInput(json)` → accepts `{events:[…]}` or `[…]` in the Cuelight remote
 *    event shape and dispatches real MotionEvent/KeyEvent input into this WebView,
 *    so pointer/keyboard input reaches cross-origin `web` presentations too.
 *  - `openSetup()` → shows the setup screen.
 *  - `appVersion()` → this shell's versionName.
 *  - `installUpdate(json)` → self-update; outcomes as `cuelight-android-update` events.
 */
class MainActivity : Activity() {
    private lateinit var web: WebView
    private lateinit var input: RemoteInput
    private var downTime = 0L
    private var loadedUrl: String? = null
    private var config: NodeConfig? = null
    private val updateListener: (JSONObject) -> Unit = ::dispatchUpdateEvent

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        WebView.setWebContentsDebuggingEnabled(true)

        web = WebView(this).apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.databaseEnabled = true
            settings.mediaPlaybackRequiresUserGesture = false
            settings.loadWithOverviewMode = true
            settings.useWideViewPort = true
            settings.userAgentString = settings.userAgentString + " CuelightNodeAndroid/" + BuildConfig.VERSION_NAME
            isFocusable = true; isFocusableInTouchMode = true
            webChromeClient = object : WebChromeClient() {
                override fun getDefaultVideoPoster(): Bitmap = Bitmap.createBitmap(1, 1, Bitmap.Config.ARGB_8888)
            }
            webViewClient = object : WebViewClient() {
                override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
                    // Retry the node page itself; subresource errors are the page's business.
                    if (request.isForMainFrame) view.postDelayed({ loadedUrl?.let { view.loadUrl(it) } }, 5000)
                }

                @SuppressLint("WebViewClientOnReceivedSslError") // proceeds only for a chain verified against the CA pin
                override fun onReceivedSslError(view: WebView, handler: SslErrorHandler, error: SslError) = handleSslError(handler, error)
            }
            addJavascriptInterface(Bridge(), "CuelightAndroid")
        }
        input = RemoteInput(1, 1)
        web.addOnLayoutChangeListener { v, _, _, _, _, _, _, _, _ -> input.resize(v.width, v.height) }
        setContentView(FrameLayout(this).apply { addView(web) })
        UpdateInstaller.listener = updateListener
        hideSystemUi()
    }

    override fun onResume() {
        super.onResume()
        hideSystemUi()
        web.onResume()
        val config = Prefs.load(this)
        if (config == null) { startActivity(Intent(this, SetupActivity::class.java)); return }
        if (config != this.config) { this.config = config; web.clearSslPreferences(); loadedUrl = null } // CA pin may have changed: reload
        UpdateInstaller.onResume(this)
        val url = config.nodeUrl()
        if (url != loadedUrl) { loadedUrl = url; web.loadUrl(url) }
        web.requestFocus()
    }

    override fun onPause() { web.onPause(); super.onPause() }

    override fun onDestroy() {
        if (UpdateInstaller.listener == updateListener) UpdateInstaller.listener = null
        web.destroy(); super.onDestroy()
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (hasFocus) hideSystemUi()
    }

    // D-pad/remote keys go to the WebView (focused) as DOM key events. Long-press
    // MENU opens setup so a TV with no touch can always get back to configuration.
    override fun onKeyLongPress(keyCode: Int, event: KeyEvent): Boolean {
        if (keyCode == KeyEvent.KEYCODE_MENU || keyCode == KeyEvent.KEYCODE_BACK) {
            startActivity(Intent(this, SetupActivity::class.java)); return true
        }
        return super.onKeyLongPress(keyCode, event)
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean {
        if (keyCode == KeyEvent.KEYCODE_MENU || keyCode == KeyEvent.KEYCODE_BACK) { event.startTracking(); return true }
        return super.onKeyDown(keyCode, event)
    }

    override fun onKeyUp(keyCode: Int, event: KeyEvent): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK && event.isTracking && !event.isCanceled) {
            if (web.canGoBack()) web.goBack()
            return true // kiosk: never exit on BACK
        }
        if (keyCode == KeyEvent.KEYCODE_MENU && event.isTracking && !event.isCanceled) return true
        return super.onKeyUp(keyCode, event)
    }

    @Suppress("DEPRECATION")
    private fun hideSystemUi() {
        window.decorView.systemUiVisibility = (View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY or View.SYSTEM_UI_FLAG_FULLSCREEN
            or View.SYSTEM_UI_FLAG_HIDE_NAVIGATION or View.SYSTEM_UI_FLAG_LAYOUT_STABLE
            or View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN or View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION)
    }

    private fun dispatch(action: InputAction) {
        val now = SystemClock.uptimeMillis()
        when (action) {
            is InputAction.Pointer -> {
                val type = when (action.touch) {
                    InputAction.Touch.DOWN -> { downTime = now; MotionEvent.ACTION_DOWN }
                    InputAction.Touch.MOVE -> MotionEvent.ACTION_MOVE
                    InputAction.Touch.UP -> MotionEvent.ACTION_UP
                    InputAction.Touch.HOVER -> MotionEvent.ACTION_HOVER_MOVE
                }
                val ev = MotionEvent.obtain(if (type == MotionEvent.ACTION_HOVER_MOVE) now else downTime, now, type, action.x, action.y, 0)
                if (type == MotionEvent.ACTION_HOVER_MOVE) { ev.source = InputDevice.SOURCE_MOUSE; web.dispatchGenericMotionEvent(ev) }
                else { ev.source = InputDevice.SOURCE_TOUCHSCREEN; web.dispatchTouchEvent(ev) }
                ev.recycle()
            }
            is InputAction.Wheel -> {
                val props = arrayOf(MotionEvent.PointerProperties().apply { id = 0; toolType = MotionEvent.TOOL_TYPE_MOUSE })
                val coords = arrayOf(MotionEvent.PointerCoords().apply {
                    x = action.x; y = action.y
                    setAxisValue(MotionEvent.AXIS_VSCROLL, action.dy); setAxisValue(MotionEvent.AXIS_HSCROLL, action.dx)
                })
                val ev = MotionEvent.obtain(now, now, MotionEvent.ACTION_SCROLL, 1, props, coords, 0, 0, 1f, 1f, 0, 0, InputDevice.SOURCE_MOUSE, 0)
                web.dispatchGenericMotionEvent(ev); ev.recycle()
            }
            is InputAction.Key -> {
                web.dispatchKeyEvent(KeyEvent(now, now, KeyEvent.ACTION_DOWN, action.keyCode, 0))
                web.dispatchKeyEvent(KeyEvent(now, now, KeyEvent.ACTION_UP, action.keyCode, 0))
            }
            is InputAction.Text -> {
                val events = KeyCharacterMap.load(KeyCharacterMap.VIRTUAL_KEYBOARD).getEvents(action.text.toCharArray())
                if (events != null) events.forEach { web.dispatchKeyEvent(it) }
                else web.dispatchKeyEvent(KeyEvent(now, action.text, KeyCharacterMap.VIRTUAL_KEYBOARD, 0)) // non-mappable chars (deprecated multi-char path)
            }
            is InputAction.Navigate -> when (action.action) {
                "back" -> if (web.canGoBack()) web.goBack()
                "forward" -> if (web.canGoForward()) web.goForward()
                "reload" -> web.reload()
            }
        }
    }

    inner class Bridge {
        @JavascriptInterface fun isAvailable(): Boolean = true

        /** Returns the number of events accepted, or -1 for an invalid body. */
        @JavascriptInterface fun injectInput(json: String): Int {
            val events = input.parse(json) ?: return -1
            web.post { events.forEach { e -> input.map(e).forEach(::dispatch) } }
            return events.size
        }

        @JavascriptInterface fun openSetup() {
            web.post { startActivity(Intent(this@MainActivity, SetupActivity::class.java)) }
        }

        /** The shell's versionName; the browser node reports it as `softwareVersion`. */
        @JavascriptInterface fun appVersion(): String = BuildConfig.VERSION_NAME

        /**
         * `{version, apkUrl, apkSha256}` → downloads, verifies and installs the APK. Returns true if
         * an install started; progress/outcome arrive as `cuelight-android-update` window events.
         */
        @JavascriptInterface fun installUpdate(json: String): Boolean = UpdateInstaller.request(this@MainActivity, json)
    }

    private fun dispatchUpdateEvent(detail: JSONObject) {
        web.evaluateJavascript("window.dispatchEvent(new CustomEvent('cuelight-android-update',{detail:$detail}))", null)
    }

    /** The DER of the certificate WebView rejected (API 29+ exposes it; older releases only via saveState). */
    private fun certificateDer(cert: SslCertificate): ByteArray? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) cert.x509Certificate?.encoded
        else SslCertificate.saveState(cert)?.getByteArray("x509-certificate")

    /**
     * Proceeds only for the configured server host with a CA pin, and only when the certificate
     * WebView saw is the leaf of a chain that verifies to the pinned root (checked off the UI thread).
     */
    private fun handleSslError(handler: SslErrorHandler, error: SslError) {
        val config = this.config
        val uri = Uri.parse(error.url)
        val host = uri.host
        val pin = config?.caSha256
        val leaf = error.certificate?.let(::certificateDer)
        if (config == null || pin == null || host == null || leaf == null || uri.scheme != "https" || !config.pinsHost(host)) {
            handler.cancel(); return
        }
        val port = if (uri.port == -1) 443 else uri.port
        val leafSha = Sha256.hex(leaf)
        tlsCheck.execute {
            val ok = PinnedTrust.serverPresents(host, port, pin, leafSha)
            runOnUiThread { if (ok) handler.proceed() else handler.cancel() }
        }
    }

    companion object {
        private val tlsCheck = Executors.newSingleThreadExecutor()
    }
}
