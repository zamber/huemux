# AGENTS.md says "no LICENSE file" but LICENSE exists

**Severity:** `medium`

**Category:** `documentation`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

`AGENTS.md:10` states: "there is **no `LICENSE` file**, and a public repo without one is all rights reserved, so no packager may redistribute it." A `LICENSE` file (GPL-3.0-or-later, 35,149 bytes) was subsequently added but the documentation was not updated.

## Affected Files

- `AGENTS.md:9-13` — "One thing blocks distribution" paragraph
- `LICENSE` — GPL-3.0-or-later, present and valid

## Evidence

```markdown
<!-- AGENTS.md lines 9-13 -->
One thing blocks distribution: there is **no `LICENSE` file**, and a public
repo without one is all rights reserved, so no packager may redistribute it.
GPL-3.0-or-later is the recommendation — see PUBLISHING.md §0.1, which also
covers why GPL-2.0 specifically would not work here.
```

```bash
$ ls -la /home/luna/projects/huemux/LICENSE
-rw-r--r-- 1 luna luna 35149 Aug  2 11:39 LICENSE
$ head -3 LICENSE
                    GNU GENERAL PUBLIC LICENSE
                       Version 3, 29 June 2007
```

## Why It Matters

A contributor or packager reading AGENTS.md sees a false obstacle. The FOSS-first distribution plan (Obtainium, IzzyOnDroid, Flathub, F-Droid) depends on a clear license status. Contradictory documentation erodes trust in the project's self-knowledge.

## Suggested Fix

Replace the paragraph in AGENTS.md with a statement confirming the license is present:

```markdown
The project is licensed under GPL-3.0-or-later (see [LICENSE](LICENSE)).
```

Also verify that PUBLISHING.md §0.1 has been updated to reflect the license now existing rather than being a recommendation.

## Related Issues

None.
