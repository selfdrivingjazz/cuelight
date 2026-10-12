package com.selfdrivingjazz.cuelight.node

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class NodeConfigTest {
    @Test fun addsHttpsForPublicHostsAndHttpForLan() {
        assertEquals("https://cuelight.example.ts.net:8454", NodeConfig.normalizeServerUrl(" cuelight.example.ts.net:8454/ "))
        assertEquals("http://192.168.1.10:4179", NodeConfig.normalizeServerUrl("192.168.1.10:4179"))
        assertEquals("http://localhost:4179", NodeConfig.normalizeServerUrl("localhost:4179"))
        assertEquals("http://tv.local", NodeConfig.normalizeServerUrl("tv.local"))
        assertEquals("https://172.32.0.1", NodeConfig.normalizeServerUrl("172.32.0.1"))
    }

    @Test fun keepsExplicitSchemeAndBasePathButDropsQueryAndFragment() {
        assertEquals("http://cuelight.example.com/sub", NodeConfig.normalizeServerUrl("HTTP://cuelight.example.com/sub/?x=1#token"))
    }

    @Test fun rejectsUnusableInput() {
        assertNull(NodeConfig.normalizeServerUrl(""))
        assertNull(NodeConfig.normalizeServerUrl("ftp://host"))
        assertNull(NodeConfig.normalizeServerUrl("javascript:alert(1)"))
        assertNull(NodeConfig.normalizeServerUrl("has space.com"))
        assertNull(NodeConfig.normalizeServerUrl("https://"))
    }

    @Test fun nodeUrlJoinsBaseAndPath() {
        assertEquals("https://h:1/node/", NodeConfig("https://h:1").nodeUrl())
        assertEquals("https://h/sub/website-node", NodeConfig("https://h/sub/", "website-node").nodeUrl())
        assertEquals("/node/", NodeConfig.normalizeNodePath("  "))
    }
}
