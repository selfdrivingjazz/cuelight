package com.selfdrivingjazz.cuelight.node

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Log
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URI
import java.net.URL
import java.util.concurrent.Executors
import javax.net.ssl.HttpsURLConnection

/** `installUpdate` body: `{version, apkUrl, apkSha256}` (see the control's Android update bridge contract). */
data class UpdateRequest(val version: String, val apkUrl: String, val apkSha256: String) {
    companion object {
        fun parse(json: String): UpdateRequest? {
            val o = try { JSONObject(json) } catch (_: Exception) { return null }
            val version = o.optString("version").takeIf { SemVer.parse(it) != null } ?: return null
            val url = o.optString("apkUrl")
            val scheme = try { URI(url).scheme?.lowercase() } catch (_: Exception) { null }
            if (scheme != "https" && scheme != "http") return null
            val sha = Sha256.normalize(o.optString("apkSha256")) ?: return null
            return UpdateRequest(version, url, sha)
        }

        /** Why a request must be ignored, or null to proceed. Codes are reported as `reason`. */
        fun ignoreReason(version: String, running: String, active: String?, lastFailure: Pair<String, Long>?, now: Long): String? {
            if (active != null) return "in-progress"
            val order = SemVer.compare(version, running) ?: return "not-newer" // unparseable running version: never auto-update
            if (order <= 0) return "not-newer"
            if (lastFailure != null && lastFailure.first == version && now - lastFailure.second < FAILURE_BACKOFF_MS) return "recently-failed"
            return null
        }

        /** After a failed or declined install, the same version is not retried for this long. */
        const val FAILURE_BACKOFF_MS = 10 * 60 * 1000L
    }
}

object ApkFiles {
    const val MAX_APK_BYTES = 512L * 1024 * 1024

    /**
     * Streams [input] into [dest] (via a sibling temp file) and keeps it only when its SHA-256
     * equals [expectedSha256]; otherwise nothing is left behind and an [IOException] is thrown.
     */
    fun writeVerified(input: InputStream, dest: File, expectedSha256: String, limit: Long = MAX_APK_BYTES) {
        val tmp = File(dest.parentFile, dest.name + ".part")
        try {
            val (sha, _) = tmp.outputStream().use { out -> Sha256.copy(input, { b, n -> out.write(b, 0, n) }, limit) }
            if (sha != expectedSha256) throw IOException("sha256 mismatch: expected $expectedSha256, got $sha")
            if (!tmp.renameTo(dest)) throw IOException("cannot move download into place")
        } finally {
            tmp.delete()
        }
    }

    fun sha256(file: File): String = file.inputStream().use { Sha256.copy(it, { _, _ -> }).first }
}

/**
 * Self-update for the shell: download → sha256 check → PackageInstaller session. Android itself
 * refuses an APK not signed with the installed app's key and asks the user to confirm (unless
 * this app is already the installer of record on Android 12+). Outcomes are reported to [listener]
 * as `{version, status, reason?, message?}` with status one of `ignored`, `downloading`,
 * `permission-required`, `confirming`, `installing`, `installed`, `failed`.
 */
object UpdateInstaller {
    private const val TAG = "CuelightUpdate"
    const val ACTION_RESULT = "com.selfdrivingjazz.cuelight.node.UPDATE_RESULT"
    const val EXTRA_VERSION = "version"

