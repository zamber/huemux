# Duplicate code between cmd/huemux and cmd/huemux-desktop

**Severity:** `medium`

**Category:** `maintainability`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

`cmd/huemux/main.go` and `cmd/huemux-desktop/main.go` share ~200 lines of identical code across five functions. The AGENTS.md documents this as intentional ("duplicated rather than shared so that binary is never touched by this one existing") but this justification is thin — sharing a small `internal/runloop` package would not affect the plain binary's size.

## Affected Files

- `cmd/huemux/main.go:481-515` — `shutdown`, `readStdinCommands`, `openBrowser`, `fatalf`
- `cmd/huemux-desktop/main.go:281-397` — identical `runHeadless` loop, `shutdown`, `readStdinCommands`, `openBrowser`, `fatalf`

## Evidence

Four functions are byte-for-byte identical:

| Function | `cmd/huemux` | `cmd/huemux-desktop` |
|---|---|---|
| `shutdown` | line 481 | line 348 |
| `readStdinCommands` | line 490 | line 357 |
| `openBrowser` | line 504 | line 381 |
| `fatalf` | line 533 | line 394 |

The headless mode loop (`cmdRun` lines 361-465 vs `runHeadless` lines 281-346) is 90% structurally identical, differing only in:
- `srv.Config()` access (needed in headless because it can't close over `cfg` from parent scope)
- Error message prefixes (`"huemux:" ` vs `"huemux-desktop:" `)

## Why It Matters

- A bug fix in one copy must be manually replicated in the other
- The `separate binary` justification only applies to compile-time dependencies (Electron/go-astilectron imports), not to shared utility functions
- The four identical functions (shutdown/readStdinCommands/openBrowser/fatalf) have zero Electron dependency — they use only `fmt`, `os`, `os/exec`, `runtime`, `bufio`, which the plain binary already links

## Suggested Fix

Extract the duplicated functions into a small shared package or file:

```
internal/runloop/
  stdin.go     — readStdinCommands
  shutdown.go  — shutdown (needs engine + store params)
  browser.go   — openBrowser  
  fatal.go     — fatalf (needs binary name param)
```

Or simpler: put them in `cmd/shared/` as a non-`main` package under the `cmd/` tree.

The headless loop itself is harder to deduplicate because `cmd/huemux-desktop` needs `srv.Config()` dynamically (the config can change at runtime). But the four utility functions are trivial.

## Related Issues

None.
