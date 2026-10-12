package com.selfdrivingjazz.cuelight.node

import org.junit.Assert.assertThrows
import org.junit.Test
import java.security.cert.CertificateException
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate

/** Throwaway ECDSA P-256 fixtures (100-year validity): ROOT signed LEAF (CN/SAN control.lan); OTHER is unrelated. */
class PinnedTrustTest {
    private fun cert(pem: String) = CertificateFactory.getInstance("X.509").generateCertificate(pem.trimIndent().byteInputStream()) as X509Certificate

    private val root = cert(ROOT)
    private val other = cert(OTHER)
    private val leaf = cert(LEAF)
    private val rootPin = "276957cdbff15b270878ef1a1372580d9a00767c938e4cd230209a2a6d17eb93"
    private val otherPin = "682539dcd60d68e6d231f0c3286e664e09a743f67bcd39c52e15225a3c5c3b89"

    @Test fun acceptsAChainThatVerifiesToThePinnedRoot() {
        PinnedTrust.verifyChain(listOf(leaf, root), rootPin)
    }

    @Test fun rejectsAChainWithoutThePinnedCertificate() {
        assertThrows(CertificateException::class.java) { PinnedTrust.verifyChain(listOf(leaf, root), otherPin) }
        assertThrows(CertificateException::class.java) { PinnedTrust.verifyChain(listOf(leaf), rootPin) }
    }

    @Test fun rejectsAChainThatMerelyContainsThePinnedRoot() {
        // An attacker can append the (public) pinned root to any chain; the leaf must chain to it.
        assertThrows(CertificateException::class.java) { PinnedTrust.verifyChain(listOf(leaf, other), otherPin) }
    }

    @Test fun acceptsAPinnedSelfSignedServerCertificate() {
        PinnedTrust.verifyChain(listOf(root), rootPin)
    }

    private companion object {
        const val ROOT = """
            -----BEGIN CERTIFICATE-----
            MIIBjzCCATWgAwIBAgIUUmFpjXWt8ZJ43jITHMugUvrAZAEwCgYIKoZIzj0EAwIw
            FDESMBAGA1UEAwwJVGVzdCBSb290MCAXDTI2MTAwOTAwMjYyM1oYDzIxMjYwOTE1
            MDAyNjIzWjAUMRIwEAYDVQQDDAlUZXN0IFJvb3QwWTATBgcqhkjOPQIBBggqhkjO
            PQMBBwNCAAQ8lKMJU/CuSDjv5edbL1Pe16Gyv0nC2oVhRCDvzzSfkN/PTVdN8Z2f
            mOyXSR4Pr8BkF7U1xCerYd/mpHYt2uJOo2MwYTAdBgNVHQ4EFgQUIdLQj8XsxB05
            V7Uunv97jk71OTEwHwYDVR0jBBgwFoAUIdLQj8XsxB05V7Uunv97jk71OTEwDwYD
            VR0TAQH/BAUwAwEB/zAOBgNVHQ8BAf8EBAMCAQYwCgYIKoZIzj0EAwIDSAAwRQIh
            AMJMSvZWoBncuwm1P7XlHqE+oly5fQ31erjtWFsrIo6lAiAQiX2LsrqYhWD32zZP
            a/qTSY7RLA+0LQ5E7oG/cHlTtw==
            -----END CERTIFICATE-----
        """
        const val OTHER = """
            -----BEGIN CERTIFICATE-----
            MIIBkDCCATegAwIBAgIUdhezA2NDMLvqus5lo69K3CzB4bwwCgYIKoZIzj0EAwIw
            FTETMBEGA1UEAwwKT3RoZXIgUm9vdDAgFw0yNjEwMDkwMDI2MjRaGA8yMTI2MDkx
            NTAwMjYyNFowFTETMBEGA1UEAwwKT3RoZXIgUm9vdDBZMBMGByqGSM49AgEGCCqG
            SM49AwEHA0IABIFRPG0XSxrrZUorxNoBNO4B2cUMpxeKLW7UYEDWXVCYxZaSWC1i
            kOzuoQPy6izDilxtHnnUXIdHE8JuegMDVb+jYzBhMB0GA1UdDgQWBBSij0NACI2m
            Lg0shkwef/hv0hXb/DAfBgNVHSMEGDAWgBSij0NACI2mLg0shkwef/hv0hXb/DAP
            BgNVHRMBAf8EBTADAQH/MA4GA1UdDwEB/wQEAwIBBjAKBggqhkjOPQQDAgNHADBE
            AiAdqVlzibc3tA9tpfQNXM0Wkq8Tjhyjy26J3gDVSqsLWgIgIr7xxwHzWwvMTK/y
            NgE7ZlodIhXvgegIZKJLoR8G5vA=
            -----END CERTIFICATE-----
        """
        const val LEAF = """
            -----BEGIN CERTIFICATE-----
            MIIBtzCCAV2gAwIBAgIUOm3jaLAfOBTaCGExHhE5c8yq2O0wCgYIKoZIzj0EAwIw
            FDESMBAGA1UEAwwJVGVzdCBSb290MCAXDTI2MTAwOTAwMjYyNFoYDzIxMjYwOTE1
            MDAyNjI0WjAWMRQwEgYDVQQDDAtjb250cm9sLmxhbjBZMBMGByqGSM49AgEGCCqG
            SM49AwEHA0IABJQ49Amo0ZoA8XKzT6t/aYEFpAunzaCDuFG9pf7UAv6f+6YZCUHN
            I9aVIqH0gyvHpmOZTOZTML/i5NpuWPp4TL+jgYgwgYUwFgYDVR0RBA8wDYILY29u
            dHJvbC5sYW4wCQYDVR0TBAIwADALBgNVHQ8EBAMCB4AwEwYDVR0lBAwwCgYIKwYB
            BQUHAwEwHQYDVR0OBBYEFKH8r4cRdtkO+SOcj0qOp+lir6iUMB8GA1UdIwQYMBaA
            FCHS0I/F7MQdOVe1Lp7/e45O9TkxMAoGCCqGSM49BAMCA0gAMEUCIDlvTTtRqDqd
            zPjJa7OaFvFwRZuqA2UyKtPjPKAKBDmLAiEAyp/xlFZtGJ5XZX3aobFSDlPMWyzm
            fhm1QFfLGce6uds=
            -----END CERTIFICATE-----
        """
    }
}