    private val executor = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())

    /** Called on the main thread with each outcome; MainActivity forwards it to the page. */
    @Volatile var listener: ((JSONObject) -> Unit)? = null

    private var active: String? = null
    private var awaitingPermission: Pair<UpdateRequest, File>? = null
    private var lastFailure: Pair<String, Long>? = null

    /** Returns true when the request was accepted and an install started. */
    fun request(context: Context, json: String): Boolean {
        val req = UpdateRequest.parse(json)
        if (req == null) { emit(null, "ignored", reason = "invalid", message = "expected {version, apkUrl, apkSha256}"); return false }
        synchronized(this) {
            val reason = UpdateRequest.ignoreReason(req.version, BuildConfig.VERSION_NAME, active, lastFailure, System.currentTimeMillis())
            if (reason != null) { emit(req.version, "ignored", reason = reason); return false }
            active = req.version
        }
        val app = context.applicationContext
        emit(req.version, "downloading")
        executor.execute {
            try {
                val apk = download(app, req)
                verifyArchive(app, apk, req.version)
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && !app.packageManager.canRequestPackageInstalls()) {
                    synchronized(this) { awaitingPermission = req to apk }
                    emit(req.version, "permission-required", message = "allow Cuelight to install apps, then return")
                    main.post {
                        try {
                            app.startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:" + app.packageName))
                                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                        } catch (e: Exception) {
                            synchronized(this) { awaitingPermission = null }
                            failed(req.version, "cannot open the install-permission screen: ${e.message}")
                        }
                    }
                } else {
                    commit(app, req.version, apk)
                }
            } catch (e: Exception) {
                Log.w(TAG, "update to ${req.version} failed", e)
                failed(req.version, e.message ?: e.javaClass.simpleName)
            }
        }
        return true
    }

    /** MainActivity.onResume: continue an install that was waiting for the unknown-sources grant. */
    fun onResume(context: Context) {
        val pending = synchronized(this) { awaitingPermission.also { awaitingPermission = null } } ?: return
        val app = context.applicationContext
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && !app.packageManager.canRequestPackageInstalls()) {
            failed(pending.first.version, "permission to install apps was not granted")
            return
        }
        executor.execute {
            try { commit(app, pending.first.version, pending.second) } catch (e: Exception) { failed(pending.first.version, e.message ?: e.javaClass.simpleName) }
        }
    }

    internal fun confirming(version: String?) = emit(version, "confirming", message = "waiting for the user to confirm the update")

    internal fun installed(version: String?) {
        synchronized(this) { active = null }
        emit(version, "installed")
    }

    internal fun failed(version: String?, message: String) {
        synchronized(this) {
            active = null
            if (version != null) lastFailure = version to System.currentTimeMillis()
        }
        emit(version, "failed", message = message)
    }

    private fun download(context: Context, req: UpdateRequest): File {
        val dir = File(context.cacheDir, "updates").apply { mkdirs() }
        val dest = File(dir, "Cuelight-${req.version}-android.apk")
        dir.listFiles()?.forEach { if (it != dest) it.delete() } // older downloads
        if (dest.isFile && ApkFiles.sha256(dest) == req.apkSha256) return dest
        dest.delete()
        val url = URL(req.apkUrl)
        val conn = url.openConnection() as HttpURLConnection
        try {
            val config = Prefs.load(context)
            if (conn is HttpsURLConnection && config != null && config.pinsHost(url.host)) {
                conn.sslSocketFactory = PinnedTrust.sslContext(config.caSha256!!).socketFactory
            }
            conn.connectTimeout = 15_000
            conn.readTimeout = 60_000
            if (conn.responseCode != HttpURLConnection.HTTP_OK) throw IOException("download failed: HTTP ${conn.responseCode}")
            conn.inputStream.use { ApkFiles.writeVerified(it, dest, req.apkSha256) }
        } finally {
            conn.disconnect()
        }
        return dest
    }

    /** Refuse an APK whose package or versionName is not what the control announced. */
    private fun verifyArchive(context: Context, apk: File, version: String) {
        val info = context.packageManager.getPackageArchiveInfo(apk.path, 0) ?: throw IOException("downloaded file is not an APK")
        if (info.packageName != context.packageName) throw IOException("APK is for ${info.packageName}, not ${context.packageName}")
        if (info.versionName != version) throw IOException("APK version ${info.versionName} does not match announced $version")
    }

    private fun commit(context: Context, version: String, apk: File) {
        val installer = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(context.packageName)
            setSize(apk.length())
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
        }
        val sessionId = installer.createSession(params)
        try {
            installer.openSession(sessionId).use { session ->
                apk.inputStream().use { input ->
                    session.openWrite("base.apk", 0, apk.length()).use { out -> input.copyTo(out); session.fsync(out) }
                }
                val intent = Intent(context, InstallResultReceiver::class.java).setAction(ACTION_RESULT).putExtra(EXTRA_VERSION, version)
                val flags = PendingIntent.FLAG_UPDATE_CURRENT or (if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) PendingIntent.FLAG_MUTABLE else 0)
                session.commit(PendingIntent.getBroadcast(context, sessionId, intent, flags).intentSender)
            }
        } catch (e: Exception) {
            installer.abandonSession(sessionId)
            throw e
        }
        emit(version, "installing")
    }

    private fun emit(version: String?, status: String, reason: String? = null, message: String? = null) {
        val detail = JSONObject().put("status", status)
        if (version != null) detail.put("version", version)
        if (reason != null) detail.put("reason", reason)
        if (message != null) detail.put("message", message)
        Log.i(TAG, detail.toString())
        main.post { listener?.invoke(detail) }
    }
}

/** Receives PackageInstaller session status for [UpdateInstaller]. */
class InstallResultReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != UpdateInstaller.ACTION_RESULT) return
        val version = intent.getStringExtra(UpdateInstaller.EXTRA_VERSION)
        when (val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                val confirm = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                else @Suppress("DEPRECATION") intent.getParcelableExtra(Intent.EXTRA_INTENT)
                if (confirm == null) { UpdateInstaller.failed(version, "installer gave no confirmation screen"); return }
                try {
                    context.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                    UpdateInstaller.confirming(version)
                } catch (e: Exception) {
                    UpdateInstaller.failed(version, "cannot show the install confirmation: ${e.message}")
                }
            }
            PackageInstaller.STATUS_SUCCESS -> UpdateInstaller.installed(version)
            else -> UpdateInstaller.failed(version, intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: "install failed (status $status)")
        }
    }
}
