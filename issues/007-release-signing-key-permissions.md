# RELEASE-SIGNING-KEY.asc has restrictive permissions and unclear status

**Severity:** `medium`

**Category:** `security`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

`RELEASE-SIGNING-KEY.asc` at the repo root has mode `-rw-------` (0600) and is 421 bytes. This is almost certainly a GPG public key (which should be world-readable, 0644), but the restrictive permissions and the name convention are ambiguous enough to raise concern.

## Affected Files

- `RELEASE-SIGNING-KEY.asc` — 421 bytes, mode 0600
- `.github/workflows/release.yml:214` — copies this file into dist: `cp RELEASE-SIGNING-KEY.asc dist/`

## Evidence

```bash
$ ls -la /home/luna/projects/huemux/RELEASE-SIGNING-KEY.asc
-rw-------  1 luna luna  421 Aug  2 12:55 RELEASE-SIGNING-KEY.asc

$ wc -c /home/luna/projects/huemux/RELEASE-SIGNING-KEY.asc
421 RELEASE-SIGNING-KEY.asc
```

421 bytes is consistent with a single armored GPG public key (no subkeys, one UID).

## Why It Matters

1. **If this is a public key:** the `-rw-------` permission is wrong — it should be `-rw-r--r--` (0644). The release workflow copies it into the release assets so users can verify signatures. Users checking out the repo can't read it.

2. **If this were a private key (it's not, at 421 bytes):** it should not be in the repo at all. GPG private keys are multiple kilobytes. But the filename `RELEASE-SIGNING-KEY.asc` is ambiguous — many projects use `RELEASE-SIGNING-KEY.asc` for the public key and store the private key elsewhere.

3. **The release workflow** at `release.yml:206-210` handles the private key through a GitHub secret (`GPG_PRIVATE_KEY`), imports it temporarily, signs, and shreds the passphrase file. This is correct. The file in the repo is the public half.

## Suggested Fix

```bash
chmod 0644 RELEASE-SIGNING-KEY.asc
```

Also add a comment at the top of the file (or a README next to it) clarifying:

```
This is the PUBLIC key for verifying release signatures.
The private key is stored as a GitHub Actions secret (GPG_PRIVATE_KEY).
```

Alternatively, if the key fingerprint is listed in README.md or a SECURITY.md, that serves the same purpose — users can verify they got the right key through a second channel.

## Related Issues

None.
