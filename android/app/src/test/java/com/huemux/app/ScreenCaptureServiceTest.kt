package com.huemux.app

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * Pins the pure helpers the capture pipeline is built on. These are the only
 * parts of [ScreenCaptureService] that run without a device: everything else
 * needs MediaProjection, a VirtualDisplay or an encoder. They are also the
 * parts where a wrong assumption is silent — a step that samples 4 pixels
 * where it should sample 2, or a signed-byte arithmetic bug that turns a dark
 * pixel into a bright one, corrupts the light output without throwing.
 */
class ScreenCaptureServiceTest {

    @Test
    fun pipelineStepKeepsTheLongEdgeWithinBudget() {
        // At or below the budget: no downsampling.
        assertEquals(1, ScreenCaptureService.pipelineStep(1, 1))
        assertEquals(1, ScreenCaptureService.pipelineStep(320, 180))
        assertEquals(1, ScreenCaptureService.pipelineStep(480, 270))
        // One pixel over the budget: round up to the next integer step.
        assertEquals(2, ScreenCaptureService.pipelineStep(481, 271))
        // A 1080p frame lands on 4 (long edge 1920 / 480).
        assertEquals(4, ScreenCaptureService.pipelineStep(1920, 1080))
        // Very long edges stay linear.
        assertEquals(11, ScreenCaptureService.pipelineStep(5000, 100))
        // A square beyond the budget.
        assertEquals(5, ScreenCaptureService.pipelineStep(2400, 2400))
    }

    @Test
    fun avg4AveragesTheFourSamplesAsUnsignedBytes() {
        // JVM bytes are signed; the pipeline treats each sample as 0..255.
        assertEquals(0, ScreenCaptureService.avg4(0, 0, 0, 0).toInt() and 0xff)
        // Four 255s (signed -1) average to 255.
        assertEquals(255, ScreenCaptureService.avg4(-1, -1, -1, -1).toInt() and 0xff)
        // 0,0,0,255 -> 63 (integer truncation, not rounding).
        assertEquals(63, ScreenCaptureService.avg4(0, 0, 0, -1).toInt() and 0xff)
        // 0,128,255,127 -> 127 (510 / 4 truncated).
        assertEquals(127, ScreenCaptureService.avg4(0, -128, -1, 127).toInt() and 0xff)
    }
}
