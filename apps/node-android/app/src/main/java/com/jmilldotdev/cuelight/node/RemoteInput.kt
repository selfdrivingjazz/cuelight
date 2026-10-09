package com.jmilldotdev.cuelight.node

import org.json.JSONArray
import org.json.JSONObject

/**
 * Pure translation of Cuelight remote events (same shape as the desktop node's
 * /input body: key | text | pointer | wheel) into Android input actions.
 * Coordinates are normalised 0..1 and scaled to view pixels.
 */
sealed class InputAction {
    enum class Touch { DOWN, MOVE, UP, HOVER }
    data class Pointer(val touch: Touch, val x: Float, val y: Float) : InputAction()
    data class Wheel(val x: Float, val y: Float, val dx: Float, val dy: Float) : InputAction()
    /** Android KeyEvent keycode (KEYCODE_* integer value). */
    data class Key(val keyCode: Int) : InputAction()
    data class Text(val text: String) : InputAction()
    data class Navigate(val action: String) : InputAction()
}

class RemoteInput(private var width: Int, private var height: Int) {
    var cursorX = 0f; private set
    var cursorY = 0f; private set
    private var pressed = false

    fun resize(width: Int, height: Int) { this.width = width; this.height = height }

    fun parse(json: String): List<JSONObject>? {
        val root = try {
            if (json.trimStart().startsWith("[")) JSONArray(json) else JSONObject(json).optJSONArray("events")
        } catch (_: Exception) { null } ?: return null
        if (root.length() > MAX_EVENTS) return null
        return (0 until root.length()).mapNotNull { root.optJSONObject(it) }
    }

    fun map(event: JSONObject): List<InputAction> = when (event.optString("type")) {
        "pointer" -> pointer(event)
        "wheel" -> {
            val dx = clamp(event.optDouble("deltaX", 0.0), -10.0, 10.0).toFloat()
            val dy = clamp(event.optDouble("deltaY", 0.0), -10.0, 10.0).toFloat()
            if (dx == 0f && dy == 0f) emptyList()
            // MotionEvent AXIS_VSCROLL is positive for scrolling up; DOM deltaY is positive for down.
            else listOf(InputAction.Wheel(cursorX, cursorY, -dx, -dy))
        }
        "text" -> event.optString("text", "").take(MAX_TEXT).let { if (it.isEmpty()) emptyList() else listOf(InputAction.Text(it)) }
        "key" -> {
            val key = event.optString("key", "")
            when {
                NAVIGATION.containsKey(key) -> listOf(InputAction.Navigate(NAVIGATION.getValue(key)))
                KEYCODES.containsKey(key) -> listOf(InputAction.Key(KEYCODES.getValue(key)))
                else -> emptyList()
            }
        }
        else -> emptyList()
    }

    private fun pointer(event: JSONObject): List<InputAction> {
        val nx = event.optDouble("x", Double.NaN); val ny = event.optDouble("y", Double.NaN)
        if (!nx.isFinite() || !ny.isFinite()) return emptyList()
        val x = Math.round(clamp(nx, 0.0, 1.0) * (width - 1).coerceAtLeast(0)).toFloat()
        val y = Math.round(clamp(ny, 0.0, 1.0) * (height - 1).coerceAtLeast(0)).toFloat()
        cursorX = x; cursorY = y
        fun p(t: InputAction.Touch) = InputAction.Pointer(t, x, y)
        return when (event.optString("state", "move")) {
            "down" -> { pressed = true; listOf(p(InputAction.Touch.DOWN)) }
            "up" -> if (pressed) { pressed = false; listOf(p(InputAction.Touch.UP)) } else emptyList()
            "click" -> {
                val out = if (pressed) listOf(p(InputAction.Touch.UP)) else emptyList()
                pressed = false
                out + listOf(p(InputAction.Touch.DOWN), p(InputAction.Touch.UP))
            }
            else -> listOf(p(if (pressed) InputAction.Touch.MOVE else InputAction.Touch.HOVER))
        }
    }

    companion object {
        const val MAX_EVENTS = 256
        const val MAX_TEXT = 1024

        // Values are android.view.KeyEvent.KEYCODE_* constants (kept literal so this stays JVM-testable).
        val KEYCODES = mapOf(
            "ArrowUp" to 19, "ArrowDown" to 20, "ArrowLeft" to 21, "ArrowRight" to 22,
            "Backspace" to 67, "Delete" to 112, "Enter" to 66, "Escape" to 111, "Tab" to 61,
            "Home" to 122, "End" to 123, "PageUp" to 92, "PageDown" to 93, "Space" to 62,
        )
        val NAVIGATION = mapOf("BrowserBack" to "back", "BrowserForward" to "forward", "BrowserReload" to "reload")

        private fun clamp(v: Double, lo: Double, hi: Double) = if (v.isNaN()) 0.0 else v.coerceIn(lo, hi)
    }
}
