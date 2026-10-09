package com.jmilldotdev.cuelight.node

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * Launches the node after boot when enabled in setup. Android 10+ restricts
 * background activity starts; TV launchers/kiosk devices generally allow it,
 * phones may require the app to be set as launcher or granted
 * "display over other apps".
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != "android.intent.action.LOCKED_BOOT_COMPLETED") return
        val config = try { Prefs.load(context) } catch (_: Exception) { null } ?: return
        if (!config.startOnBoot) return
        context.startActivity(Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }
}
