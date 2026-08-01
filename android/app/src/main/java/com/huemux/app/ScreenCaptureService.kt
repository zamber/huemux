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

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopSelf()
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

        startCapture()
        return START_NOT_STICKY
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
            }, null)
        }

        display = p.createVirtualDisplay(
            "huemux-capture",
            w, h, resources.displayMetrics.densityDpi,
            DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR,
            reader!!.surface, null, null,
        )
    }

    /** Packs one RGBA image into tightly-packed RGB and hands it to Go. */
    private fun pushImage(image: android.media.Image) {
        val plane = image.planes[0]
        val buf: ByteBuffer = plane.buffer
        val pixelStride = plane.pixelStride
        val rowStride = plane.rowStride
        val w = image.width
        val h = image.height

        // rowStride usually exceeds width*4: the surface is padded to a
        // hardware-friendly alignment. Copying row by row rather than assuming
        // a packed buffer is what keeps the image from shearing diagonally.
        val need = w * h * 3
        var out = rgbScratch
        if (out == null || out.size != need) {
            out = ByteArray(need)
            rgbScratch = out
        }
        var o = 0
        for (y in 0 until h) {
            var rowStart = y * rowStride
            for (x in 0 until w) {
                val i = rowStart + x * pixelStride
                out[o++] = buf.get(i)
                out[o++] = buf.get(i + 1)
                out[o++] = buf.get(i + 2)
            }
        }
        Mobile.pushFrame(w.toLong(), h.toLong(), out)
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
        display?.release()
        reader?.close()
        projection?.stop()
        display = null; reader = null; projection = null; rgbScratch = null
        Log.i(TAG, "capture stopped")
        super.onDestroy()
    }

    private var rgbScratch: ByteArray? = null

    companion object {
        /**
         * Invoked whenever capture stops, for any reason. Set by MainActivity
         * so the web UI can put its buttons back — the page has no other way
         * to learn that the system ended the session.
         */
        @Volatile
        var onCaptureEnded: (() -> Unit)? = null

        const val TAG = "HueMuxCapture"
        const val ACTION_STOP = "com.huemux.app.STOP_CAPTURE"
        const val EXTRA_RESULT_CODE = "resultCode"
        const val EXTRA_RESULT_DATA = "resultData"
        private const val CHANNEL_ID = "huemux-capture"
        private const val NOTIFICATION_ID = 1

        // Upper bound on the captured long edge. The Go pipeline reduces to a
        // 64x36 grid regardless, so anything beyond this is bandwidth and
        // battery for no visible gain.
        const val CAP_LONG_EDGE = 480

        /**
         * Fraction of the display resolution to capture, 0.05..1.0. Settable
         * from the UI: higher is sharper input for the colour pipeline (and
         * usable as a screen recording), lower is cheaper.
         */
        @Volatile
        var captureScale: Float = 0.25f

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
    }
}
