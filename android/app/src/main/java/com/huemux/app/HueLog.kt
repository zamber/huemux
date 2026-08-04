package com.huemux.app

import android.os.SystemClock
import android.util.Log
import com.huemux.mobile.Mobile

/**
 * Wraps [android.util.Log] so every Kotlin-side error or warning also lands in
 * the Go diagnostics ring buffer, which is what the in-app diagnostics report
 * prints. Without this a Kotlin exception was only visible in logcat —
 * unreachable on a phone without adb — so a bug report from a device never
 * mentioned the half of the app that lives in Kotlin.
 *
 * The report's "recent log" section is a fixed-size ring (800 lines, see
 * internal/debuglog). The rate limiter keeps an erroring hot loop from
 * flooding it: repeated lines from the same burst are dropped once the window
 * is full, which is fine because a flood of identical failures carries no
 * information beyond the first few.
 *
 * [sink] is the write path and is overridable so unit tests can count lines
 * without loading the Go library. It is deliberately internal, not private, so
 * tests in the same module can swap it; the app code only ever sees [e], [w]
 * and [i].
 */
object HueLog {

    /** Maximum lines pushed into the diagnostics ring per [WINDOW_MS]. */
    internal const val MAX_LINES = 50

    /** Throttle window, in milliseconds. */
    internal const val WINDOW_MS = 10_000L

    /**
     * Write path for the diagnostics ring. The default forwards to
     * [Mobile.logHost], which is safe before the server has started (the ring
     * is always running) and never throws in a way that should crash the app.
     */
    internal var sink: (String) -> Unit = { line ->
        try {
            Mobile.logHost(line)
        } catch (_: Throwable) {
            // A logging failure must not take the app down with it.
        }
    }

    private val stamps = ArrayList<Long>(MAX_LINES + 1)

    /**
     * Clears the throttle history. Unit tests call this so a throttling test
     * does not inherit the previous test's window.
     */
    internal fun resetForTests() {
        synchronized(stamps) { stamps.clear() }
    }

    /**
     * Returns true when the current burst has already sent [MAX_LINES] lines
     * within [WINDOW_MS]. Synchronized because the audio loop, the capture
     * thread and the WebView bridge all log concurrently.
     */
    private fun throttled(): Boolean = synchronized(stamps) {
        val now = SystemClock.elapsedRealtime()
        val cutoff = now - WINDOW_MS
        val it = stamps.iterator()
        while (it.hasNext() && it.next() < cutoff) it.remove()
        if (stamps.size >= MAX_LINES) return true
        stamps.add(now)
        return false
    }

    private fun send(prefix: String, tag: String, msg: String, t: Throwable?) {
        val line = buildString {
            append(prefix).append('/').append(tag).append(": ").append(msg)
            if (t != null) {
                append(" — ").append(t::class.java.simpleName)
                if (t.message != null) append(": ").append(t.message)
            }
        }
        if (!throttled()) sink(line)
    }

    /** Logs an error to logcat and the diagnostics ring. */
    fun e(tag: String, msg: String, t: Throwable? = null) {
        if (t != null) Log.e(tag, msg, t) else Log.e(tag, msg)
        send("E", tag, msg, t)
    }

    /** Logs a warning to logcat and the diagnostics ring. */
    fun w(tag: String, msg: String, t: Throwable? = null) {
        if (t != null) Log.w(tag, msg, t) else Log.w(tag, msg)
        send("W", tag, msg, t)
    }

    /**
     * Logs an informational line to logcat only. Infos are deliberately not
     * routed to the diagnostics ring: they are frequent and rarely the thing a
     * bug report is missing, and the ring is shared with the Go side.
     */
    fun i(tag: String, msg: String) {
        Log.i(tag, msg)
    }
}
