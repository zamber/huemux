# docs/ — huemux.com

GitHub Pages source for [huemux.com](https://huemux.com), built from this
folder on every push to `main` (see the repo's Pages settings: source =
`main` / `/docs`).

Deliberately **not** using a stock `theme:` (jekyll-theme-minimal, previously)
— that theme has no dark mode and a fixed ~860px layout that looked cramped
on wide monitors. This is a small self-contained Jekyll site instead:

- `_layouts/default.html` — the whole page shell (header/nav, `{{ content }}`,
  footer, lightbox script tag).
- `assets/css/style.css` — design tokens ported from `web/shared/theme.css`
  (the app's own UI), so this page and the app read as one product. Follows
  `prefers-color-scheme` for real light/dark, unlike the old theme.
- `assets/js/lightbox.js` — click-to-zoom for any `<img class="zoomable">`.
- `assets/fonts/` — the same self-hosted Fira Code `.woff2` files as
  `web/shared/fonts/`, copied here since GitHub Pages only publishes this
  `docs/` folder (the `web/` original isn't reachable from a page served
  from here). Keep both copies in sync if the font ever changes.
- `index.md` — the page content. `wide: true` in its front matter widens
  `<main>` (see the layout) for the screenshots section.
- `CNAME` — the custom domain (`huemux.com`).

No build step: kramdown (bundled with the `github-pages` gem GitHub Pages
already runs) is all that's needed, same "no build step" approach as the
Go binary's own embedded frontend.
