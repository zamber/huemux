# Store listing metadata

Fastlane-structured metadata, read by **IzzyOnDroid** and **F-Droid** to build
the app's listing. Also the format Play's `supply` uses, so the same text can
be reused later.

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

## Still to add

**Screenshots.** Deliberately absent rather than forgotten: the obvious
candidates show the bridge's LAN address, and this repository does not carry
local network details (see AGENTS.md). Take fresh ones with the sync page
either disconnected or cropped above the status block.

Four is a good set — lights grid, a room with a colour picker open, the sync
page mid-stream, and settings.

**icon.png.** The launcher icon is a vector
(`android/app/src/main/res/drawable/ic_launcher_foreground.xml`); stores want a
512x512 PNG. Render it rather than drawing a second one, so the two cannot
drift.

## Changelogs

Named by `versionCode`, which here is the commit count at the tag — see
`android/app/build.gradle.kts`. Find it with:

```sh
git rev-list --count <tag>
```

Not required: IzzyOnDroid falls back to the GitHub release body, which
`.github/release-notes/<tag>.md` already provides. Worth adding for a release
that deserves more than "no functional changes".
