package com.jmilldotdev.cuelight.node

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import com.jmilldotdev.cuelight.node.InputAction.Touch

class RemoteInputTest {
    private fun ev(s: String) = JSONObject(s)

    @Test fun pointerScalesAndClampsToViewPixels() {
        val r = RemoteInput(1001, 501)
        assertEquals(listOf(InputAction.Pointer(Touch.HOVER, 500f, 500f)), r.map(ev("""{"type":"pointer","x":0.5,"y":2}""")))
        assertEquals(emptyList<InputAction>(), r.map(ev("""{"type":"pointer","y":0.5}""")))
    }

    @Test fun pressedPointerMovesAsTouchDragAndUpOnlyAfterDown() {
        val r = RemoteInput(101, 101)
        assertEquals(emptyList<InputAction>(), r.map(ev("""{"type":"pointer","x":0,"y":0,"state":"up"}""")))
        assertEquals(Touch.DOWN, (r.map(ev("""{"type":"pointer","x":0,"y":0,"state":"down"}""")).single() as InputAction.Pointer).touch)
        assertEquals(Touch.MOVE, (r.map(ev("""{"type":"pointer","x":0.1,"y":0}""")).single() as InputAction.Pointer).touch)
        assertEquals(Touch.UP, (r.map(ev("""{"type":"pointer","x":0.1,"y":0,"state":"up"}""")).single() as InputAction.Pointer).touch)
        assertEquals(listOf(Touch.DOWN, Touch.UP), r.map(ev("""{"type":"pointer","x":0.1,"y":0,"state":"click"}""")).map { (it as InputAction.Pointer).touch })
    }

    @Test fun wheelScrollsAtCursorWithAndroidSignAndClamp() {
        val r = RemoteInput(11, 11)
        r.map(ev("""{"type":"pointer","x":1,"y":1}"""))
        assertEquals(listOf(InputAction.Wheel(10f, 10f, -0f, -10f)), r.map(ev("""{"type":"wheel","deltaY":50}""")))
        assertEquals(emptyList<InputAction>(), r.map(ev("""{"type":"wheel"}""")))
    }

    @Test fun keysTextAndNavigation() {
        val r = RemoteInput(1, 1)
        assertEquals(listOf(InputAction.Key(66)), r.map(ev("""{"type":"key","key":"Enter"}""")))
        assertEquals(listOf(InputAction.Navigate("back")), r.map(ev("""{"type":"key","key":"BrowserBack"}""")))
        assertEquals(emptyList<InputAction>(), r.map(ev("""{"type":"key","key":"F13"}""")))
        assertEquals(listOf(InputAction.Text("hi")), r.map(ev("""{"type":"text","text":"hi"}""")))
    }

    @Test fun parseAcceptsBodyOrArrayAndRejectsOversizeOrGarbage() {
        val r = RemoteInput(1, 1)
        assertEquals(1, r.parse("""{"events":[{"type":"key"}]}""")!!.size)
        assertEquals(2, r.parse("""[{"type":"key"},{"type":"text"}]""")!!.size)
        assertNull(r.parse("nope"))
        assertNull(r.parse("[" + (1..257).joinToString(",") { "{}" } + "]"))
    }
}
