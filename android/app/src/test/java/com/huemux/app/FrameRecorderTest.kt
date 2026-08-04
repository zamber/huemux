package com.huemux.app

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * Pins the one pure computation in the recorder: the encoder bitrate. The rest
 * of the class talks to MediaCodec/MediaMuxer and needs a device.
 */
class FrameRecorderTest {

    @Test
    fun bitrateForStaysWithinTheConfiguredBounds() {
        // Tiny captures clamp to the 500 kbit/s floor.
        assertEquals(500_000, FrameRecorder.bitrateFor(320, 180))
        // 720p lands mid-range: 1280*720*3 = 2,764,800.
        assertEquals(2_764_800, FrameRecorder.bitrateFor(1280, 720))
        // Enormous captures cap at the 20 Mbit/s ceiling.
        assertEquals(20_000_000, FrameRecorder.bitrateFor(5000, 5000))
        // A zero-area frame still needs a legal bitrate, not 0.
        assertEquals(500_000, FrameRecorder.bitrateFor(0, 0))
    }
}
