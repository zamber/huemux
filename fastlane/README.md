# Store listing metadata

Fastlane-structured metadata, read by **F-Droid** to build the app's listing.
Also the format Play's `supply` uses, so the same text can be reused later.

```
fastlane/metadata/android/<locale>/
  title.txt              app name
  short_description.txt  one line, 80 characters max
  full_description.txt   listing body, limited HTML (<b>, <i>, <u>, <br>)
  images/
    icon.png             512x512
    phoneScreenshots/    1.png, 2.png, …
  changelogs/
    <versionCode>.txt    per-release notes
```

## Screenshots — pending, taken by the user

**How to take them** (phone, portrait, ~9:19 aspect like a real
screenshot; each file exactly `1.png`, `2.png`, `3.png`, `4.png`):

1. **Lights grid** — the rooms view with a few lights on. Crop out the
   top of the screen if it shows the server address.
2. **Colour picker** — tap a colour-capable bulb so the picker overlay is
   open over the lights grid.
3. **Sync mid-stream** — the sync page with sync running and the preview
   showing zones. The status block at the bottom shows the bridge's LAN
   address — crop it out or pause the page so it is absent.
4. **Settings** — the settings page.

Keep the bridge's LAN address and any other private detail out of every
frame; the repo is public. Save the files into
`fastlane/metadata/android/en-US/images/phoneScreenshots/` and the same
into `.../pl/...` — ready for a future store submission.

Why not the existing `docs/screenshots/`: they were browser captures
(desktop aspect) and their predecessors showed the bridge's LAN address;
fresh phone-captured images are what a store listing wants anyway.

## icon.png — done, and how to redo it

`images/icon.png` is 512x512, opaque, rendered from `web/shared/icon.svg` so it
cannot drift from the launcher icon. Both come from `scripts/gen-icon.py`.

Rasterising needs a real browser. ImageMagick's SVG renderer ignores gradient
references on strokes and produces a silhouette in flat black — it looks like a
bug in the artwork rather than in the tool, which is exactly how it wastes an
hour. To redo it after an icon change:

```sh
scripts/gen-icon.py
# serve the repo and open a page containing:
#   <img src="/shared/icon.svg" style="display:block;width:512px;height:512px">
# then screenshot that element at CSS scale.
```

Check the result is exactly 512x512 and has no alpha channel; stores reject a
listing icon with transparency.

## Changelogs

Named by `versionCode`, which here is the commit count at the tag — see
`android/app/build.gradle.kts`. Find it with:

```sh
git rev-list --count <tag>
```

Not required: F-Droid falls back to the GitHub release body, which
`.github/release-notes/<tag>.md` already provides. Worth adding for a release
that deserves more than "no functional changes".
