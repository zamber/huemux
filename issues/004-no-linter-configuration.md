# No linter configuration — only `go vet` and `gofmt`

**Severity:** `high`

**Category:** `maintainability`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

The project uses `go vet ./...` and `gofmt -l` as its only static analysis. No golangci-lint, staticcheck, revive, or any other linter is configured. Several bug classes are not caught.

## Affected Files

- `Makefile:41-42` — `vet` and `fmt` targets only
- `.github/workflows/release.yml:138` — runs `go vet ./...` before build
- `.github/workflows/android.yml:50-51` — runs `scripts/gen-third-party-licenses.sh` and `scripts/check-i18n.py` but no linter
- (absent) `.golangci.yml` — does not exist

## Evidence

```makefile
# Makefile:41-45
vet:
	go vet ./...
test:
	go test ./...
```

The release workflow runs only `go vet`:

```yaml
# release.yml:136-139
- name: Vet and test
  run: |
    go vet ./...
    go test ./...
```

## Why It Matters

`go vet` catches only a small subset of bugs. The following issue classes are undetected:

| Linter | What it would catch here |
|---|---|
| `staticcheck` | Unused functions, ineffective assignments, deprecated APIs |
| `errcheck` | Ignored error returns (there are several `_ = ...` in the codebase) |
| `gosec` | Security issues beyond `InsecureSkipVerify` (already `//nolint`'d) |
| `ineffassign` | Assignment that never takes effect |
| `misspell` | Typos in identifiers (e.g. `Favorite` not `Favourite` — deliberate UK/US split, but linter catches the pattern) |
| `govet` fieldalignment | Struct field reordering for memory efficiency |

Notable examples of what a linter would flag:
- `internal/server/http.go:249`: `_ = conn.WriteMessage(opText, raw)` — ignored write error in broadcast loop
- `internal/config/settings.go:172`: `_ = os.WriteFile(path, raw, 0o600)` — silently discarded write error
- `internal/config/favorites.go:94`: `_ = os.WriteFile(path, raw, 0o600)` — same pattern

## Suggested Fix

1. Add `.golangci.yml` with a conservative initial set
2. Add `golangci-lint run` to CI (both `release.yml` and `android.yml`)
3. Add a `lint` target to the Makefile
4. Fix or `//nolint` existing violations

Initial `.golangci.yml` recommendation:

```yaml
linters:
  enable:
    - govet
    - staticcheck
    - errcheck
    - gosec
    - ineffassign
    - unused
    - misspell
    - gofmt
    - goimports
linters-settings:
  errcheck:
    exclude-functions:
      - (*github.com/zamber/huemux/internal/server.Conn).WriteMessage
      - (*os.File).Close
      - (*os.File).Write
  gosec:
    excludes:
      - G402  # TLS InsecureSkipVerify — intentionally skipped for bridge
```

## Related Issues

None.
