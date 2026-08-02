package com.huemux.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.graphics.PixelFormat
import android.hardware.display.DisplayManager
import android.media.ImageReader
import android.media.projection.MediaProjection
import android.media.projection.MediaProjectionManager
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.util.Log
import com.huemux.mobile.Mobile
import java.nio.ByteBuffer

/**
 * Captures the screen and feeds it to the Go colour pipeline.
 *
 * A foreground service because Android requires one for MediaProjection, and
 * because the capture must survive the user leaving the app — the whole point
 * is syncing lights to whatever is on screen, which is rarely HueMux itself.
 *
 * The frames never travel through JavaScript. [Mobile.pushFrame] calls
 * straight into the Go engine in the same process, so a 320x180 frame at 30fps
 * costs a JNI call rather than a base64 round trip through a WebView bridge.
 */
class ScreenCaptureService : Service() {

    private var projection: MediaProjection? = null
    private var reader: ImageReader? = null
    private var display: android.hardware.display.VirtualDisplay? = null

    /**
     * One thread owns the capture pipeline: the ImageReader delivers frames on
     * it, and every teardown and rebuild is posted to it.
     *
     * This is not tidiness. Changing the capture resolution while capturing
     * used to crash the app, because the rebuild closed the ImageReader from
     * the caller's thread while a frame callback was running on another — a
     * use-after-free on the native buffer, which surfaces as a SIGSEGV with no
     * Kotlin stack to blame. Serialising both onto one thread makes the race
     * unrepresentable rather than unlikely.
     */
    private var pipelineThread: HandlerThread? = null
    private var pipeline: Handler? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (intent?.action == ACTION_RECONFIGURE) {
            // A capture-scale change while running. Rebuilding the display is
            // the only way to resize it — VirtualDisplay.resize() exists but
            // the ImageReader behind it is fixed at its construction size, so
            // resizing only the display gives scaled frames in a buffer that
            // no longer matches, which the row-stride copy reads as a sheared
            // image rather than an error.
            pipeline?.post { rebuildCapture() }
            return START_NOT_STICKY
        }

        // Order is load-bearing on Android 14+: getMediaProjection() throws if
        // the service is not already in the foreground. Doing this first is not
        // tidiness, it is the difference between working and a SecurityException.
        if (Build.VERSION.SDK_INT >= 29) {
            startForeground(
                NOTIFICATION_ID, buildNotification(),
                ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PROJECTION,
            )
        } else {
            startForeground(NOTIFICATION_ID, buildNotification())
        }

        val resultCode = intent?.getIntExtra(EXTRA_RESULT_CODE, 0) ?: 0
        val data: Intent? = if (Build.VERSION.SDK_INT >= 33) {
            intent?.getParcelableExtra(EXTRA_RESULT_DATA, Intent::class.java)
        } else {
            @Suppress("DEPRECATION")
            intent?.getParcelableExtra(EXTRA_RESULT_DATA)
        }
        if (data == null) {
            Log.e(TAG, "no projection consent data; stopping")
            stopSelf()
            return START_NOT_STICKY
        }

        val mgr = getSystemService(Context.MEDIA_PROJECTION_SERVICE) as MediaProjectionManager
        projection = mgr.getMediaProjection(resultCode, data).also { p ->
            // Android 14+ requires a registered callback before createVirtualDisplay.
            p.registerCallback(object : MediaProjection.Callback() {
                override fun onStop() {
                    // Fires when the user stops sharing from the system UI.
                    // Without telling the page, its Stop button stays enabled,
                    // the Start button stays disabled and the UI claims to be
                    // streaming long after the frames have stopped.
                    Log.i(TAG, "projection stopped by the system")
                    onCaptureEnded?.invoke()
                    stopSelf()
                }
            }, null)
        }

