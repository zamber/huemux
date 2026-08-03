# Android WebView JS bridge reachable from third-party pages

**Severity:** `high`

**Category:** `security`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

The Android app's WebView has `HueMuxNative` JavaScript bridge injected unconditionally. The Settings page's About section links to the GitHub repo, and the default `WebViewClient` renders that page inside the WebView with the native bridge still accessible. Any JavaScript on github.com (or any other externally navigated page) can call `startCapture()`, `saveTextFile()`, or other bridge methods.

## Affected Files

- `android/app/src/main/java/com/huemux/app/MainActivity.kt:60` — `webViewClient = WebViewClient()` (no `shouldOverrideUrlLoading`)
- `android/app/src/main/java/com/huemux/app/MainActivity.kt:122` — `webView.addJavascriptInterface(NativeBridge(), "HueMuxNative")`
- `web/settings.js:325-326` — About section sets `about.source.href = a.source_url` (the GitHub repo URL)
- `android/app/src/main/java/com/huemux/app/MainActivity.kt:245-407` — native bridge methods

## Evidence

```kotlin
// MainActivity.kt:60
webViewClient = WebViewClient() // default — all navigation stays in the WebView
```

```kotlin
// MainActivity.kt:122
webView.addJavascriptInterface(NativeBridge(), "HueMuxNative")
```

```javascript
// settings.js:325-326 — clicking this link navigates the WebView to GitHub
about.source.href = a.source_url;
```

The bridge exposes:
- `startCapture(areaId, callback)` — spawns MediaProjection consent + DTLS streaming
- `saveTextFile(name, text)` — writes to public Downloads via MediaStore
- `captureState()` — device/display info
- `stopCapture()`, `stopRecording()`, `shareLastFile()`

## Why It Matters

1. A compromised GitHub repo (or any page the user navigates to) can start screen capture and transmit it to a Hue bridge controlled by the attacker
2. The user sees the MediaProjection consent dialog but the context (why it appeared) is confusing
3. The server's own Origin check (`ws.go:62-108`) protects the HTTP/WS surface but does not protect the Java bridge

## Suggested Fix

Override `shouldOverrideUrlLoading` to open external URLs in the system browser:

```kotlin
webViewClient = object : WebViewClient() {
    override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
        if (request.url.host != "127.0.0.1" && request.url.host != "localhost") {
            view.context.startActivity(Intent(Intent.ACTION_VIEW, request.url))
            return true
        }
        return false
    }
}
```

Or inject the bridge only when the page is on loopback (check URL in `onPageStarted`).

## Related Issues

- `009-no-security-headers.md` — CSP on the web UI would add defense-in-depth
