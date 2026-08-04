package com.huemux.app

import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

/**
 * Pins the diagnostics-routing wrapper. The two things that matter about it
 * are that failures reach the ring (via the injectable sink) and that a hot
 * error loop cannot flood the 800-line ring it shares with the Go side.
 *
 * The tests replace [HueLog.sink] with a list, so the Go library is never
 * actually loaded. `isReturnDefaultValues` in the Gradle config keeps the
 * android.util.Log stubs quiet.
 */
class HueLogTest {

    private var defaultSink: (String) -> Unit = {}

    @Before
    fun setUp() {
        HueLog.resetForTests()
        defaultSink = HueLog.sink
    }

    @After
    fun tearDown() {
        HueLog.sink = defaultSink
    }

    @Test
    fun errorLinesReachTheSinkWithSourceAndThrowable() {
        val sent = mutableListOf<String>()
        HueLog.sink = { sent.add(it) }
        HueLog.e("T", "nope", IllegalStateException("bad state"))
        assertEquals(1, sent.size)
        assertTrue(sent.first().contains("E/T: nope"))
        assertTrue(sent.first().contains("IllegalStateException"))
        assertTrue(sent.first().contains("bad state"))
    }

    @Test
    fun warningsAreRoutedButInfoIsNot() {
        val sent = mutableListOf<String>()
        HueLog.sink = { sent.add(it) }
        HueLog.w("T", "careful")
        HueLog.i("T", "noise")
        assertEquals(1, sent.size)
        assertEquals("W/T: careful", sent.first())
    }

    @Test
    fun burstBeyondTheWindowIsThrottled() {
        val sent = mutableListOf<String>()
        HueLog.sink = { sent.add(it) }
        repeat(200) { HueLog.e("T", "boom $it") }
        // The window admits MAX_LINES; the rest of the burst is dropped.
        assertEquals(HueLog.MAX_LINES, sent.size)
        // The first line made it through, so the burst was capped, not lost.
        assertEquals("E/T: boom 0", sent.first())
    }
}
