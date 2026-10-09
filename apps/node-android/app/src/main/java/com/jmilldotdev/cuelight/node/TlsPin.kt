package com.jmilldotdev.cuelight.node

import java.io.InputStream
import java.net.URL
import java.security.MessageDigest
import java.security.cert.CertPathValidator
import java.security.cert.CertificateException
import java.security.cert.CertificateFactory
import java.security.cert.PKIXParameters
import java.security.cert.TrustAnchor
import java.security.cert.X509Certificate
import java.util.concurrent.ConcurrentHashMap
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager

object Sha256 {
    fun hex(bytes: ByteArray): String = MessageDigest.getInstance("SHA-256").digest(bytes).toHex()

    /** Streams [input] to [sink] while hashing; returns the lowercase hex digest and byte count. */
    fun copy(input: InputStream, sink: (ByteArray, Int) -> Unit, limit: Long = Long.MAX_VALUE): Pair<String, Long> {
        val digest = MessageDigest.getInstance("SHA-256")
        val buffer = ByteArray(64 * 1024)
        var total = 0L
        while (true) {
            val n = input.read(buffer)
            if (n < 0) break
            total += n
            if (total > limit) throw java.io.IOException("download exceeds $limit bytes")
            digest.update(buffer, 0, n)
            sink(buffer, n)
        }
        return digest.digest().toHex() to total
    }

    /**
     * Normalises an operator-entered SHA-256 fingerprint: accepts an optional `sha256:` prefix,
     * `:`/space separators (openssl `-fingerprint` style) and any case. Null if not 32 bytes of hex.
     */
    fun normalize(input: String): String? {
        val text = input.trim().removePrefix("sha256:").removePrefix("SHA256:").removePrefix("SHA-256:")
            .filterNot { it == ':' || it.isWhitespace() }.lowercase()
        return if (text.length == 64 && text.all { it in '0'..'9' || it in 'a'..'f' }) text else null
    }

    private fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
}

/**
 * Trust for a control server with a self-signed root: a chain is accepted only if one of its
 * certificates has the pinned SHA-256 (of its DER encoding) and the chain validates (signatures,
 * validity dates) up to that certificate. System/user CAs play no part.
 */
object PinnedTrust {
    fun verifyChain(chain: List<X509Certificate>, pin: String) {
        if (chain.isEmpty()) throw CertificateException("empty certificate chain")
        val anchorIndex = chain.indexOfFirst { Sha256.hex(it.encoded) == pin }
        if (anchorIndex < 0) throw CertificateException("no certificate in the chain matches the pinned SHA-256")
        val anchor = chain[anchorIndex]
        anchor.checkValidity()
        if (anchorIndex == 0) return // the server certificate itself is pinned
        val path = CertificateFactory.getInstance("X.509").generateCertPath(chain.subList(0, anchorIndex))
        val params = PKIXParameters(setOf(TrustAnchor(anchor, null))).apply { isRevocationEnabled = false }
        try {
            CertPathValidator.getInstance("PKIX").validate(path, params)
        } catch (e: Exception) {
            throw CertificateException("chain does not verify to the pinned certificate: ${e.message}", e)
        }
    }

    fun trustManager(pin: String): X509TrustManager = object : X509TrustManager {
        override fun checkClientTrusted(chain: Array<out X509Certificate>, authType: String) = throw CertificateException("client certificates are not accepted")
        override fun checkServerTrusted(chain: Array<out X509Certificate>, authType: String) = verifyChain(chain.toList(), pin)
        override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
    }

    fun sslContext(pin: String): SSLContext = SSLContext.getInstance("TLS").apply { init(null, arrayOf(trustManager(pin)), null) }

    private val verifiedLeaves = ConcurrentHashMap<String, String>()

    /**
     * WebView only exposes the leaf of a chain it rejected, so the shell opens its own connection
     * to [host]:[port] with pinned trust (HttpsURLConnection also verifies the hostname) and accepts
     * the WebView's certificate only if it is the leaf that connection validated. Blocking; results
     * are cached per host/port/pin. Returns false on any failure.
     */
    fun serverPresents(host: String, port: Int, pin: String, leafSha256: String): Boolean {
        val key = "$host:$port|$pin"
        if (verifiedLeaves[key] == leafSha256) return true
        val leaf = try {
            val conn = URL("https", host, port, "/").openConnection() as HttpsURLConnection
            try {
                conn.sslSocketFactory = sslContext(pin).socketFactory
                conn.connectTimeout = 10_000
                conn.readTimeout = 10_000
                conn.connect() // TLS handshake + hostname verification; no request is sent
                Sha256.hex(conn.serverCertificates[0].encoded)
            } finally {
                conn.disconnect()
            }
        } catch (_: Exception) {
            return false
        }
        verifiedLeaves[key] = leaf
        return leaf == leafSha256
    }
}
