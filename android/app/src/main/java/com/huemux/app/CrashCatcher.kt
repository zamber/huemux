package com.huemux.app

import android.util.Log
import com.huemux.mobile.Mobile
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Global uncaught-exception handler. Its job is to leave a trace of a crash
 * somewhere a diagnostics report can find it, because an Android process that
 * crashes is gone — the Go ring buffer dies with it, and a report taken on the
 * next launch would otherwise describe a healthy app that had just been
 * restarted for no reason.
 *
 * Two writes happen, so the crash survives both directions:
 *  1. `crash-last.txt` in the app's files dir persists across the restart and
 *     is appended to the diagnostics report's host block (see
 *     [MainActivity.publishHostInfo]).
 *  2. [Mobile.logHost] pushes the same line into the Go ring, which interleaves
 *     it with the Go log lines in the "recent log" section while the process
 *     is still alive (e.g. when the handler fires but the process does not
 *     actually terminate, which some frameworks do).
 *
 * The previous handler is always chained, so the OS still gets the crash the
 * normal way.
 */
object CrashCatcher {

    private const val TAG = "HueMuxCrash"
    private const val FILE_NAME = "crash-last.txt"

    @Volatile
    private var file: File? = null

    /** Installs the handler. Idempotent. */
    fun install(dir: File) {
        file = File(dir, FILE_NAME)
        val prev = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            try {
                val line = describe(thread, throwable)
                file?.writeText(line + "\n" + throwable.stackTraceToString())
                Log.e(TAG, line)
                try {
                    Mobile.logHost("android: " + line.replace('\n', ' '))
                } catch (_: Throwable) {
                    // Nothing else to do — the process is about to die.
                }
            } catch (_: Throwable) {
                // The handler must never throw; it would mask the crash.
            }
            prev?.uncaughtException(thread, throwable)
        }
    }

    /**
     * A one-line summary of the most recent crash, or the empty string when
     * none has been recorded. Read by the diagnostics host block.
     */
    fun latest(): String {
        val f = file ?: return ""
        return try {
            f.takeIf { it.exists() }?.readText()?.lineSequence()?.firstOrNull() ?: ""
        } catch (_: Throwable) {
            ""
        }
    }

    private fun describe(thread: Thread, t: Throwable): String {
        val ts = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US).format(Date())
        val msg = t.message ?: t.toString()
        return "$ts UNCAUGHT ${t::class.java.simpleName} on ${thread.name}: $msg"
    }
}
