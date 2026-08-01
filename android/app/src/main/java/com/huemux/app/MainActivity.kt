package com.huemux.app

import android.annotation.SuppressLint
import android.net.wifi.WifiManager
import android.os.Bundle
import android.util.Log
import android.view.ViewGroup
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.TextView
import androidx.activity.OnBackPressedCallback
import androidx.appcompat.app.AppCompatActivity
import com.huemux.mobile.Mobile

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
            // Keep navigation inside the app rather than bouncing to Chrome.
            webViewClient = WebViewClient()
        }
        setContentView(webView)

        // Keep in-app back navigation working before it falls through to
        // finishing the activity.
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (webView.canGoBack()) webView.goBack() else finish()
            }
        })

        acquireMulticastLock()
        startServer()
    }

    private fun startServer() {
        try {
            // filesDir is the app-private directory Android guarantees is
            // writable; os.UserConfigDir() means nothing here, which is why
            // internal/config grew SetDir.
            val url = Mobile.start(filesDir.absolutePath)
            Log.i(TAG, "huemux server started at $url")
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
            multicastLock?.let { if (it.isHeld) it.release() }
            Mobile.stop()
        }
        super.onDestroy()
    }

    private companion object {
        const val TAG = "HueMux"
    }
}
