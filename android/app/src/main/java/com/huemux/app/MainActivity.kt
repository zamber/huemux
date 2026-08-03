package com.huemux.app

import android.Manifest
import android.annotation.SuppressLint
import android.content.ContentValues
import android.content.Intent
import android.content.pm.PackageManager
import android.media.projection.MediaProjectionManager
import android.net.Uri
import android.net.wifi.WifiManager
import android.os.Build
import android.provider.MediaStore
import android.os.Bundle
import android.util.Log
import android.view.ViewGroup
import android.webkit.JavascriptInterface
import android.webkit.PermissionRequest
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.TextView
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import com.huemux.mobile.Mobile
import org.json.JSONObject

/**
 * Hosts the HueMux UI.
 *
 * The whole application is Go: [Mobile.start] boots the same HTTP + WebSocket
 * server the desktop build runs and returns a loopback URL, and this activity
 * is little more than a WebView pointed at it. That is deliberately the same
 * shape as `cmd/huemux-desktop`, which wraps the identical server in an
 * Electron window — nothing about pairing, the Hue protocol, or the UI is
 * reimplemented here.
 *
 * Because the server really is on 127.0.0.1, the origin checks in
 * internal/server/ws.go pass unmodified. No relaxation of the loopback
 * security model was needed to make Android work.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var webView: WebView
    private var multicastLock: WifiManager.MulticastLock? = null
    // The host the server is running on — captured from the initial URL so
    // in-app navigation stays in the WebView regardless of whether the server
    // is on loopback, a LAN address, or a Tailscale IP.
    private var appHost: String? = null

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        webView = WebView(this).apply {
            layoutParams = ViewGroup.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            )
            settings.javaScriptEnabled = true
            // Required, not optional: theme and language preferences live in
            // localStorage (web/shared/theme.js, i18n.js). Without this the UI
            // silently forgets both on every launch.
            settings.domStorageEnabled = true
            // HueMuxNative is injected into every page this WebView renders,
            // so no page outside the app's own origin may ever be rendered
            // here: its JavaScript would have full access to screen capture,
            // the Downloads folder, and the share sheet. Navigation to any
            // other host leaves the WebView for the system browser, which has
            // no such bridge.
            //
            // The app's own host is captured from the initial server URL
            // (startServer below) rather than hardcoded to 127.0.0.1 — the
            // server may be on a LAN or Tailscale address when connecting to
            // a remote instance.
            webViewClient = object : WebViewClient() {
                override fun shouldOverrideUrlLoading(
                    view: WebView,
                    request: WebResourceRequest,
                ): Boolean {
                    val host = request.url.host ?: ""
                    if (host == appHost || host == "127.0.0.1" || host == "localhost") {
                        return false
                    }
                    view.context.startActivity(Intent(Intent.ACTION_VIEW, request.url))
                    return true
                }
            }

            // Long press does nothing in this app — every control acts on
            // release — but the WebView still runs the platform gesture:
            // a haptic tick, then text selection with its context menu, which
            // left buttons and sliders sitting in a stuck half-selected state
            // if a finger rested on them. CSS suppresses the tap highlight and
            // the selection (see the touch block in shared/theme.css); only
            // this reaches the haptic feedback and the context menu.
            //
            // Returning true consumes the event rather than disabling the
            // listener, so nothing downstream treats it as unhandled.
            isLongClickable = false
            setOnLongClickListener { true }
            isHapticFeedbackEnabled = false

            // A WebView ignores downloads unless told what to do with them, so
            // the settings page's "Download diagnostics" button would silently
            // do nothing. Hand the URL to the system, which offers the share
            // and save options the user needs to actually send the file on.
            // This is the only way logs leave the phone — there is no adb, no
            // command line, and no reachable filesystem.
            setDownloadListener { url, _, _, _, _ ->
                try {
                    startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
                } catch (e: Exception) {
                    Log.e(TAG, "could not open download: $url", e)
                }
            }

            // Music reactivity captures the microphone through getUserMedia
            // (web/music.js). A WebView refuses every such request unless a
            // WebChromeClient grants it — which is the whole reason the mic
            // returned NotAllowedError. The app's own page only ever asks
            // for audio capture; anything else is refused outright.
            webChromeClient = object : WebChromeClient() {
                override fun onPermissionRequest(request: PermissionRequest) {
                    val wantsMic = request.resources.any { it == PermissionRequest.RESOURCE_AUDIO_CAPTURE }
                    if (!wantsMic) {
                        request.deny()
                        return
                    }
                    if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) ==
                        PackageManager.PERMISSION_GRANTED
                    ) {
                        request.grant(request.resources)
                        return
                    }
                    // RECORD_AUDIO is a dangerous permission: the OS asks the
                    // user for it at runtime, and the WebView request has to
                    // wait for that answer before it can be granted.
                    pendingMicRequest = request
                    micPermissionLauncher.launch(Manifest.permission.RECORD_AUDIO)
                }
            }
        }
        setContentView(webView)

        // targetSdk 35 makes Android 15+ draw the app edge-to-edge, so without
        // this the WebView extends under the status bar: the page header sits
        // beneath the clock and battery icons, unreadable and — because the
        // system consumes the touches — unclickable. That included the
        // Settings link, which is the only route to the diagnostics report,
        // so the app could not even be debugged from itself.
        //
        // Padding the WebView rather than the web content: it works regardless
        // of what the page's CSS knows about safe areas, and it keeps
        // env(safe-area-inset-*) at zero inside the WebView so a page that
        // does handle insets cannot double-pad.
        ViewCompat.setOnApplyWindowInsetsListener(webView) { v, insets ->
            val bars = insets.getInsets(
                WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout()
            )
            v.setPadding(bars.left, bars.top, bars.right, bars.bottom)
            insets
        }

        // Keep in-app back navigation working before it falls through to
        // finishing the activity.
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (webView.canGoBack()) webView.goBack() else finish()
            }
        })

        // The page detects this object's presence to decide that native
        // capture is available — a capability check, not a guess about the
        // environment. See startCapture() in web/app.js.
        webView.addJavascriptInterface(NativeBridge(), "HueMuxNative")

        // Capture can end without the page asking: the user taps "Stop
        // sharing" in the system UI, or Android reclaims the service. Tell the
        // page so it can reset its controls, and stop the DTLS stream so the
        // bridge releases the entertainment area immediately instead of
        // waiting out its keepalive timeout.
        ScreenCaptureService.onCaptureEnded = {
            try {
                Mobile.stopSync()
            } catch (e: Exception) {
                Log.w(TAG, "stopSync after capture ended", e)
            }
            runOnUiThread {
                webView.evaluateJavascript(
                    "window.__huemuxCaptureEnded && window.__huemuxCaptureEnded()", null,
                )
            }
        }

        // The capture scale lives in a @Volatile companion field on the
        // service, which is process state and resets to its default every cold
        // start. Restore the user's choice here, before anything can start a
        // capture with the wrong one.
        ScreenCaptureService.captureScale =
            prefs().getFloat(PREF_SCALE, ScreenCaptureService.captureScale).coerceIn(0.05f, 1.0f)

        acquireMulticastLock()
        startServer()
        publishHostInfo()
    }

    private fun startServer() {
        try {
            // filesDir is the app-private directory Android guarantees is
            // writable; os.UserConfigDir() means nothing here, which is why
            // internal/config grew SetDir.
            // Before start, so the About screen and any diagnostics report
            // name the build the user actually installed rather than the
            // literal string "android".
            Mobile.setVersion(BuildConfig.VERSION_NAME)
            val url = Mobile.start(filesDir.absolutePath)
            Log.i(TAG, "huemux server started at $url")
            appHost = Uri.parse(url).host
            webView.loadUrl(url)
        } catch (e: Exception) {
            // A dead server means a blank WebView and no clue why, so say so
            // on screen rather than leaving a white rectangle.
            Log.e(TAG, "failed to start huemux", e)
            setContentView(
                TextView(this).apply {
                    text = getString(R.string.start_failed, e.message ?: e.toString())
                    setPadding(48, 96, 48, 48)
                }
            )
        }
    }

    /**
     * Bridge discovery uses multicast, which Android drops by default to save
     * power. Without this lock, mDNS/SSDP finds nothing and pairing silently
     * falls back to "no bridge found" — with manual IP entry as the only way
     * through. Some OEM ROMs filter multicast regardless, hence best-effort.
     */
    private fun acquireMulticastLock() {
        try {
            val wifi = applicationContext.getSystemService(WIFI_SERVICE) as WifiManager
            multicastLock = wifi.createMulticastLock("huemux-discovery").apply {
                setReferenceCounted(true)
                acquire()
            }
        } catch (e: Exception) {
            Log.w(TAG, "could not acquire multicast lock; discovery may fail", e)
        }
    }

    override fun onDestroy() {
        // isFinishing distinguishes a real exit from a configuration change.
        // Tearing the server down on a rotation would drop the WebSocket and
        // blank the UI for no reason.
        if (isFinishing) {
            ScreenCaptureService.onCaptureEnded = null
            ScreenCaptureService.stop(this)
            multicastLock?.let { if (it.isHeld) it.release() }
            Mobile.stop()
        }
        super.onDestroy()
    }


    // --- native screen capture bridge ------------------------------------
    //
    // No mobile browser implements getDisplayMedia, so the web UI cannot
    // capture anything on its own. This exposes MediaProjection to the page.
    //
    // The consent dialog is asynchronous and the user can dismiss it, so
    // startCapture returns a JS promise rather than a bare boolean: the page
    // needs to distinguish "running" from "declined" to reset its buttons.

    private var pendingCaptureCallback: String? = null

    // The WebView's mic request parked while the OS runtime dialog is up;
    // granted or denied once the user answers (see onPermissionRequest).
    private var pendingMicRequest: PermissionRequest? = null

    private val micPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { granted ->
        val request = pendingMicRequest
        pendingMicRequest = null
        if (request == null) return@registerForActivityResult
        if (granted) {
            request.grant(request.resources)
        } else {
            // Denied — tell the WebView so the page surfaces a real error
            // instead of hanging on "Checking…".
            request.deny()
        }
    }

    private val projectionLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        val cb = pendingCaptureCallback
        pendingCaptureCallback = null
        if (result.resultCode == RESULT_OK && result.data != null) {
            ScreenCaptureService.startForegroundService(this, result.resultCode, result.data!!)
            resolveCapture(cb, true, "")
        } else {
            // Dismissing the system dialog is a normal outcome, not an error
            // worth logging as one — but the page must hear about it or its
            // Start button stays disabled forever.
            resolveCapture(cb, false, "capture permission denied")
        }
    }

    private fun resolveCapture(cbId: String?, ok: Boolean, err: String) {
        if (cbId == null) return
        val js = "window.__huemuxCaptureResult && window.__huemuxCaptureResult(" +
            "'" + cbId + "', " + ok + ", " + org.json.JSONObject.quote(err) + ")"
        runOnUiThread { webView.evaluateJavascript(js, null) }
    }

    inner class NativeBridge {
        /**
         * Asks for screen-capture consent and starts the service.
         * Resolution is reported back through window.__huemuxCaptureResult.
         */
        @JavascriptInterface
        fun startCapture(areaId: String, callbackId: String) {
            runOnUiThread {
                try {
                    Mobile.startSync(areaId)
                } catch (e: Exception) {
                    // Selecting the area is what opens the DTLS stream. If that
                    // fails there is nothing to capture *for*, so do not put a
                    // consent dialog in front of the user first.
                    Log.e(TAG, "startSync failed", e)
                    resolveCapture(callbackId, false, e.message ?: "could not select area")
                    return@runOnUiThread
                }
                pendingCaptureCallback = callbackId
                val mgr = getSystemService(MEDIA_PROJECTION_SERVICE) as MediaProjectionManager
                projectionLauncher.launch(mgr.createScreenCaptureIntent())
            }
        }

        @JavascriptInterface
        fun stopCapture() {
            runOnUiThread {
                ScreenCaptureService.stop(this@MainActivity)
                try {
                    Mobile.stopSync()
                } catch (e: Exception) {
                    Log.w(TAG, "stopSync failed", e)
                }
            }
        }

        // --- capture quality and recording -------------------------------
        //
        // These return JSON strings rather than posting results back through
        // window.__huemux* callbacks, because unlike startCapture they are all
        // answerable immediately: no consent dialog, no activity result. The
        // page treats each one as a synchronous call.

        /**
         * Everything the page needs to render the capture/recording controls:
         * the requested scale, the size that scale actually produced, the
         * display's own size, and whether a recording is running.
         */
        @JavascriptInterface
        fun captureState(): String {
            val svc = ScreenCaptureService.instance
            val m = resources.displayMetrics
            // Refresh the diagnostics block on the way past. The page polls
            // this on every status push, which makes it the cheapest place to
            // keep the report current without a timer of its own.
            publishHostInfo()
            return JSONObject().apply {
                put("scale", ScreenCaptureService.captureScale.toDouble())
                put("capturing", svc != null)
                put("captureW", ScreenCaptureService.capturedW)
                put("captureH", ScreenCaptureService.capturedH)
                put("displayW", m.widthPixels)
                put("displayH", m.heightPixels)
                put("capLongEdge", ScreenCaptureService.CAP_LONG_EDGE)
                put("recording", svc?.isRecording() == true)
                // The resolution control is locked while recording: changing
                // it rebuilds the capture display, and the encoder is
                // configured for the size it had when it started. Allowing it
                // would corrupt the rest of the recording.
                put("scaleLocked", svc?.isRecording() == true)
                put("lastRecording", svc?.lastRecordingName() ?: "")
                // The full location, not just the filename: a recording the
                // user cannot find has not really been saved.
                put("lastLocation", svc?.lastRecordingLocation() ?: "")
                put("canShare", (svc?.lastRecordingUri() != null) || lastSavedUri != null)
            }.toString()
        }

        /**
         * Sets the fraction of the display resolution to capture. Applied to a
         * running capture immediately — waiting for the next start would make
         * the control look broken while you drag it.
         */
        @JavascriptInterface
        fun setCaptureScale(scale: Double) {
            // Refused rather than queued while recording: see scaleLocked.
            if (ScreenCaptureService.instance?.isRecording() == true) return
            val v = scale.toFloat().coerceIn(0.05f, 1.0f)
            ScreenCaptureService.captureScale = v
            prefs().edit().putFloat(PREF_SCALE, v).apply()
            ScreenCaptureService.requestReconfigure(this@MainActivity)
        }

        @JavascriptInterface
        fun startRecording(): String {
            val svc = ScreenCaptureService.instance
                ?: return result(false, "start screen sync first — recording encodes the same capture")
            val err = svc.startRecording()
            return result(err == null, err ?: "")
        }

        /**
         * Writes [text] to the device's Downloads folder and returns the
         * location, as JSON.
         *
         * The web page cannot save a file on its own here. A WebView ignores a
         * download unless a DownloadListener handles it, and the listener does
         * not fire for a navigation started inside an iframe — which is how
         * the diagnostics button broke: it was moved into a throwaway iframe
         * to stop a failed download replacing the Settings page, and that
         * silently removed the only mechanism that made it work.
         *
         * Doing the write in Kotlin removes the listener from the path
         * completely. The page fetches its own text over loopback, which
         * always works, and hands it here.
         */
        @JavascriptInterface
        fun saveTextFile(name: String, text: String): String {
            return try {
                val where = writeToDownloads(name, text)
                lastSavedUri = where.second
                result(true, "", where.first)
            } catch (e: Exception) {
                Log.e(TAG, "saving $name", e)
                Mobile.logHost("save: $name failed: ${e.message}")
                result(false, e.message ?: e.toString())
            }
        }

        /**
         * Opens the system share sheet for the last file this app saved or
         * recorded. Without it, a file in Downloads or Movies is findable only
         * by knowing where to look — which is the state that made a recording
         * feel like it had gone nowhere.
         */
        @JavascriptInterface
        fun shareLastFile(): String {
            val uri = lastSavedUri ?: ScreenCaptureService.instance?.lastRecordingUri()
            if (uri == null) return result(false, "nothing has been saved yet")
            return try {
                val mime = if (uri.toString().endsWith(".mp4")) "video/mp4" else "text/plain"
                val send = Intent(Intent.ACTION_SEND).apply {
                    type = mime
                    putExtra(Intent.EXTRA_STREAM, uri)
                    addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                }
                runOnUiThread {
                    startActivity(Intent.createChooser(send, getString(R.string.share_file)))
                }
                result(true, "")
            } catch (e: Exception) {
                Log.e(TAG, "sharing", e)
                result(false, e.message ?: e.toString())
            }
        }

        @JavascriptInterface
        fun stopRecording(): String {
            val svc = ScreenCaptureService.instance ?: return result(false, "not recording")
            val err = svc.stopRecording()
            return result(err == null, err ?: "", svc.lastRecordingName())
        }
    }

    private fun result(ok: Boolean, error: String, name: String = ""): String =
        JSONObject().apply {
            put("ok", ok)
            put("error", error)
            put("name", name)
        }.toString()

    /**
     * The last file saved through the bridge, for the share sheet.
     *
     * @Volatile because @JavascriptInterface methods run on the WebView's own
     * bridge thread, not the UI thread, so this is written and read off-main.
     */
    @Volatile
    private var lastSavedUri: Uri? = null

    /**
     * Writes a text file to the public Downloads folder. Returns a
     * human-readable location and the URI to share.
     *
     * MediaStore from Android 10, where an app cannot write shared storage by
     * path; an app-scoped file below that, because asking for
     * WRITE_EXTERNAL_STORAGE to save a log is not a reasonable trade.
     */
    private fun writeToDownloads(name: String, text: String): Pair<String, Uri?> {
        val bytes = text.toByteArray(Charsets.UTF_8)
        if (Build.VERSION.SDK_INT >= 29) {
            val values = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, name)
                put(MediaStore.Downloads.MIME_TYPE, "text/plain")
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            val uri = contentResolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
                ?: throw IllegalStateException("MediaStore refused the entry")
            contentResolver.openOutputStream(uri).use { out ->
                out ?: throw IllegalStateException("MediaStore gave no stream")
                out.write(bytes)
            }
            contentResolver.update(
                uri, ContentValues().apply { put(MediaStore.Downloads.IS_PENDING, 0) }, null, null,
            )
            Mobile.logHost("save: wrote Download/$name (${bytes.size} bytes)")
            return Pair("Download/$name", uri)
        }
        val dir = getExternalFilesDir(null) ?: throw IllegalStateException("no external files dir")
        dir.mkdirs()
        val f = java.io.File(dir, name)
        f.writeBytes(bytes)
        Mobile.logHost("save: wrote ${f.absolutePath} (${bytes.size} bytes)")
        return Pair(f.absolutePath, null)
    }

    private fun prefs() = getSharedPreferences(PREFS, MODE_PRIVATE)

    /**
     * Copies the Android half's state into the Go diagnostics report.
     *
     * The report is generated by the Go server, which can see the bridge, the
     * stream and its own log — and nothing about MediaProjection, the virtual
     * display or the encoder, all of which live here. A report from a phone
     * where recording had failed therefore contained no mention of recording
     * at all, which is how a bug got reported with nothing to act on.
     */
    private fun publishHostInfo() {
        try {
            val sb = StringBuilder()
            sb.append("android sdk           ${Build.VERSION.SDK_INT}\n")
            sb.append("device                ${Build.MANUFACTURER} ${Build.MODEL}\n")
            sb.append("mic permission        ${
                if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) ==
                    PackageManager.PERMISSION_GRANTED
                ) "granted" else "not granted"
            }\n")
            val svc = ScreenCaptureService.instance
            if (svc != null) {
                sb.append(svc.diagnosticsBlock())
            } else {
                sb.append("capture               service not running\n")
                val m = resources.displayMetrics
                sb.append("display               ${m.widthPixels}x${m.heightPixels} @${m.densityDpi}dpi\n")
                sb.append("capture scale         ${ScreenCaptureService.captureScale}\n")
                // Last known, so a report taken after capture stopped still
                // says what the colour engine was being fed.
                if (ScreenCaptureService.pipelineW > 0) {
                    sb.append("colour pipeline       ${ScreenCaptureService.pipelineW}x${ScreenCaptureService.pipelineH} (last)\n")
                }
            }
            Mobile.setHostInfo(sb.toString())
        } catch (e: Exception) {
            Log.w(TAG, "publishing host info", e)
        }
    }



    private companion object {
        const val TAG = "HueMux"
        const val PREFS = "huemux"
        const val PREF_SCALE = "captureScale"
    }
}
