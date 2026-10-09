package com.jmilldotdev.cuelight.node

import android.content.Context

object Prefs {
    private const val FILE = "cuelight-node"

    fun load(context: Context): NodeConfig? {
        val p = context.getSharedPreferences(FILE, Context.MODE_PRIVATE)
        val server = p.getString("serverUrl", null) ?: return null
        return NodeConfig(
            serverUrl = server,
            nodePath = p.getString("nodePath", null) ?: NodeConfig.DEFAULT_NODE_PATH,
            startOnBoot = p.getBoolean("startOnBoot", false),
            caSha256 = p.getString("caSha256", null),
        )
    }

    fun save(context: Context, config: NodeConfig) {
        context.getSharedPreferences(FILE, Context.MODE_PRIVATE).edit()
            .putString("serverUrl", config.serverUrl)
            .putString("nodePath", config.nodePath)
            .putBoolean("startOnBoot", config.startOnBoot)
            .putString("caSha256", config.caSha256)
            .apply()
    }
}
