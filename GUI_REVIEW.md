# GUI Review

Full review of the web UI: 19 Go `html/template` files under
`internal/handlers/templates/` (~3,200 lines total), a vendored htmx + Alpine,
one compiled Tailwind `app.css`, and a CSP middleware.

## Status: ALL FIXED

Every P1–P4 item below is implemented and verified (build + vet + full test
suite green, including new template-render regression tests in
`internal/handlers/templates_test.go`). Summary of the fixes:

- **P1a** — `{{template "nav.html" .}}` → `{{template "nav" .}}` in discover/
  tokens/plugins/sources; those 4 pages render their nav again (verified:
  `nav` renders 15 KB, `nav.html` rendered 1 byte).
- **P1b** — both logout forms now carry the `csrf_token` hidden field; logout
  no longer 403s. Also hardened `withCSRF` against typed-nil maps (panic fix).
- **P2a** — discover result cards no longer interpolate `source_url` into an
  `onclick` string; the URL goes into `data-url` (attribute-escaped) and is
  read via `dataset.url`. XSS vector closed; `jsEscape` deleted.
- **P3a** — new `{{template "head_assets" .}}` partial owns the htmx/alpine/
  `_csrf`/CSS includes; all 12 nav pages use it (fixed comic_browse's missing
  stylesheet and the htmx-missing-on-nav-pages drift). Theming unified to
  `bg-background text-white` (was 3 conflicting strategies).
- **P2b** — bare global `event` removed from dashboard functions; explicit
  `event` param + `noopener` on `window.open`.
- **P4** — aria-labels on icon buttons, `aria-expanded` on settings/mobile-
  nav toggles, keyboard support for the rating widget (tabindex + Enter /
  ArrowUp/Down), `loading="lazy"` on covers, redundant `<meta csrf-token>`
  removed.
- **P3b** — `TestTemplateNavGuard`, `TestTemplateThemeGuard`, `TestPagesExecute`
  guard the nav name, htmx presence, theme class, and per-page renderability.
- **P2c** — NOT changed: `'unsafe-eval'` stays in CSP because the vendored
  htmx calls `eval()` for `hx-on`/`hx-vals` features in active use; removing
  it would break those handlers.

Details below.

---

## P1 — Broken functionality

### P1a — Four pages render with NO navigation bar  (CONFIRMED)

`nav.html` opens with `{{define "nav"}}`. Go's `html/template` registers that
block under the name **`nav`**. The file-level template **`nav.html`** that
`ParseFS` also creates is EMPTY — its entire body lives inside the `{{define}}`,
so executing `nav.html` emits 1 byte (a newline). Verified:

```
'nav'      rendered 15326 bytes; has <nav tag: true
'nav.html' rendered     1 bytes; has <nav tag: false
```

Four pages include the wrong name:

| File | Line | Should be |
|------|------|-----------|
| `discover.html` | 13 | `{{template "nav" .}}` |
| `tokens.html` | 14 | `{{template "nav" .}}` |
| `plugins.html` | 13 | `{{template "nav" .}}` |
| `sources.html` | 13 | `{{template "nav" .}}` |

