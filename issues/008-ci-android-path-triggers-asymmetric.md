# CI Android verify job has incomplete path triggers

**Severity:** `medium`

**Category:** `correctness` / `maintainability`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

The `android.yml` workflow's `verify` job only triggers on `mobile/**` changes (plus `internal/**` generically). It does NOT include `internal/hue/**` or `internal/pipeline/**` explicitly, and the generic `internal/**` should cover them. But the `paths` filter at line 19-22 of `android.yml` lists `internal/**` which is correct. However, the `paths` filter does not include `cmd/**` — a change to `cmd/huemux/main.go` that breaks the Android cross-compile would not trigger the verify job.

## Affected Files

- `.github/workflows/android.yml:19-28` — path filter for `push` trigger
- `.github/workflows/android.yml:26-31` — path filter for `pull_request` trigger

## Evidence

```yaml
# android.yml push trigger:19-28
push:
  branches: [main]
  paths:
    - 'mobile/**'
    - 'internal/**'
    - 'web/**'
    - 'go.mod'
    - 'go.sum'
    - '.github/workflows/android.yml'
```

```yaml
# android.yml PR trigger:26-31
pull_request:
  paths:
    - 'mobile/**'
    - 'internal/**'
    - 'go.mod'
    - 'go.sum'
```

Differences between push and PR triggers:
- PR trigger is missing `web/**` — a frontend change won't trigger verification on PR
- PR trigger is missing `.github/workflows/android.yml` — a workflow change won't test itself

Both are missing:
- `cmd/**` — the entry points that import `internal/**` packages
- `Makefile` — build configuration changes
- `assets.go` — if the embed directive changes

## Why It Matters

1. A change to `cmd/huemux/main.go` that adds a non-Android-compatible import would pass CI on PR (no verify job trigger) but fail in the release AAR build
2. A change to `web/` files on a PR wouldn't trigger Android verification — the embedded assets change but the Go build isn't checked
3. The asymmetry between push and PR triggers means a PR can pass while the push to main fails — the worst CI experience

## Suggested Fix

Either:
1. Make push and PR triggers identical
2. Or simplify to just `'**'` (all paths, the default when `paths` is omitted) and rely on the fast compile check being cheap enough to always run

The cross-compile check (`CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build ./...`) takes seconds. There is no reason to gate it behind a path filter. Removing the path filter eliminates this entire class of misconfiguration.

## Related Issues

None.