        instance = this
        val t = HandlerThread("huemux-capture").also { it.start() }
        pipelineThread = t
        pipeline = Handler(t.looper)
        pipeline?.post { startCapture() }
        return START_NOT_STICKY
    }

    /**
     * Rebuilds the capture display at the current [captureScale]. No-op when
     * capture is not running, so a scale change made before starting is simply
     * picked up by the next [startCapture].
     *
     * Runs on [pipeline] only — see that field's comment for why.
     */
    private fun rebuildCapture() {
        if (projection == null || display == null) return
        Mobile.logHost("capture: rebuilding at scale=$captureScale")
        display?.release()
        reader?.close()
        display = null
        reader = null
        startCapture()
    }

    /**
     * Starts recording, if capture is running. Returns null on success or a
     * message for the UI.
     *
     * There is one implementation. Recording encodes the frames already
     * flowing to the colour engine ([FrameRecorder]), so it needs nothing from
     * the system that screen sync has not already been granted.
     *
     * A second mode used to exist, which mirrored the display again at its
     * native resolution for a sharper video. It is gone because it cannot
     * work: from Android 14 a MediaProjection permits exactly one
     * VirtualDisplay, and asking for a second does not fail — it ends the
     * projection. The device log showed the whole capture session dying 365ms
     * after the request, with no error anywhere, which is precisely what that
     * rule produces. Recording at full resolution is done by raising the
     * capture resolution instead, which reaches the same encoder by a route
     * the platform allows.
     */
    @Synchronized
    fun startRecording(): String? {
        if (projection == null) return "screen capture is not running"
        if (frameRecorder?.isRecording == true) return "already recording"
        if (capturedW <= 0 || capturedH <= 0) return "screen capture is not running"
        Mobile.logHost("record: start requested capture=${capturedW}x$capturedH")
        val rec = frameRecorder ?: FrameRecorder(applicationContext).also { frameRecorder = it }
        return rec.start(capturedW, capturedH)
    }

    @Synchronized
    fun stopRecording(): String? = frameRecorder?.stop()

    fun isRecording(): Boolean = frameRecorder?.isRecording == true

    fun lastRecordingName(): String = frameRecorder?.lastOutput ?: ""

    /** Where the last recording went, in words, for the UI to show verbatim. */
    fun lastRecordingLocation(): String = frameRecorder?.lastLocation ?: ""

    /** The last recording's URI, for the share sheet. */
    fun lastRecordingUri(): android.net.Uri? = frameRecorder?.lastUri

    /**
     * A one-line summary for the diagnostics report. Everything about capture
     * and recording lives in this process's Kotlin half, which the Go
     * diagnostics cannot see — so a broken recording produced a report with no
     * mention of recording in it at all.
     */
    fun diagnosticsBlock(): String {
        val sb = StringBuilder()
        sb.append("capture               ")
        sb.append(if (display != null) "running ${capturedW}x$capturedH scale=$captureScale" else "stopped")
        sb.append('\n')
        val m = resources.displayMetrics
        sb.append("display               ${m.widthPixels}x${m.heightPixels} @${m.densityDpi}dpi\n")
        sb.append("colour pipeline       ${pipelineW}x$pipelineH (capped at $PIPELINE_LONG_EDGE)\n")
        sb.append("recording             ")
        sb.append(
            when {
                frameRecorder?.isRecording == true -> "recording · " + (frameRecorder?.stats() ?: "")
                else -> "stopped"
            }
        )
        sb.append('\n')
        val last = lastRecordingLocation()
        if (last.isNotEmpty()) sb.append("last recording        $last\n")
        frameRecorder?.let { sb.append("frame encoder         ${it.stats()}\n") }
        return sb.toString()
    }

    private fun startCapture() {
        val p = projection ?: return

        // Match the display's aspect ratio and orientation. A fixed 320x180
        // landscape buffer on a portrait phone squashed the whole screen into
        // a letterbox, so the colour pipeline sampled a distorted image with
        // huge black bands — the zones nearest the edges read as black
        // regardless of what was actually on screen.
        val metrics = resources.displayMetrics
        val dispW = metrics.widthPixels.coerceAtLeast(1)
        val dispH = metrics.heightPixels.coerceAtLeast(1)
        val scale = captureScale.coerceIn(0.05f, 1.0f)

        // Long edge capped at CAP_LONG_EDGE so a scale of 1.0 on a 1440p
        // phone does not push a full-resolution buffer through the pipeline;
        // the Go side reduces to a 64x36 grid regardless.
        val longEdge = (maxOf(dispW, dispH) * scale).toInt().coerceIn(64, CAP_LONG_EDGE)
        val ratio = longEdge.toFloat() / maxOf(dispW, dispH)
        // Even dimensions: some encoders and the RGBA row stride behave badly
        // on odd widths.
        val w = ((dispW * ratio).toInt().coerceAtLeast(32)) and 1.inv()
        val h = ((dispH * ratio).toInt().coerceAtLeast(32)) and 1.inv()
        capturedW = w
        capturedH = h
        Log.i(TAG, "display ${dispW}x$dispH scale=$scale -> capture ${w}x$h")
        Mobile.logHost("capture: display ${dispW}x$dispH scale=$scale -> ${w}x$h")

        reader = ImageReader.newInstance(w, h, PixelFormat.RGBA_8888, 2).apply {
            setOnImageAvailableListener({ r ->
                val image = r.acquireLatestImage() ?: return@setOnImageAvailableListener
                try {
                    pushImage(image)
                } catch (e: Exception) {
                    Log.w(TAG, "frame dropped", e)
                } finally {
                    // Not optional: an unclosed Image exhausts the reader's
                    // buffers within a couple of frames and capture silently
                    // stops with no error anywhere.
                    image.close()
                }
            }, pipeline)
        }

        display = p.createVirtualDisplay(
            "huemux-capture",
            w, h, resources.displayMetrics.densityDpi,
            DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR,
            reader!!.surface, null, null,
        )
    }

    /**
     * Splits one captured frame between the two consumers that want it.
     *
     * ## Why this is not one buffer any more
     *
     * The colour engine reduces every frame to a 64x36 grid. It does not care
     * how big the frame was. The recorder cares a great deal. Those are
     * opposite requirements, and this used to serve both from a single
     * full-resolution RGB copy, which meant the expensive path ran whether or
     * not anything needed it:
     *
     *   - a per-pixel copy of the whole frame — at 942x1920 that is 1.8M
     *     iterations doing three bounds-checked ByteBuffer.get(int) calls
     *     each, 5.4M native reads per frame, for data that was about to be
     *     averaged down to 2304 cells;
     *   - a 5.4MB array across the JNI boundary, copied again on the Go side;
     *   - then the recorder converting that same 5.4MB to YUV.
     *
     * Raising the capture resolution for a recording therefore slowed the
     * lights down, which is the wrong way round: the resolution exists for the
     * video, and the colour pipeline should be indifferent to it.
     *
     * Now the frame is read once per consumer, in bulk rows, and each gets
     * what it actually needs:
     *
     *   colour engine   downsampled to [PIPELINE_LONG_EDGE] with 2x2
     *                   averaging — still ~7x oversampled per grid cell
     *   recorder        the full-resolution image, converted straight to YUV
     *                   with no RGB intermediate
     *
     * Bulk row reads are the other half. `buf.get(byte[], off, len)` is a
     * memcpy; `buf.get(int)` in a loop is not, and that difference dominated
     * everything else here.
     */
    private fun pushImage(image: android.media.Image) {
        val plane = image.planes[0]
        val buf: ByteBuffer = plane.buffer
        val pixelStride = plane.pixelStride
        val rowStride = plane.rowStride
        val w = image.width
        val h = image.height

        // The display produces frames as fast as it composites, ~60/s. The
        // bridge is fed at 20Hz and the encoder is configured for 30. Encoding
        // and averaging frames that nothing will consume is pure heat.
        val now = android.os.SystemClock.elapsedRealtime()
        val rec = frameRecorder
        val wantRecord = rec != null && rec.isRecording && now - lastRecordMs >= MIN_FRAME_INTERVAL_MS
        val wantPipeline = now - lastPipelineMs >= MIN_FRAME_INTERVAL_MS
        if (!wantPipeline && !wantRecord) return

        val rowBytes = w * pixelStride
        var row = rowScratch
        if (row == null || row.size < rowBytes) {
            row = ByteArray(rowBytes)
            rowScratch = row
        }

        if (wantPipeline) {
            lastPipelineMs = now
            pushDownscaled(buf, row, rowStride, pixelStride, w, h)
        }
        if (wantRecord) {
            lastRecordMs = now
            rec!!.onFrame(buf, row, rowStride, pixelStride, w, h)
        }
    }

    /**
     * Averages the frame down to at most [PIPELINE_LONG_EDGE] on its long edge
     * and hands that to Go.
     *
     * 2x2 averaging rather than nearest sampling. Nearest is cheaper and looks
     * identical on flat content, but on text and fine patterns it aliases, and
     * since the engine's zones average whatever they are given, aliasing
     * arrives as colours that shimmer while the screen is still.
     */
    private fun pushDownscaled(
        buf: ByteBuffer, row: ByteArray, rowStride: Int, pixelStride: Int, w: Int, h: Int,
    ) {
        val step = pipelineStep(w, h)
        val sw = w / step
        val sh = h / step
        if (sw < 2 || sh < 2) return

        val need = sw * sh * 3
        var out = pipelineScratch
        if (out == null || out.size != need) {
            out = ByteArray(need)
            pipelineScratch = out
        }

        // A second row buffer only when there is a second row to average.
        var row2 = if (step > 1) {
            var r = rowScratch2
            if (r == null || r.size < w * pixelStride) {
                r = ByteArray(w * pixelStride)
                rowScratch2 = r
            }
            r
        } else null

        var o = 0
        for (y in 0 until sh) {
            val srcY = y * step
            buf.position(srcY * rowStride)
            buf.get(row, 0, w * pixelStride)
            // The bottom edge may have no row beneath it. row2 still holds
            // the previous iteration's pixels, so averaging against it there
            // would smear one row's colours into the next — drop to single-row
            // sampling for that row instead.
            var second = row2
            if (second != null) {
                if (srcY + 1 < h) {
                    buf.position((srcY + 1) * rowStride)
                    buf.get(second, 0, w * pixelStride)
                } else {
                    second = null
                }
            }
            for (x in 0 until sw) {
                val i = x * step * pixelStride
                val j = if (i + pixelStride < w * pixelStride) i + pixelStride else i
                if (second != null) {
                    out[o++] = avg4(row[i], row[j], second[i], second[j])
                    out[o++] = avg4(row[i + 1], row[j + 1], second[i + 1], second[j + 1])
                    out[o++] = avg4(row[i + 2], row[j + 2], second[i + 2], second[j + 2])
                } else {
                    out[o++] = row[i]
                    out[o++] = row[i + 1]
                    out[o++] = row[i + 2]
                }
            }
        }
        pipelineW = sw
        pipelineH = sh
        Mobile.pushFrame(sw.toLong(), sh.toLong(), out)
    }

    private fun buildNotification(): Notification {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= 26) {
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, getString(R.string.capture_channel),
                    NotificationManager.IMPORTANCE_LOW)
            )
        }
        val builder = if (Build.VERSION.SDK_INT >= 26) {
            Notification.Builder(this, CHANNEL_ID)
        } else {
            @Suppress("DEPRECATION") Notification.Builder(this)
        }
        return builder
            .setContentTitle(getString(R.string.capture_title))
            .setContentText(getString(R.string.capture_text))
            .setSmallIcon(android.R.drawable.ic_menu_view)
            .setOngoing(true)
            .build()
    }

    override fun onDestroy() {
        // Covers every other way this ends — stopSelf from the page, the
        // system reclaiming the service, the activity going away.
        onCaptureEnded?.invoke()
        // Before the projection goes: stopping the recorder finalises the MP4
        // and clears its MediaStore pending flag. Skipping it on this path
        // would leave every recording that ended by the user swiping the app
        // away invisible in the gallery and undeletable from it.
        try {
            if (frameRecorder?.isRecording == true) frameRecorder?.stop()
        } catch (e: Exception) {
            Log.w(TAG, "recorder stop during shutdown", e)
        }
        frameRecorder = null

        // Tear the pipeline down on its own thread, for the same reason the
        // rebuild runs there: a frame callback may be in flight right now, and
        // closing the reader underneath it is a native crash. quitSafely lets
        // the posted teardown run before the looper stops.
        val t = pipelineThread
        pipeline?.post {
            display?.release()
            reader?.close()
            display = null; reader = null
        }
        t?.quitSafely()
        try {
            t?.join(1000)
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
        }
        pipeline = null
        pipelineThread = null

        projection?.stop()
        projection = null
        if (instance === this) instance = null
        Mobile.logHost("capture: stopped")
        Log.i(TAG, "capture stopped")
        super.onDestroy()
    }

    /** One captured row, reused. Bulk reads land here instead of per-pixel gets. */
    private var rowScratch: ByteArray? = null

    /** The row below it, for the colour pipeline's 2x2 averaging. */
    private var rowScratch2: ByteArray? = null
    private var pipelineScratch: ByteArray? = null
    private var lastPipelineMs = 0L
    private var lastRecordMs = 0L
    private var frameRecorder: FrameRecorder? = null

    companion object {
        /**
         * Invoked whenever capture stops, for any reason. Set by MainActivity
         * so the web UI can put its buttons back — the page has no other way
         * to learn that the system ended the session.
         */
        @Volatile
        var onCaptureEnded: (() -> Unit)? = null

        /**
         * The running service, or null. The web UI asks synchronous questions
         * ("are you recording?", "what size are you capturing?") and expects an
         * answer in the same call, which an Intent cannot give. Only ever
         * touched from the main thread and the WebView's bridge thread, and
         * every method it exposes is @Synchronized.
         */
        @Volatile
        var instance: ScreenCaptureService? = null
            private set

        const val TAG = "HueMuxCapture"

        /**
         * Longest edge the colour engine is fed. It reduces to a 64x36 grid,
         * so 480 leaves roughly seven samples per grid cell in each axis —
         * more than enough to average stably, and a fraction of the work of
         * handing it a 1920-pixel frame it will throw away.
         */
        const val PIPELINE_LONG_EDGE = 480

        /**
         * Floor on the gap between frames handed to either consumer, ~30/s.
         * The display composites at ~60, the bridge is fed at 20Hz and the
         * encoder is configured for 30, so the frames above this rate are
         * work nothing consumes.
         */
        const val MIN_FRAME_INTERVAL_MS = 33L

        /** Integer downscale factor that keeps the long edge within budget. */
        fun pipelineStep(w: Int, h: Int): Int {
            val longEdge = maxOf(w, h)
            if (longEdge <= PIPELINE_LONG_EDGE) return 1
            return (longEdge + PIPELINE_LONG_EDGE - 1) / PIPELINE_LONG_EDGE
        }

        /** Mean of four sample bytes, kept unsigned. */
        fun avg4(a: Byte, b: Byte, c: Byte, d: Byte): Byte {
            val sum = (a.toInt() and 0xff) + (b.toInt() and 0xff) +
                (c.toInt() and 0xff) + (d.toInt() and 0xff)
            return (sum shr 2).toByte()
        }

        /** Size actually handed to the colour engine, for diagnostics. */
        @Volatile
        var pipelineW: Int = 0

        @Volatile
        var pipelineH: Int = 0
        const val ACTION_STOP = "com.huemux.app.STOP_CAPTURE"
        const val ACTION_RECONFIGURE = "com.huemux.app.RECONFIGURE_CAPTURE"
        const val EXTRA_RESULT_CODE = "resultCode"
        const val EXTRA_RESULT_DATA = "resultData"
        private const val CHANNEL_ID = "huemux-capture"
        private const val NOTIFICATION_ID = 1

        /**
         * Hard ceiling on the captured long edge.
         *
         * This used to be 480 — the point past which the colour pipeline, which
         * reduces everything to a 64x36 grid, gains nothing. That was right
         * while the size was fixed, and wrong the moment [captureScale] became
         * a control: it silently clamped the top two thirds of the slider to
         * the same result, so the setting looked broken by being obeyed. The
         * cap now exists only to stop a request no encoder would honour, and
         * the default scale is what keeps the ordinary case cheap.
         */
        const val CAP_LONG_EDGE = 1920

        /**
         * Fraction of the display resolution to capture, 0.05..1.0. Settable
         * from the UI: higher is sharper input for the colour pipeline (and a
         * better screen recording), lower is cheaper.
         *
         * 0.2 lands a typical 1080x2400 phone at a 480-pixel long edge, which
         * is what every build up to now captured at, so the default costs
         * exactly what it did before the cap was raised.
         */
        @Volatile
        var captureScale: Float = 0.2f

        /** Last negotiated capture size, for the settings UI to display. */
        @Volatile
        var capturedW: Int = 0

        @Volatile
        var capturedH: Int = 0

        fun startForegroundService(ctx: Context, resultCode: Int, data: Intent) {
            val i = Intent(ctx, ScreenCaptureService::class.java)
                .putExtra(EXTRA_RESULT_CODE, resultCode)
                .putExtra(EXTRA_RESULT_DATA, data)
            ctx.startForegroundService(i)
        }

        fun stop(ctx: Context) {
            ctx.startService(Intent(ctx, ScreenCaptureService::class.java).setAction(ACTION_STOP))
        }

        /** Applies a new [captureScale] to a capture already in progress. */
        fun requestReconfigure(ctx: Context) {
            if (instance == null) return
            ctx.startService(Intent(ctx, ScreenCaptureService::class.java).setAction(ACTION_RECONFIGURE))
        }
    }
}