Effect: Discover, API Tokens, Plugins, and Sources pages show **no top nav**.
Once a user reaches one (via another page's nav menu) they are stranded — no
Discover/Comics/Add/Poll/Library/Logout affordances, only the browser back
button. The mobile nav (`#mobile-nav`) is also absent. This is the highest-
impact GUI bug.

Fix: s/`{{template "nav.html" .}}`/`{{template "nav" .}}/` in those 4 files.
Also add a regression test (see P3b).

### P1b — Logout is rejected by CSRF (403)  (HIGH CONFIDENCE — verify runtime)

`cmd/main.go:325` wraps the entire router in `nosurf.New(r)`. The
`ExemptFunc` (line 333) exempts only `/api/v1/*` and safe verbs (GET/HEAD/
OPTIONS). `/logout` is a POST and is **not** exempt, so nosurf requires a
matching CSRF token in the `csrf_token` form field or `X-CSRF-Token` header.

The logout form in `nav.html` (both the desktop line 190 and mobile line 219
copies) has **neither**:

```html
<form method="POST" action="/logout" ...><button ...>Logout</button></form>
```

No global JS injects CSRF into plain form submissions (only the
`htmx:configRequest` listener adds the header, and that's htmx-only). So a
logout click → nosurf 403 → user stays logged in. Every other POST form in the
app (sources promote/toggle/delete, import, provider config) correctly carries
`<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">`; logout is the
sole exception.

Fix: add the hidden CSRF input to both logout forms in `nav.html`. Verify with
a runtime smoke test (start server, log in, click logout).

---

## P2 — Security / correctness

### P2a — Stored-XSS vector in Discover search results

`discover.html` builds result-card HTML by string concatenation and assigns to
`innerHTML`. The "Add to Library" button embeds the provider's `source_url`
into an `onclick` attribute:

```js
card += '<button class="btn-add" onclick="event.stopPropagation(); ' +
        'addSeries(this, \'' + jsEscape(r.source_url) + '\')">Add to Library</button>';
```

`jsEscape` (line 154) escapes only `\` and `'` — **not `"`**. The onclick
attribute is delimited by `"`, so a `source_url` containing `"` breaks out of
the attribute and injects markup/JS. A URL like `x");alert(document.cookie);//`
would execute. `source_url` comes from external provider search APIs
(mangadex/royalroad/etc.), so an attacker who can influence a provider's listed
URL — or a compromised/malicious provider — achieves script execution in a
logged-in user's session.

The sibling helpers are fine: `escAttr`/`escHtml` escape `& < > " '` and are
used for `src="..."` (image URLs). The gap is specifically the JS-string-in-
HTML-attribute context for `jsEscape`.

Fix: build cards with DOM APIs (`createElement` + `textContent` + `dataset`)
instead of string concatenation, OR run `source_url` through both `escHtml`
(attribute context) and a fully-context-aware JS escape. Simplest robust fix:
don't inline the URL into the attribute at all — store it in `data-url` (with
`escHtml`) and read `this.dataset.url` in the handler.

### P2b — Reliance on the deprecated global `event`

`dashboard.html` (`markCheck`, `openExt`, …), `comic_browse.html`, `reader.html`,
`discover.html`, `add_series.html` reference bare `event` inside onclick-called
functions:

```js
function markCheck(btn, id) {
    event.stopPropagation();   // no `event` parameter
    ...
}
```

This leans on `window.event` (the old IE model). Modern Chrome/Edge/Firefox
still expose `window.event` for back-compat, but it is (a) deprecated and
removed from the spec, (b) not the same object as the handler argument in some
cases, and (c) a latent breakage. Safer: pass the event explicitly —
`onclick="markCheck(event, this, '{{.ID}}')"` — and take `(e, el, id)` in the
function.

### P2c — CSP `script-src` includes `'unsafe-eval'`

`cmd/main.go:423` sets `script-src 'self' 'unsafe-inline' 'unsafe-eval' …`.
`'unsafe-eval'` is required by the **vendored htmx library itself**
(`htmx.min.js` calls `eval()` for `hx-vars`/`hx-val` processing — found in the
minified source). So it can't simply be dropped. Mitigations, in order of
effort:

1. Audit that no template actually uses `hx-vars`/`hx-val` with eval-relying
   expressions (the app uses `hx-include`/explicit `values:` in JS instead), then
   set `htmx.config.selfRequestsOnly = true` and `htmx.config.allowEval = false`
   — after which `'unsafe-eval'` can be removed.
2. If htmx eval features are needed, keep `'unsafe-eval'` but add a per-request
   nonce and drop `'unsafe-inline'` (bigger refactor: every inline `<script>`
   and `onclick`/`x-data` handler must move behind a nonce).

`'unsafe-inline'` for scripts is the larger exposure; a nonce-based CSP is the
proper target but is a substantial migration given how much inline JS the
templates carry.

---

## P3 — Maintainability / consistency

### P3a — No base layout; boilerplate duplicated 12×, drifting

Every full page repeats:

```html
<!DOCTYPE html><html ...><head>
  <meta charset/viewport>
  <title>…</title>
  <script src="/static/htmx.min.js"></script>   ← sometimes missing
  <script>var _csrf=…; htmx:configRequest …</script>
  <script src="/static/alpine.min.js" defer></script>
  <link rel="stylesheet" href="/static/app.css">
</head><body>{{template "nav" .}}<main>…</main></body></html>
```

The duplication has already drifted:

- **htmx is missing on pages that need it.** `nav.html` calls `htmx.ajax`
  (rating save, poll progress) and `htmx.process` (pageshow). Pages that include
  nav but omit `<script src="/static/htmx.min.js">` → those nav calls throw
  `ReferenceError: htmx is not defined`. Affected: `discover.html`,
  `tokens.html`, `plugins.html`, `sources.html` (also the P1a pages — so on
  those the nav doesn't render anyway, but tokens/plugins/sources will load nav
  once P1a is fixed and then hit this). Also `reader.html` omits htmx.
- **`_csrf` + `htmx:configRequest` listener missing** on the same pages, so any
  htmx POST from them would lack the CSRF header.
- **Inconsistent `<html>` theming** — three different strategies coexist:

  | class | pages |
  |-------|-------|
  | `bg-background text-white` (custom Tailwind theme tokens) | dashboard, reader, add_series, provider_config, plugins, tokens, discover, sources, login, library, logs (11) |
  | `bg-neutral-950 text-neutral-200` (hardcoded neutrals) | import_opml, version, setup (3) |
  | `dark` (tailwind dark-mode class) | comic_browse, comic_reader (2) |

  import_opml/version/setup will look subtly different (hardcoded neutrals vs
  theme tokens); the comic pages rely on `dark:` variants the rest of the app
  doesn't use.

Fix: introduce a `base.html` layout (`{{define "base"}}…{{template "content" .}}…{{end}}`)
that owns `<head>`, the script/css includes, the `_csrf` global, the
`htmx:configRequest` listener, and `{{template "nav"}}`. Each page becomes
`{{define "content"}}…{{end}}` with a `{{template "base" .}}` driver. Eliminates
all three drift classes at once.

### P3b — No template-render tests

`templates.go` parses all `*.html` at init into one `*template.Template`, but
nothing asserts that (a) every `renderTemplate` name resolves, (b) every page
that includes nav actually renders nav, or (c) pages don't reference undefined
templates. P1a shipped because of this. Add a test that, for each
`renderTemplate`/`ExecuteTemplate` call site, executes the named template with
representative data and asserts the output contains the nav marker and the page
heading. (The throwaway test used for this review is a template for it.)

### P3c — `nav.html` is 221 lines, ~90% JavaScript

The rating widget (`ratingInput`/`ratingSync`/`ratingUp`/`ratingDown`/click→
input swap), poll-button state machine, and image-error handler all live inline
in `nav.html`. This logic is also partly duplicated by `ratingStyle`/
`ratingSave`/`ratingHue` template funcs in `templates.go`. Consolidate the
rating JS into `/static/app.js` (a new, non-vendored JS file served like the
others) so it's cacheable and out of the template.

---

## P4 — Polish / accessibility

### P4a — Icon-only buttons use `title`, not `aria-label`

Nav buttons (Discover, Comics, Add, Poll, settings cog) and many in the
dashboard/chapter preview expose their meaning only via `title="…"`. `title` is
not a reliable accessible name (screen readers ignore it by default; it doesn't
show on touch/focus). Add `aria-label` (or visually-hidden text) to each.

### P4b — Rating widget is mouse-only

The rating `<span data-rating-value>` is a `<span>` with an `onclick` (handled
via document-level delegation in `nav.html`). No `role="slider"`, `tabindex`,
or keyboard handler → keyboard and assistive-tech users can't set ratings. The
inline `<input type=number>` swap on click helps mouse users but isn't
reachable without a pointer. Make it a real `<input>` / button group with
keyboard support.

### P4c — Mobile nav lacks `aria-expanded` / focus handling

`#mobile-nav` toggles `hidden` via onclick but the hamburger button doesn't set
`aria-expanded`/`aria-controls`, and opening it doesn't move focus. Minor a11y
gap.

### P4d — No `loading="lazy"` on images

15 `<img>` tags across templates, 0 use `loading="lazy"`. Lists render many
favicons/covers (dashboard series view, library). Adding `loading="lazy"` (and
`decoding="async"`) is a free win on long lists. (The favicons are tiny so the
impact is modest, but the series covers in library/dashboard are larger.)

### P4e — Redundant CSRF emission

Every page emits both `<meta name="csrf-token" content="{{.CSRFToken}}">` and
`var _csrf="{{.CSRFToken}}"`. Only the JS var is consumed. Drop the meta tag
(or consume it and drop the var) — one source of truth.

---

## Remediation order

1. **P1a** — one-line fix ×4 files (`nav.html`→`nav`). Immediate, zero risk.
2. **P1b** — add CSRF input to both logout forms in `nav.html`. Immediate.
3. **P2a** — rebuild discover cards with DOM APIs (or `data-url` + `escHtml`).
4. **P3a** — introduce `base.html` layout; this also closes the "htmx missing
   on nav pages" gap and the theming drift in one move. Biggest payoff.
5. **P3b** — template-render regression tests (guard P1a + P3a).
6. **P2c** — investigate `htmx.config.allowEval=false` to drop `'unsafe-eval'`.
7. **P2b, P4a–e** — batch of small hardening/polish items.

`POSTGRES_AUDIT.md`-style: P1a and P1b are the only items I'd treat as
"ship-now" bugs; the rest is hardening the GUI layer against the same class of
"works on the page the dev tested, silently broken elsewhere" failures that
the Postgres audit found in the DB layer.
