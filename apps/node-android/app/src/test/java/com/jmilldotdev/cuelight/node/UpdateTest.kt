package com.jmilldotdev.cuelight.node

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.ByteArrayInputStream
import java.io.File
import java.io.IOException

class SemVerTest {
    @Test fun ordersBySemverPrecedenceLikeControl() {
        val ascending = listOf(
            "0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
            "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.2.0", "1.10.0", "10.0.0",
        )
        for (i in ascending.indices) for (j in ascending.indices) {
            assertEquals("${ascending[i]} vs ${ascending[j]}", i.compareTo(j), SemVer.compare(ascending[i], ascending[j]))
        }
    }

    @Test fun rejectsWhatTheControlRejects() {
        listOf("1.0", "1.0.0.0", "01.0.0", "1.0.0-", "1.0.0-a..b", "1.0.0+build", "v1.0.0", "", "1.0.x").forEach {
            assertNull(it, SemVer.parse(it))
        }
        assertEquals(SemVer(0, 0, 0, "dev"), SemVer.parse("0.0.0-dev"))
    }
}

class UpdateRequestTest {
    private val sha = "a".repeat(64)

    @Test fun parsesAndNormalisesTheBridgeBody() {
        val req = UpdateRequest.parse("""{"version":"0.3.0","apkUrl":"https://control.lan/updates/0.3.0/Cuelight-0.3.0-android.apk","apkSha256":"${sha.uppercase()}"}""")
        assertEquals(UpdateRequest("0.3.0", "https://control.lan/updates/0.3.0/Cuelight-0.3.0-android.apk", sha), req)
        assertNull(UpdateRequest.parse("""{"version":"0.3","apkUrl":"https://h/a.apk","apkSha256":"$sha"}"""))
        assertNull(UpdateRequest.parse("""{"version":"0.3.0","apkUrl":"file:///sdcard/a.apk","apkSha256":"$sha"}"""))
        assertNull(UpdateRequest.parse("""{"version":"0.3.0","apkUrl":"https://h/a.apk","apkSha256":"abc"}"""))
        assertNull(UpdateRequest.parse("not json"))
    }

    @Test fun ignoresAnythingNotStrictlyNewerThanRunning() {
        assertEquals("not-newer", UpdateRequest.ignoreReason("0.2.0", "0.2.0", null, null, 0))
        assertEquals("not-newer", UpdateRequest.ignoreReason("0.1.9", "0.2.0", null, null, 0))
        assertEquals("not-newer", UpdateRequest.ignoreReason("0.2.0-rc.1", "0.2.0", null, null, 0))
        assertEquals("not-newer", UpdateRequest.ignoreReason("0.3.0", "garbage", null, null, 0))
        assertNull(UpdateRequest.ignoreReason("0.2.0", "0.2.0-rc.1", null, null, 0))
        assertNull(UpdateRequest.ignoreReason("0.10.0", "0.9.0", null, null, 0))
    }

    @Test fun ignoresWhileBusyAndBacksOffAFailedVersionOnly() {
        assertEquals("in-progress", UpdateRequest.ignoreReason("0.4.0", "0.2.0", "0.3.0", null, 0))
        val failedAt = 1_000_000L
        val failure = "0.3.0" to failedAt
        assertEquals("recently-failed", UpdateRequest.ignoreReason("0.3.0", "0.2.0", null, failure, failedAt + UpdateRequest.FAILURE_BACKOFF_MS - 1))
        assertNull(UpdateRequest.ignoreReason("0.3.0", "0.2.0", null, failure, failedAt + UpdateRequest.FAILURE_BACKOFF_MS))
        assertNull(UpdateRequest.ignoreReason("0.3.1", "0.2.0", null, failure, failedAt + 1))
    }
}

class Sha256Test {
    @get:Rule val tmp = TemporaryFolder()

    private val helloSha = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" // "hello"

    @Test fun normalisesFingerprintSpellings() {
        val colons = helloSha.chunked(2).joinToString(":").uppercase()
        listOf(helloSha, "sha256:$helloSha", "SHA256:$colons", colons, " ${helloSha.uppercase()} ").forEach {
            assertEquals(it, helloSha, Sha256.normalize(it))
        }
        assertNull(Sha256.normalize(helloSha.dropLast(2)))
        assertNull(Sha256.normalize(helloSha.dropLast(1) + "g"))
    }

    @Test fun keepsADownloadOnlyWhenItsDigestMatches() {
        val dest = File(tmp.root, "a.apk")
        ApkFiles.writeVerified(ByteArrayInputStream("hello".toByteArray()), dest, helloSha)
        assertEquals("hello", dest.readText())
        assertEquals(helloSha, ApkFiles.sha256(dest))

        val bad = File(tmp.root, "b.apk")
        assertThrows(IOException::class.java) { ApkFiles.writeVerified(ByteArrayInputStream("hellO".toByteArray()), bad, helloSha) }
        assertEquals(listOf("a.apk"), tmp.root.list()!!.toList())
    }

    @Test fun abortsDownloadsOverTheSizeLimit() {
        val dest = File(tmp.root, "big.apk")
        assertThrows(IOException::class.java) { ApkFiles.writeVerified(ByteArrayInputStream(ByteArray(11)), dest, helloSha, limit = 10) }
        assertFalse(dest.exists())
        assertTrue(tmp.root.list()!!.isEmpty())
    }
}
