package sdj.cable.node

import java.net.URI

/** Persisted node settings. Pure data so URL logic is unit-testable off-device. */
data class NodeConfig(
    val serverUrl: String,
    val nodePath: String = DEFAULT_NODE_PATH,
    val startOnBoot: Boolean = false,
) {
    /** Full URL of the browser node page served by the control server. */
    fun nodeUrl(): String = joinUrl(serverUrl, nodePath)

    companion object {
        const val DEFAULT_NODE_PATH = "/node/"

        /**
         * Normalises operator input into an origin-ish base URL: adds a scheme
         * (https unless clearly a LAN/loopback host), strips query/fragment and
         * trailing slashes. Returns null when the input is not a usable http(s) URL.
         */
        fun normalizeServerUrl(input: String): String? {
            var text = input.trim()
            if (text.isEmpty() || text.any { it.isWhitespace() }) return null
            if (!text.contains("://")) {
                val host = text.substringBefore('/').substringBefore(':')
                text = (if (isLocalHost(host)) "http://" else "https://") + text
            }
            val uri = try { URI(text) } catch (_: Exception) { return null }
            val scheme = uri.scheme?.lowercase() ?: return null
            if (scheme != "http" && scheme != "https") return null
            val host = uri.host ?: return null
            if (host.isEmpty()) return null
            val port = if (uri.port == -1) "" else ":${uri.port}"
            val path = (uri.rawPath ?: "").trimEnd('/')
            return "$scheme://$host$port$path"
        }

        /** Normalises a node page path to start with '/'; blank means the default. */
        fun normalizeNodePath(input: String): String {
            val text = input.trim()
            if (text.isEmpty()) return DEFAULT_NODE_PATH
            return if (text.startsWith("/")) text else "/$text"
        }

        fun joinUrl(base: String, path: String): String = base.trimEnd('/') + normalizeNodePath(path)

        internal fun isLocalHost(host: String): Boolean {
            val h = host.lowercase()
            if (h == "localhost" || h.endsWith(".local") || h.endsWith(".lan")) return true
            val parts = h.split('.')
            if (parts.size != 4 || parts.any { p -> p.toIntOrNull()?.let { it !in 0..255 } ?: true }) return false
            val a = parts[0].toInt(); val b = parts[1].toInt()
            return a == 10 || a == 127 || (a == 192 && b == 168) || (a == 172 && b in 16..31)
        }
    }
}
