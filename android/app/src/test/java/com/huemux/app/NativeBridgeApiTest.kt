package com.huemux.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/**
 * Keeps the web frontend and the Kotlin native bridge from drifting apart.
 *
 * The JS page talks to [MainActivity.NativeBridge] through
 * `addJavascriptInterface`. Android's bridge resolves a call by method name
 * and argument count, so a JS call that names a method the Kotlin side
 * removed, or passes a different number of arguments than the method accepts,
 * fails at runtime with "no method ...". That failure mode shipped once — in
 * alpha.36 `startCapture` was called with three arguments against a
 * two-parameter method — and was invisible to every pipeline because nothing
 * exercised the bridge contract.
 *
 * These tests re-derive that contract from the two sources and assert they
 * agree: every `@JavascriptInterface` method in [MainActivity], every
 * `window.HueMuxNative.<method>(...)` call in the web sources, and every
 * member call to a bridge-named method (the page reaches most of the bridge
 * through a local alias, e.g. `const n = window.HueMuxNative`).
 */
class NativeBridgeApiTest {

    /** Web files that invoke the native bridge, relative to the repo root. */
    private val webFiles = listOf("web/app.js", "web/music.js", "web/settings.js")

    private val kotlinMain = "android/app/src/main/java/com/huemux/app/MainActivity.kt"

    private val jsInterface = Regex("""@JavascriptInterface\s+fun\s+(\w+)\s*\(([^)]*)\)""")

    /** Walks up from the test working dir to the repository root. */
    private fun repoRoot(): File {
        var dir: File? = File(System.getProperty("user.dir"))
        repeat(6) {
            val d = dir ?: return@repeat
            if (File(d, "web/app.js").isFile && File(d, kotlinMain).isFile) return d
            dir = d.parentFile
        }
        // org.junit.Assert.fail returns void, so Kotlin cannot see it as the
        // terminating statement of a File-returning function ("missing return
        // statement"); throw the same AssertionError fail() throws internally.
        throw AssertionError("could not locate the repo root from ${System.getProperty("user.dir")}")
    }

    /** Maps every @JavascriptInterface method name to its parameter count. */
    private fun nativeApi(root: File): Map<String, Int> {
        val src = File(root, kotlinMain).readText()
        val api = linkedMapOf<String, Int>()
        for (m in jsInterface.findAll(src)) {
            api[m.groupValues[1]] = paramCount(m.groupValues[2])
        }
        return api
    }

    private fun paramCount(params: String): Int =
        if (params.isBlank()) 0 else params.split(",").size

    /**
     * Counts top-level arguments in a call whose `(` sits at [openIdx] in [s].
     * Starts just after the `(` — counting the `(` itself as a nested paren
     * would put every real comma inside it and read `f(a, b)` as one argument.
     * Understands nested parentheses and quoted strings, so
     * `setCaptureScale(Number(els.captureScale.value) / 100)` counts as one
     * argument and `saveTextFile(name, text)` as two.
     */
    private fun countArgs(s: String, openIdx: Int): Int {
        var depth = 0
        var args = 1
        var hasAny = false
        var inStr: Char? = null
        var i = openIdx + 1
        while (i < s.length) {
            val c = s[i]
            if (inStr != null) {
                if (c == '\\') i++
                else if (c == inStr) inStr = null
            } else {
                when {
                    c == '\'' || c == '"' -> inStr = c
                    c == '(' -> depth++
                    c == ')' -> {
                        if (depth == 0) return if (hasAny) args else 0
                        depth--
                    }
                    c == ',' && depth == 0 -> args++
                    !c.isWhitespace() -> hasAny = true
                }
            }
            i++
        }
        return -1
    }

    @Test
    fun everyNativeMethodIsUsedByTheWebPage() {
        val root = repoRoot()
        val api = nativeApi(root)
        assertTrue("no @JavascriptInterface methods found in $kotlinMain", api.isNotEmpty())
        val allJs = webFiles.map { File(root, it).readText() }.joinToString("\n")
        for (name in api.keys) {
            assertTrue(
                "native method $name is never invoked from the web sources",
                allJs.contains(".$name("),
            )
        }
    }

    @Test
    fun qualifiedCallsNameRealMethodsWithMatchingArity() {
        val root = repoRoot()
        val api = nativeApi(root)
        val qualifiedCall = Regex("""window\.HueMuxNative\.(\w+)\s*\(""")
        for (f in webFiles) {
            val src = File(root, f).readText()
            for (m in qualifiedCall.findAll(src)) {
                val name = m.groupValues[1]
                val want = api[name]
                assertTrue(
                    "$f calls window.HueMuxNative.$name(...) but no such @JavascriptInterface method exists",
                    want != null,
                )
                val args = countArgs(src, m.range.last)
                assertEquals(
                    "$f calls window.HueMuxNative.$name with $args arg(s); the native method takes $want",
                    want, args,
                )
            }
        }
    }

    @Test
    fun qualifiedCapabilityChecksNameRealMethods() {
        val root = repoRoot()
        val api = nativeApi(root)
        val ref = Regex("""window\.HueMuxNative\.(\w+)""")
        for (f in webFiles) {
            val src = File(root, f).readText()
            for (m in ref.findAll(src)) {
                assertTrue(
                    "$f references window.HueMuxNative.${m.groupValues[1]} but no such @JavascriptInterface method exists",
                    api.containsKey(m.groupValues[1]),
                )
            }
        }
    }

    @Test
    fun memberCallsToBridgeMethodsMatchNativeArity() {
        val root = repoRoot()
        val api = nativeApi(root)
        val names = api.keys.joinToString("|")
        // The page reaches most of the bridge through a local alias such as
        // `const n = window.HueMuxNative`. Only calls on that alias (or on the
        // qualified name) are bridge calls; a regex over bare `.name(` would
        // also match unrelated objects that happen to have a method with the
        // same name (e.g. HueMuxMusic.startCapture()).
        val aliasRe = Regex("""(?:const|let|var)\s+(\w+)\s*=\s*window\.HueMuxNative\b""")
        for (f in webFiles) {
            val src = File(root, f).readText()
            val aliases = aliasRe.findAll(src).map { it.groupValues[1] }.distinct()
            val receiver = buildString {
                append("window\\.HueMuxNative")
                for (a in aliases) append("|\\b").append(a)
            }
            val memberCall = Regex("""(?:$receiver)\.($names)\s*\(""")
            for (m in memberCall.findAll(src)) {
                val name = m.groupValues[1]
                val want = api[name] ?: continue
                val args = countArgs(src, m.range.last)
                assertEquals(
                    "$f calls .$name with $args arg(s); the native method takes $want",
                    want, args,
                )
            }
        }
    }
}
