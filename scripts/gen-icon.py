#!/usr/bin/env python3
"""Generate every form of the HueMux mark from one geometry definition.

The mark is three coloured inputs — red, green, blue — merging into one
output node. Many hues in, one output; that is what the app does and what
"mux" in the name means.

The horizontal sweep replaced an older six-strand fan. Six gradients read as
a bundle at 512px but turned into mush at the 22px header size; three flat
strands and one node survive the small size.

    scripts/gen-icon.py

Outputs:
    web/shared/icon.svg            dark background, rounded square (default)
    web/shared/icon-light.svg      light background, for light surfaces
    web/shared/icon-mark.svg       bare mark, no background, currentColor-safe
    web/favicon.svg                small-size variant, heavier strokes
    web/favicon.ico                multi-resolution browser fallback
    android/.../ic_launcher_foreground.xml   adaptive foreground
    android/.../ic_launcher_background.xml   adaptive background
    android/.../mipmap-*/ic_launcher.png     raster launcher fallbacks

Raster generation needs ImageMagick's `convert`; the SVG and Android vector
outputs do not, so the script still works on a machine without it.
"""

import os
import shutil
import subprocess

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")

# --- geometry ---------------------------------------------------------------
# One 100x100 space for everything. The Android canvas is 108x108 with only the
# middle 72x72 guaranteed visible, so the mark is scaled into that safe zone
# rather than drawn to the full bleed — a launcher that masks to a tight circle
# would otherwise clip the outermost strands.

STRANDS = ["#FF453A", "#32D74B", "#32ADE6"]

# Three inputs on the left merge into one output node on the right.
DOTS = [(26.0, 30.0), (26.0, 50.0), (26.0, 70.0)]
SINK = (68.0, 50.0)
DOT_R = 5.2
STROKE = 6.4


def furthest_from_centre():
    """Worst-case distance from the centre, for the circular-mask check."""
    pts = [(x, y, DOT_R) for x, y in DOTS] + [(SINK[0], SINK[1], DOT_R + 1.4)]
    return max(((x - 50.0) ** 2 + (y - 50.0) ** 2) ** 0.5 + r for x, y, r in pts)


def strand_path(sx, sy):
    """One horizontal cubic from an input dot to the output node.

    The control points sit on the same horizontal axis, so every strand
    leaves to the right and arrives from the left. Three separate curves
    then read as one merge rather than as unrelated arcs.
    """
    ex, ey = SINK
    bend = (ex - sx) * 0.55
    return (f"M{sx},{sy} C{sx + bend:.1f},{sy} "
            f"{ex - bend:.1f},{ey} {ex},{ey}")


def gradients(prefix, converge):
    """A per-strand gradient from its own hue to the convergence colour.

    The blend is what makes it read as combining rather than merely touching.
    """
    out = []
    for i, colour in enumerate(STRANDS):
        sx, sy = DOTS[i]
        out.append(
            f'    <linearGradient id="{prefix}{i}" x1="{sx}" y1="{sy}" '
            f'x2="{SINK[0]}" y2="{SINK[1]}" gradientUnits="userSpaceOnUse">\n'
            f'      <stop offset="0" stop-color="{colour}"/>\n'
            f'      <stop offset="0.62" stop-color="{colour}"/>\n'
            f'      <stop offset="1" stop-color="{converge}"/>\n'
            f'    </linearGradient>')
    return "\n".join(out)


def mark(prefix, converge, glow):
    """The strands, their dots, and the convergence node."""
    parts = []
    if glow:
        parts.append(f'  <circle cx="{SINK[0]}" cy="{SINK[1]}" r="13" fill="url(#{prefix}glow)"/>')
    for i, colour in enumerate(STRANDS):
        sx, sy = DOTS[i]
        parts.append(
            f'  <path d="{strand_path(sx, sy)}" fill="none" stroke="url(#{prefix}{i})" '
            f'stroke-width="{STROKE}" stroke-linecap="round"/>')
    for i, colour in enumerate(STRANDS):
        sx, sy = DOTS[i]
        parts.append(f'  <circle cx="{sx}" cy="{sy}" r="{DOT_R}" fill="{colour}"/>')
    parts.append(f'  <circle cx="{SINK[0]}" cy="{SINK[1]}" r="{DOT_R + 1.4}" fill="{converge}"/>')
    return "\n".join(parts)


