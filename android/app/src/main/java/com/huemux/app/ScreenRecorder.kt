package com.huemux.app

import android.content.ContentValues
import android.content.Context
import android.hardware.display.DisplayManager
import android.hardware.display.VirtualDisplay
import android.media.MediaRecorder
import android.media.projection.MediaProjection
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.os.ParcelFileDescriptor
import android.provider.MediaStore
import android.util.Log
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Records the screen to an MP4 while screen sync is running.
 *
 * ## Why this is a second VirtualDisplay, not a second surface
 *
 * The obvious design — hang a MediaRecorder surface off the display the colour
 * pipeline already mirrors into — is not available: [MediaProjection.createVirtualDisplay]
 * takes exactly one Surface, bound for the display's lifetime, and the capture
 * display's is already spoken for by the ImageReader that feeds Go. A display
 * cannot fan out to two consumers.
 *
 * So recording always costs a second VirtualDisplay off the same projection.
 * What the two [Quality] modes actually choose is that display's *resolution*,
 * which is the part that matters for cost and for how the video looks:
 *
 *  - [Quality.CAPTURE] mirrors at the same size the pipeline samples. Cheap,
 *    and what you want if the point of the recording is to show what the sync
 *    engine was reacting to. It is also small — capture is capped at
 *    [ScreenCaptureService.CAP_LONG_EDGE] — so it looks like what it is.
 *  - [Quality.NATIVE] mirrors at the display's own resolution, for a demo
 *    video someone else will watch. Costs a second full-size encode.
 *
 * ## Failing soft is a requirement, not politeness
 *
 * A device that refuses the second display (some OEM projection stacks cap the
 * count, and encoder configuration can fail for a size the hardware dislikes)
 * must lose the recording and keep the sync. Screen sync is what the app is
 * for; recording is a convenience on top of it. Every failure path here logs,
 * releases whatever it managed to build, and returns an error string for the
 * UI — none of them throw into the caller or touch the capture display.
 */
class ScreenRecorder(private val ctx: Context) {

    enum class Quality { CAPTURE, NATIVE }

    private var recorder: MediaRecorder? = null
    private var display: VirtualDisplay? = null
    private var pfd: ParcelFileDescriptor? = null
    private var uri: Uri? = null
    private var file: File? = null
    private var name: String = ""

    @Volatile
    var isRecording: Boolean = false
        private set

    /** Human-readable location of the last completed recording, for the UI. */
    @Volatile
    var lastOutput: String = ""
        private set

    /**
     * Starts recording. Returns null on success, or a message describing why
     * not — never throws, so a caller can ignore the result and still be sure
     * screen capture is unaffected.
     */
    @Synchronized
    fun start(
        projection: MediaProjection,
        quality: Quality,
        captureW: Int,
        captureH: Int,
        dispW: Int,
        dispH: Int,
        densityDpi: Int,
    ): String? {
        if (isRecording) return "already recording"

        val (w, h) = when (quality) {
            Quality.CAPTURE -> encoderSize(captureW, captureH, Int.MAX_VALUE)
            Quality.NATIVE -> encoderSize(dispW, dispH, NATIVE_CAP_LONG_EDGE)
        }
        if (w < 64 || h < 64) return "capture too small to record ($w x $h)"

        name = "huemux-" + SimpleDateFormat("yyyyMMdd-HHmmss", Locale.US).format(Date()) + ".mp4"

        try {
            openOutput()
        } catch (e: Exception) {
            Log.e(TAG, "could not open output file", e)
            cleanup()
            return "could not create the video file: " + (e.message ?: e.toString())
        }

        val r = newRecorder()
        recorder = r
        try {
            // No audio source: recording the screen needs no permission beyond
            // the projection consent already given, and adding a microphone
            // would need RECORD_AUDIO and change what consenting means.
            r.setVideoSource(MediaRecorder.VideoSource.SURFACE)
            r.setOutputFormat(MediaRecorder.OutputFormat.MPEG_4)
            r.setVideoEncoder(MediaRecorder.VideoEncoder.H264)
            r.setVideoSize(w, h)
            r.setVideoFrameRate(FRAME_RATE)
            r.setVideoEncodingBitRate(bitrateFor(w, h))
            val out = pfd
            if (out != null) r.setOutputFile(out.fileDescriptor) else r.setOutputFile(file!!.absolutePath)
            r.prepare()
        } catch (e: Exception) {
            Log.e(TAG, "recorder setup failed for ${w}x$h", e)
            cleanup()
            return "this device would not encode ${w}x$h: " + (e.message ?: e.toString())
        }

        try {
            display = projection.createVirtualDisplay(
                "huemux-record",
                w, h, densityDpi,
                DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR,
                r.surface, null, null,
            )
        } catch (e: Exception) {
            Log.e(TAG, "second virtual display refused", e)
            cleanup()
            return "this device would not give a second screen mirror: " + (e.message ?: e.toString())
        }
        if (display == null) {
            cleanup()
            return "this device would not give a second screen mirror"
        }

        try {
            r.start()
        } catch (e: Exception) {
            Log.e(TAG, "recorder start failed", e)
            cleanup()
            return "recording would not start: " + (e.message ?: e.toString())
        }

        isRecording = true
        Log.i(TAG, "recording ${w}x$h -> $name")
        return null
    }

