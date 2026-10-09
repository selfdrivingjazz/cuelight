package com.jmilldotdev.cuelight.node

import android.app.Activity
import android.os.Bundle
import android.text.InputType
import android.view.Gravity
import android.view.ViewGroup.LayoutParams.MATCH_PARENT
import android.view.ViewGroup.LayoutParams.WRAP_CONTENT
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView

/** Control server URL entry. D-pad navigable; works on phones and TVs. */
class SetupActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val current = Prefs.load(this)
        val pad = (24 * resources.displayMetrics.density).toInt()

        val title = TextView(this).apply { text = "Cuelight setup"; textSize = 26f }
        val hint = TextView(this).apply {
            text = "Control server URL (e.g. https://cuelight.example.ts.net:8454 or 192.168.1.10:4179)"
            textSize = 16f
        }
        val server = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            setSingleLine(); setText(current?.serverUrl ?: ""); hint.let { this.hint = "https://…" }
        }
        val pathLabel = TextView(this).apply { text = "Browser node path"; textSize = 16f }
        val path = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            setSingleLine(); setText(current?.nodePath ?: NodeConfig.DEFAULT_NODE_PATH)
        }
        val caLabel = TextView(this).apply {
            text = "Server root CA SHA-256 (optional — only for a control server with a self-signed root; e.g. sha256:3f9a…)"
            textSize = 16f
        }
        val ca = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
            setSingleLine(); setText(current?.caSha256?.let { "sha256:$it" } ?: "")
        }
        val boot = CheckBox(this).apply { text = "Start on boot"; isChecked = current?.startOnBoot ?: false }
        val error = TextView(this).apply { setTextColor(0xFFF87171.toInt()) }
        val save = Button(this).apply {
            text = "Save and start"
            setOnClickListener {
                val url = NodeConfig.normalizeServerUrl(server.text.toString())
                if (url == null) { error.text = "Enter a valid http(s) URL"; return@setOnClickListener }
                val caText = ca.text.toString().trim()
                val pin = if (caText.isEmpty()) null else Sha256.normalize(caText)
                if (caText.isNotEmpty() && pin == null) { error.text = "The root CA SHA-256 must be 64 hex characters"; return@setOnClickListener }
                if (pin != null && !url.startsWith("https://")) { error.text = "A root CA pin needs an https:// server URL"; return@setOnClickListener }
                Prefs.save(this@SetupActivity, NodeConfig(url, NodeConfig.normalizeNodePath(path.text.toString()), boot.isChecked, pin))
                setResult(RESULT_OK); finish()
            }
        }

        val column = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL; gravity = Gravity.CENTER_HORIZONTAL
            setPadding(pad, pad, pad, pad)
            listOf(title, hint, server, pathLabel, path, caLabel, ca, boot, error, save).forEach { addView(it, LinearLayout.LayoutParams(MATCH_PARENT, WRAP_CONTENT)) }
        }
        setContentView(ScrollView(this).apply { addView(column) })
        server.requestFocus()
    }
}