def svg(bg_shape, converge, glow, prefix="g"):
    glow_def = ""
    if glow:
        glow_def = (f'    <radialGradient id="{prefix}glow">\n'
                    f'      <stop offset="0" stop-color="{converge}" stop-opacity="0.55"/>\n'
                    f'      <stop offset="1" stop-color="{converge}" stop-opacity="0"/>\n'
                    f'    </radialGradient>\n')
    return (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" '
        'width="512" height="512" role="img" aria-label="HueMux">\n'
        '  <defs>\n'
        f'{glow_def}{gradients(prefix, converge)}\n'
        '  </defs>\n'
        f'{bg_shape}'
        f'{mark(prefix, converge, glow)}\n'
        '</svg>\n')


def write(path, content):
    full = os.path.join(ROOT, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w") as f:
        f.write(content)
    print(f"  {path}")


print(f"mark reaches {furthest_from_centre():.1f} of 50 units from centre "
      f"(circular masks need < 50)")
print("writing icons:")

# Dark: the default everywhere. Convergence is white and glows.
write("web/shared/icon.svg", svg(
    '  <rect width="100" height="100" rx="22" fill="#0A0A0A"/>\n',
    "#FFFFFF", glow=True, prefix="d"))

# Light: same mark, dark convergence node so it stays visible on white.
write("web/shared/icon-light.svg", svg(
    '  <rect width="100" height="100" rx="22" fill="#F7F3EA"/>\n',
    "#12121A", glow=False, prefix="l"))

# Bare mark, for a surface that already has a background — the app header.
#
# Two files rather than one using currentColor: an <img> is a replaced element
# with its own document, so currentColor inside it resolves against the SVG's
# own root, not the page's. The only ways to make one file adapt are inlining
# the markup or a CSS mask, and both cost more than shipping a second 1KB file
# that the theme switcher swaps.
write("web/shared/icon-mark.svg", svg("", "#FFFFFF", glow=False, prefix="m"))
write("web/shared/icon-mark-light.svg", svg("", "#12121A", glow=False, prefix="ml"))

# Favicon: heavier strokes and larger dots survive 16px far better.
STROKE, DOT_R = 6.0, 4.8
write("web/favicon.svg", svg(
    '  <rect width="100" height="100" rx="22" fill="#0A0A0A"/>\n',
    "#FFFFFF", glow=False, prefix="f"))
STROKE, DOT_R = 6.4, 5.2

# --- Android ----------------------------------------------------------------
# VectorDrawable, not SVG. Gradients need the aapt namespace; supported from
# API 24 and this app is minSdk 26. Scaled into the 72x72 safe zone: the
# adaptive canvas is 108x108 but a launcher may mask it to any shape.

SCALE = 72.0 / 100.0
OFF = (108.0 - 72.0) / 2.0


def t(v):
    return round(v * SCALE + OFF, 2)


def vd_gradient(colour, sx, sy, converge):
    return (
        '            <gradient\n'
        '                android:type="linear"\n'
        f'                android:startX="{t(sx)}" android:startY="{t(sy)}"\n'
        f'                android:endX="{t(SINK[0])}" android:endY="{t(SINK[1])}">\n'
        f'                <item android:offset="0" android:color="{colour}"/>\n'
        f'                <item android:offset="0.62" android:color="{colour}"/>\n'
        f'                <item android:offset="1" android:color="{converge}"/>\n'
        '            </gradient>\n')


def vd_path(sx, sy):
    ex, ey = SINK
    bend = (ex - sx) * 0.55
    return (f"M{t(sx)},{t(sy)} C{t(sx + bend)},{t(sy)} "
            f"{t(ex - bend)},{t(ey)} {t(ex)},{t(ey)}")


def vd_circle(cx, cy, r):
    """VectorDrawable has no circle element; emit one as two arcs."""
    return (f"M{t(cx) - r * SCALE},{t(cy)} "
            f"a{r * SCALE},{r * SCALE} 0 1,0 {2 * r * SCALE},0 "
            f"a{r * SCALE},{r * SCALE} 0 1,0 {-2 * r * SCALE},0 Z")


converge = "#FFFFFFFF"
lines = [
    '<?xml version="1.0" encoding="utf-8"?>',
    '<!--',
    '  Generated by scripts/gen-icon.py — do not edit by hand.',
    '',
      '  Three inputs converging into one: many hues in, one output. Drawn inside',
    '  the 72x72 safe zone of the 108dp adaptive canvas, because a launcher may',
    '  mask this to any shape and the outermost strands would otherwise clip.',
    '-->',
    '<vector xmlns:android="http://schemas.android.com/apk/res/android"',
    '    xmlns:aapt="http://schemas.android.com/aapt"',
    '    android:width="108dp" android:height="108dp"',
    '    android:viewportWidth="108" android:viewportHeight="108">',
    '',
]
for i, colour in enumerate(STRANDS):
    sx, sy = DOTS[i]
    lines += [
        f'    <path android:pathData="{vd_path(sx, sy)}"',
        f'        android:strokeWidth="{round(STROKE * SCALE, 2)}"',
        '        android:strokeLineCap="round">',
        '        <aapt:attr name="android:strokeColor">',
        vd_gradient(colour + "FF" if len(colour) == 7 else colour, sx, sy, converge).rstrip(),
        '        </aapt:attr>',
        '    </path>',
    ]
for i, colour in enumerate(STRANDS):
    sx, sy = DOTS[i]
    lines.append(f'    <path android:fillColor="{colour}" '
                 f'android:pathData="{vd_circle(sx, sy, DOT_R)}"/>')
lines.append(f'    <path android:fillColor="{converge}" '
             f'android:pathData="{vd_circle(SINK[0], SINK[1], DOT_R + 1.4)}"/>')
lines += ['</vector>', '']
write("android/app/src/main/res/drawable/ic_launcher_foreground.xml", "\n".join(lines))

write("android/app/src/main/res/drawable/ic_launcher_background.xml",
      '<?xml version="1.0" encoding="utf-8"?>\n'
      '<!-- Generated by scripts/gen-icon.py — do not edit by hand. -->\n'
      '<vector xmlns:android="http://schemas.android.com/apk/res/android"\n'
      '    android:width="108dp" android:height="108dp"\n'
      '    android:viewportWidth="108" android:viewportHeight="108">\n'
      '    <path android:fillColor="#0A0A0A" android:pathData="M0,0h108v108h-108z"/>\n'
      '</vector>\n')

# Raster outputs. The launcher and browser surfaces need actual bitmaps:
# some launchers ignore the adaptive vector, and a browser tab caches an ICO
# independently of the SVG. ImageMagick is optional so the vector pipeline
# still runs on a bare machine.
if shutil.which("convert"):
    subprocess.run([
        "convert", "-background", "none",
        os.path.join(ROOT, "web/favicon.svg"),
        "-define", "icon:auto-resize=48,32,16",
        os.path.join(ROOT, "web/favicon.ico"),
    ], check=True)
    for density, size in (("mdpi", 48), ("hdpi", 72), ("xhdpi", 96),
                          ("xxhdpi", 144), ("xxxhdpi", 192)):
        outdir = os.path.join(ROOT, "android/app/src/main/res", "mipmap-" + density)
        os.makedirs(outdir, exist_ok=True)
        subprocess.run([
            "convert", "-background", "none",
            os.path.join(ROOT, "web/shared/icon.svg"),
            "-resize", f"{size}x{size}",
            os.path.join(outdir, "ic_launcher.png"),
        ], check=True)
    print("  web/favicon.ico")
    print("  android/.../mipmap-*/ic_launcher.png")
else:
    print("  (ImageMagick convert not found; skipped favicon.ico and raster launcher icons)")

print("done")