    /**
     * Stops and finalises the recording. Returns null on success, or a message.
     * Safe to call when not recording.
     */
    @Synchronized
    fun stop(): String? {
        if (!isRecording) return null
        isRecording = false

        var err: String? = null
        try {
            // A recorder stopped before it has written any frames throws, and
            // leaves a zero-length file that no player will open. Treat it as
            // a failure with an explanation rather than a crash: it is what a
            // user gets for tapping start and stop in quick succession.
            recorder?.stop()
        } catch (e: Exception) {
            Log.w(TAG, "recorder stop failed (too short?)", e)
            err = "the recording was too short to save"
        }

        // Release the display before the recorder: frames arriving at a
        // released encoder surface are a native-side crash on some devices.
        display?.release()
        display = null

        try {
            recorder?.reset()
            recorder?.release()
        } catch (e: Exception) {
            Log.w(TAG, "recorder release failed", e)
        }
        recorder = null

        publishOutput(err == null)
        lastOutput = if (err == null) name else ""
        return err
    }

    // --- output plumbing ---------------------------------------------------
    //
    // Two paths, decided by API level rather than preference. From Android 10
    // the gallery is MediaStore and an app cannot write to shared storage by
    // path at all; below it, MediaStore has no RELATIVE_PATH and writing to
    // the public Movies directory needs WRITE_EXTERNAL_STORAGE — a permission
    // this app should not be asking for to save a convenience video. So old
    // devices get an app-scoped file, which needs no permission and is still
    // reachable over USB and from a file manager.

    private fun openOutput() {
        if (Build.VERSION.SDK_INT >= 29) {
            val values = ContentValues().apply {
                put(MediaStore.Video.Media.DISPLAY_NAME, name)
                put(MediaStore.Video.Media.MIME_TYPE, "video/mp4")
                put(MediaStore.Video.Media.RELATIVE_PATH, Environment.DIRECTORY_MOVIES + "/HueMux")
                // Hides the entry from the gallery until it is a complete,
                // playable file — without this a half-written MP4 shows up as
                // a broken thumbnail the moment recording starts.
                put(MediaStore.Video.Media.IS_PENDING, 1)
            }
            val u = ctx.contentResolver.insert(MediaStore.Video.Media.EXTERNAL_CONTENT_URI, values)
                ?: throw IllegalStateException("MediaStore refused the entry")
            uri = u
            pfd = ctx.contentResolver.openFileDescriptor(u, "w")
                ?: throw IllegalStateException("MediaStore gave no file descriptor")
            return
        }
        val dir = ctx.getExternalFilesDir(Environment.DIRECTORY_MOVIES)
            ?: throw IllegalStateException("no external files directory")
        dir.mkdirs()
        file = File(dir, name)
    }

    private fun publishOutput(ok: Boolean) {
        try {
            pfd?.close()
        } catch (e: Exception) {
            Log.w(TAG, "closing output descriptor failed", e)
        }
        pfd = null

        val u = uri
        if (u != null) {
            if (ok) {
                val values = ContentValues().apply { put(MediaStore.Video.Media.IS_PENDING, 0) }
                try {
                    ctx.contentResolver.update(u, values, null, null)
                } catch (e: Exception) {
                    Log.w(TAG, "could not clear IS_PENDING", e)
                }
            } else {
                // A pending entry nobody clears stays invisible but keeps its
                // bytes forever. Delete the failed one.
                try {
                    ctx.contentResolver.delete(u, null, null)
                } catch (e: Exception) {
                    Log.w(TAG, "could not delete failed recording", e)
                }
            }
        } else if (!ok) {
            file?.delete()
        }
        uri = null
        file = null
    }

    /**
     * The no-argument constructor is deprecated from API 31 and the Context
     * one does not exist before it, so both spellings have to be present and
     * exactly one of them reachable at runtime.
     */
    @Suppress("DEPRECATION")
    private fun newRecorder(): MediaRecorder =
        if (Build.VERSION.SDK_INT >= 31) MediaRecorder(ctx) else MediaRecorder()

    /** Tears down whatever was built so far. Used only on a failed start. */
    private fun cleanup() {
        display?.release()
        display = null
        try {
            recorder?.reset()
            recorder?.release()
        } catch (e: Exception) {
            Log.w(TAG, "cleanup release failed", e)
        }
        recorder = null
        publishOutput(false)
        isRecording = false
    }

    private companion object {
        const val TAG = "HueMuxRecord"
        const val FRAME_RATE = 30

        /**
         * Native-quality recordings are capped here. A 1440p phone will encode
         * 1440p, but plenty of devices advertise an H.264 encoder that fails to
         * configure above 1080p, and a recording that refuses to start is worse
         * than one that is slightly smaller than the panel.
         */
        const val NATIVE_CAP_LONG_EDGE = 1920

        /**
         * Sizes the encoder is willing to accept. H.264 encoders on real
         * devices reject dimensions that are not multiples of 16 far more often
         * than the spec suggests they should, and the failure surfaces as an
         * opaque prepare() exception, so round rather than find out.
         */
        fun encoderSize(w: Int, h: Int, capLongEdge: Int): Pair<Int, Int> {
            var outW = w
            var outH = h
            val longEdge = maxOf(outW, outH)
            if (longEdge > capLongEdge) {
                val ratio = capLongEdge.toFloat() / longEdge
                outW = (outW * ratio).toInt()
                outH = (outH * ratio).toInt()
            }
            return Pair(align16(outW), align16(outH))
        }

        fun align16(v: Int) = (v / 16) * 16

        /**
         * Roughly 0.1 bits per pixel per frame, which is the usual rule of
         * thumb for screen content — flat UI compresses far better than camera
         * footage. Clamped so a tiny capture still gets a usable bitrate and a
         * native-resolution one does not ask for something no encoder will
         * agree to.
         */
        fun bitrateFor(w: Int, h: Int): Int =
            (w.toLong() * h.toLong() * FRAME_RATE / 10).toInt().coerceIn(500_000, 20_000_000)
    }
}
