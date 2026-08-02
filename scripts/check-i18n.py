#!/usr/bin/env python3
"""Validate every translation against the English source.

Run after any string change, and in CI:

    scripts/check-i18n.py

Translations are machine-produced, which makes them fast to obtain and easy to
get subtly wrong in ways no reviewer who does not read the language will spot.
These are the failures that are checkable without knowing the language, and
each one has bitten real projects:

  - a missing key      renders as a raw key path on screen
  - an extra key       is dead weight, and usually a hallucinated feature
  - a mangled {token}  is a substitution that silently never happens
  - a translated brand turns HueMux into something else in that locale
  - a stray RTL mark   corrupts rendering in ways that are near-impossible to
                       trace back to a JSON file

What it cannot check is whether the translation is any good. That needs a
speaker, and the Settings hint says so.
"""

import json
import os
import re
import sys

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
I18N = os.path.join(ROOT, "web", "shared", "i18n")
BASE = "en"

# Substituted at runtime by i18n.js; a translation that alters one produces a
# sentence with a literal brace in it, or a value that never appears.
PLACEHOLDER = re.compile(r"\{[a-zA-Z_]+\}")

# Must survive verbatim in every language.
KEEP = ["HueMux", "Philips", "Hue", "DTLS", "TLS", "GPL-3.0-or-later"]

# Bidi overrides. The layout sets direction; marks embedded in strings fight it
# and are invisible in an editor.
BIDI = re.compile(r"[‎‏‪-‮⁦-⁩]")


def flatten(o, prefix=""):
    out = {}
    for k, v in o.items():
        key = f"{prefix}.{k}" if prefix else k
        if isinstance(v, dict):
            out.update(flatten(v, key))
        else:
            out[key] = v
    return out


def main():
    base_path = os.path.join(I18N, BASE + ".json")
    base = flatten(json.load(open(base_path, encoding="utf-8")))

    files = sorted(f for f in os.listdir(I18N)
                   if f.endswith(".json") and f != BASE + ".json")
    if not files:
        print("no translations found", file=sys.stderr)
        return 1

    failures = 0
    for fname in files:
        tag = fname[:-5]
        path = os.path.join(I18N, fname)
        problems = []

        try:
            with open(path, encoding="utf-8") as f:
                raw = f.read()
            data = flatten(json.loads(raw))
        except Exception as e:
            print(f"FAIL {tag:12} unparseable: {e}")
            failures += 1
            continue

        missing = sorted(set(base) - set(data))
        extra = sorted(set(data) - set(base))
        if missing:
            problems.append(f"{len(missing)} missing: {', '.join(missing[:5])}"
                            + (" …" if len(missing) > 5 else ""))
        if extra:
            problems.append(f"{len(extra)} extra: {', '.join(extra[:5])}"
                            + (" …" if len(extra) > 5 else ""))

        for key in sorted(set(base) & set(data)):
            src, dst = base[key], data[key]
            if not isinstance(dst, str):
                problems.append(f"{key}: not a string")
                continue
            want = sorted(PLACEHOLDER.findall(src))
            got = sorted(PLACEHOLDER.findall(dst))
            if want != got:
                problems.append(f"{key}: placeholders {want} -> {got}")
            for term in KEEP:
                # Only require a term to survive if the source used it and the
                # target is not simply a shorter rewording that dropped it —
                # a missing brand name is worth flagging either way.
                if term in src and term not in dst:
                    problems.append(f"{key}: lost '{term}'")

        if BIDI.search(raw):
            problems.append("contains bidi control characters")

        if problems:
            failures += 1
            print(f"FAIL {tag:12}")
            for p in problems[:12]:
                print(f"       {p}")
            if len(problems) > 12:
                print(f"       … and {len(problems) - 12} more")
        else:
            print(f"ok   {tag:12} {len(data)} strings")

    print()
    print(f"{len(files)} translations, {failures} with problems")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
