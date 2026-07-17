# Dreamy Translations — Sync Plugin Plan

Plugin for `dreamy-translations.com`. **Plan only — no code yet.**

## Site analysis

### URL shapes

| Page | URL | Notes |
|---|---|---|
| Story | `https://dreamy-translations.com/novel/{slug}` | slug is short alphanumeric (`imc`, `rev`, `saaft`) |
| Chapter | `https://dreamy-translations.com/novel/{slug}/chapter/{N}` | N is integer, 0-indexed for some novels (prologue) and 1-indexed for others |
| Cover | `https://supabase.dreamy-translations.com/storage/v1/object/public/covers/{id}/cover.jpg` | Supabase storage bucket |

### Content layout (verified from live fetches)

**Story page contains everything needed on a single HTML response:**

- `<h1>` → title (e.g. "Be Careful When Installing Mods")
- `by {author}` text → author (e.g. `네뮤`)
- Synopsis paragraph after the tags
- `Start Reading` link → `/novel/{slug}/chapter/{firstN}` (tells us the starting index)
- `N Chapters` + `N Free` + `N Premium` counts
- Full chapter list with per-chapter: `Ch. N` + title + word count + URL
- Some chapters marked Premium (paywalled; readable only with a Pass)

**Chapter page:**

- Title at top (`0. Prologue`, `1. Tutorial (1)`, etc.)
- Body content as paragraphs inside `<article>`
- Previous / Next navigation links
- "Table of Contents" link back to story

### Cloudflare

The site is behind Cloudflare (`cdn-cgi/` URLs in the response). Empirically a
plain `GET` with a realistic browser User-Agent (which `safefetch` already
sends) **returns real HTML, not a challenge page**. FlareSolverr is **not
required** in practice — but the plugin must detect a challenge response
(HTTP 403 with the Cloudflare interstitial) and surface a clear error so the
user can wire FlareSolverr if their IP gets flagged later.

### Backend stack

- Next.js frontend (SSR — content is in the initial HTML, no JS execution needed)
- **Supabase backend** confirmed via cover image host. REST endpoint at
  `https://supabase.dreamy-translations.com/rest/v1/` requires the anon
  API key; key extracted from a JS chunk is
  `eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJpc3MiOiJzdXBhYmFzZSIsImlhdCI6MTc2NTQ2ODUwMCwiZXhwIjo0OTIxMTQyMTAwLCJyb2xlIjoiYW5vbiJ9.RpZtGEq3Pik5K07fANkOcXUthOFB83MvN97L_yzpoQk`
  (expires 2125, anon role). Authenticated calls succeed but the obvious
  table names (`novels`, `chapters`) don't exist — site uses different
  names that the minified JS accesses via computed/template strings.
- **Status:** API is reachable; schema discovery needs more work (try the
  PostgREST OpenAPI root at `/rest/v1/` or grep a sourcemap). Plan
  defaults to HTML scraping; Supabase is a stretch goal for phase 2.

## Architecture decision: compiled-in Go provider

**NOT a declarative TOML provider.** Reasons:

1. The user's XPaths (`/html/body/div[5]/main/article/div`) are purely
   positional — no stable CSS classes to anchor a TOML selector on.
2. Chapter discovery requires parsing a paginated chapter list, detecting
   Free vs Premium, and finding the starting chapter index from the
   `Start Reading` link. Too much logic for TOML.
3. Potential Supabase REST shortcut needs custom code.
4. Premium-chapter filtering needs per-row decisions.

The plugin lives at `internal/provider/text/dreamy/` as a new Go package,
self-registers via `init()` into `plugin.Default` (no other wiring needed).

## Capability surface

Implements `plugin.Provider` base + four capabilities:

| Capability | Methods | Implementation |
|---|---|---|
| base | `Meta()`, `MatchURL()` | trivial |
| `SeriesLister` | `FetchSeriesMetadata(url)` | scrape story page |
| `Poller` (update detection) | `PollUpdates(series)` | scrape chapter list on story page; returns `[]models.Chapter` |
| `ContentFetcher` (chapter sync) | `FetchChapter(url)` | scrape chapter page; returns canonical `ChapterContent` |
| `CommentFetcher` | `FetchComments(url)` | returns nil (site has no comments) |

Does **not** implement: `Searcher`, `ChapterLister`, `PageLister` (comic
capabilities), `LoginAuth`, `CookieAuth` (no auth required for free content).

### Two-interface contract (required for every plugin)

Every plugin must expose **both** a chapter-update interface and a
chapter-sync interface:

- **Update detection** (`Poller`): returns `[]models.Chapter` listing new
  chapters discovered on the source. Called by the scheduler on the
  provider's polling interval.
- **Chapter sync** (`ContentFetcher`): downloads a single chapter and parses
  it into the canonical `ChapterContent` shape (title + body + metadata),
  which the storage layer persists. **Does not return raw HTML.**

The plugin is the only code that knows the source format (HTML, JSON API,
markdown). Everything downstream — archiver, reader, OPDS — consumes the
unified `ChapterContent` shape.

### The `ChapterContent` shape

Proposed addition to `internal/plugin/content.go` (affects the whole plugin
system, not just dreamy):

```go
package plugin

// ChapterContent is the canonical parsed-chapter shape every provider
// returns from a content fetch. Plugins convert their source format
// (HTML, JSON, markdown) into this structure. Storage / archiver / reader
// consume only this shape and never touch provider-specific HTML.
type ChapterContent struct {
    Title       string       // stripped of decorations ("Ch. N", word counts)
    BodyHTML    string       // sanitized chapter prose (paragraphs + images)
    BodyText    string       // plain-text rendering; empty -> derived downstream
    PublishedAt time.Time    // zero if unknown
    WordCount   int          // 0 if unknown
    Premium     bool         // true = paywalled, BodyHTML empty
    AuthorNote  string       // separated author's note, if site splits it
    Images      []string     // image URLs in BodyHTML for the archiver to cache
}

// ContentFetcher is the chapter-sync capability: downloads a chapter from
// the source and parses it into the canonical ChapterContent shape that
// gets persisted. This is the "sync to storage" interface — every plugin
// implements it.
type ContentFetcher interface {
    FetchChapter(url string) (ChapterContent, error)
}
```

### Migration path for existing providers

RoyalRoad / AO3 / XenForo / FFN currently implement `HTMLFetcher`
(returns raw string). Three options:

1. **Adapter** — default `ContentFetcher` wrapper calls the provider's
   `HTMLFetcher` and shoves the result into `ChapterContent.BodyHTML`
   (other fields empty). Zero churn to existing providers.
2. **Incremental upgrade** — each provider gains a real `FetchChapter`
   that populates Title/WordCount/PublishedAt. Better data over time.
3. **Both (recommended)** — adapter now so storage layer switches to
   `ContentFetcher` immediately; upgrade providers opportunistically as
   we touch them.

Dreamy implements `ContentFetcher` directly from day one — no legacy path.

## File layout

```
internal/provider/text/dreamy/
├── dreamy.go              Provider struct, Meta, MatchURL, init() registration
├── metadata.go            FetchSeriesMetadata — title, author, synopsis, cover, counts
├── chapters.go            PollUpdates — chapter list parsing, premium filtering
├── content.go             FetchChapterContent — title + body extraction
├── supabase.go            (optional) REST client if API is available; falls back to HTML
├── parse.go               Shared helpers: slug extraction, URL absolutizing, premium detect
└── dreamy_test.go         Fixture-based tests for every parser
```

## Detailed design

### `dreamy.go` — base

```go
package dreamy

type Provider struct {
    client *http.Client       // safefetch-wrapped, browser UA, 30s timeout
    rate   time.Duration      // internal polite delay between requests
}

func init() {
    plugin.Default.Register(&Provider{
        client: safeClient(), // helper that wraps safefetch with browser UA
        rate:   2 * time.Second,
    })
}

func (p *Provider) Meta() plugin.Meta {
    return plugin.Meta{
        Name:                "dreamytranslations",
        DisplayName:         "Dreamy Translations",
        Kind:                plugin.KindText,
        Homepage:            "https://dreamy-translations.com",
        FaviconURL:          "https://dreamy-translations.com/favicon.ico",
        AuthModes:           []plugin.AuthMode{plugin.AuthNone},
        Rate:                plugin.RateSpec{RequestsPerSecond: 0.5, Burst: 1, Concurrency: 1},
        PollIntervalDefault: "1h", // releases are scheduled, hourly is plenty
    }
}

func (p *Provider) MatchURL(rawURL string) bool {
    return plugin.HostMatch(rawURL, "dreamy-translations.com")
}
```

Registration is automatic on app startup — the package gets blank-imported in
`cmd/main.go` alongside the other text providers. No other wiring.

### `parse.go` — slug + URL helpers

```go
var chapterURLRe = regexp.MustCompile(`^/novel/([^/]+)/chapter/(\d+)$`)

// slugFromURL extracts "imc" from "/novel/imc" or "/novel/imc/chapter/0".
func slugFromURL(rawURL string) (string, error) { ... }

// chapterURL builds "/novel/{slug}/chapter/{n}" absolutely.
func chapterURL(slug string, n int) string { ... }

// isPremiumRow sniffs a chapter list row for the Premium marker.
// Implementation TBD on actual HTML class — likely a badge/span text match.
func isPremiumRow(s *goquery.Selection) bool { ... }
```

### `metadata.go` — FetchSeriesMetadata

```go
func (p *Provider) FetchSeriesMetadata(rawURL string) (models.Series, error) {
    slug, err := slugFromURL(rawURL)
    doc := p.fetchDoc(storyURL(slug))   // GET + parse + Cloudflare check
    return models.Series{
        Title:        strings.TrimSpace(doc.Find("h1").First().Text()),
        Author:       parseAuthor(doc),             // text after "by "
        SourceURL:    storyURL(slug),
        ProviderName: "dreamytranslations",
        Summary:      parseSynopsis(doc),
        ImageURL:     parseCoverURL(doc),           // supabase URL
        Status:       "active",
        Rating:       models.UnratedRating,
    }, nil
}
```

Selectors must be **defensive**: prefer semantic tags (`h1`, `main`,
`article`) over the user's positional XPaths. The XPaths they provided
(`/html/body/div[5]/...`) are useful as a fallback discovery path but
will break the moment the site adds a sibling `<div>`. Implementation
should:

1. Try semantic selector first (`h1`, `main h1`, etc.)
2. Fall back to the user's XPath translated to goquery's XPath support
   via `cascadia` or `goquery.Find(xpath)` (goquery supports XPath via
   the `gxpath` package — may need a new dep).
3. If both fail, return a clear parse error.

### `chapters.go` — PollUpdates

Two strategies, attempted in order:

**Strategy A: Supabase REST** (preferred if available)

```go
// GET https://supabase.dreamy-translations.com/rest/v1/chapters?
//     novel_id=eq.{id}&order=number.asc&select=number,title,premium
// Headers: apikey (anonymous key, usually in page source)
```

Returns JSON. Cheap, deterministic, no HTML drift. Worth 30 minutes of
investigation during implementation to extract the anon key from a story
page's `__NEXT_DATA__` or network tab.

**Strategy B: HTML scrape** (fallback, always implemented)

```go
func (p *Provider) PollUpdates(series models.Series) ([]models.Chapter, error) {
    doc := p.fetchDoc(series.SourceURL)
    var chapters []models.Chapter
    doc.Find(`a[href^="/novel/"]`).Each(func(_ int, s *goquery.Selection) {
        href, _ := s.Attr("href")
        m := chapterURLRe.FindStringSubmatch(href)
        if m == nil { return }                  // not a chapter link
        if isPremiumRow(s) { return }           // skip paywalled
        n, _ := strconv.Atoi(m[2])
        chapters = append(chapters, models.Chapter{
            SeriesID: series.ID,
            Title:    cleanTitle(s.Text(), n),  // strip "Ch. N" prefix, word count
            URL:      absURL(href),
            // PublishedAt: not on chapter list — fetch from chapter page
            //              lazily, or leave zero and let the archiver fill it.
        })
    })
    return dedupByURL(chapters), nil
}
```

**Pagination handling:** long novels (>100 chapters) paginate the chapter
list. Implementation must follow pagination links — typically `?page=N` or
infinite scroll. Investigate during implementation; the live fetch showed
`Chapters (1) (2) (3) ... (6)` markers indicating 6 pages for a 258-chapter
novel. The plugin must walk every page.

### `content.go` — FetchChapter (ContentFetcher)

```go
func (p *Provider) FetchChapter(chapterURL string) (plugin.ChapterContent, error) {
    doc := p.fetchDoc(chapterURL)

    // Title: prefer main > h1, fall back to main > button > span > span
    // (positional XPath from the user, used only as a fallback signal).
    titleNode := doc.Find("main h1")
    if titleNode.Length() == 0 {
        titleNode = doc.Find("main button span span").First()
    }
    title := cleanChapterTitle(titleNode.Text())  // strip "Ch. N" prefix

    // Body: main > article. Grab inner HTML.
    bodyHtml, err := doc.Find("main article").First().Html()
    if err != nil || strings.TrimSpace(bodyHtml) == "" {
        // Premium interstitial detection: page renders a "Sign in for Pass"
        // CTA instead of an article element.
        if doc.Find(":contains('Sign in for Pass')").Length() > 0 {
            return plugin.ChapterContent{
                Title:    title,
                Premium:  true,
                SourceURL: chapterURL,
            }, nil
        }
        return plugin.ChapterContent{}, fmt.Errorf("no chapter content found at %s", chapterURL)
    }

    // Collect image URLs for the archiver to cache.
    var images []string
    doc.Find("main article img").Each(func(_ int, s *goquery.Selection) {
        if src, _ := s.Attr("src"); src != "" {
            images = append(images, absURL(src))
        }
    })

    bodyText := htmlToText(bodyHtml)  // strip tags for full-text search

    return plugin.ChapterContent{
        Title:     title,
        BodyHTML:  bodyHtml,
        BodyText:  bodyText,
        WordCount: countWords(bodyText),
        Images:    images,
        SourceURL: chapterURL,
        // PublishedAt: not on chapter page in the captured fixture;
        //              could be scraped from Supabase if the REST shortcut
        //              pans out, else left zero.
    }, nil
}
```

**The plugin owns the parsing.** Storage code receives a fully-formed
`ChapterContent` and persists its fields; it never sees the raw HTML or
knows the site's DOM structure.

**Sanitization**: NOT done in the provider. The archiver/reader pipeline
still runs `bluemonday.UGCPolicy()` on `BodyHTML` downstream, uniformly
across every provider. Provider returns extracted-but-unsanitized HTML;
policy applied once at the storage boundary.

**Premium chapter**: when the body area is missing and the page contains a
Pass upsell, return `ChapterContent{Premium: true}`. Storage marks the
chapter row so the UI can show "locked" and the scheduler doesn't retry
the fetch forever.

### `supabase.go` — optional REST shortcut

```go
type supabaseClient struct {
    baseURL string // https://supabase.dreamy-translations.com/rest/v1
    apiKey  string // anonymous key, extracted from story page HTML
    http    *http.Client
}

func (c *supabaseClient) ListChapters(slug string) ([]chapterRow, error) { ... }
func (c *supabaseClient) GetChapterBody(slug string, n int) (string, error) { ... }
```

If during implementation we find the REST endpoint works without auth (most
Supabase setups allow anonymous read on public tables), this becomes the
primary path and HTML scraping becomes the fallback. The Provider struct
holds both; tries API first, falls back on any error.

## Cloudflare handling

`safefetch.Get` already sends a Chrome User-Agent and uses an SSRF-guarded
transport. Empirically this works for dreamy-translations.com today. The
plugin adds:

1. **Challenge detection**: if the response HTML contains
   `"Just a moment..."` (Cloudflare's challenge title) or the HTTP status
   is 403/503 with `Server: cloudflare`, return a typed error:
   ```go
   var ErrCloudflareChallenge = errors.New("cloudflare challenge — configure FLARESOLVERR_URL")
   ```
2. **FlareSolverr fallback**: if `FLARESOLVERR_URL` is set and a normal GET
   returns `ErrCloudflareChallenge`, retry via FlareSolverr (the existing
   `fanfictionnet` provider has this pattern — copy it).
3. **Backoff**: 429s respected via the shared `providers/http.go` retry
   logic. No special handling in this plugin.

## Rate limit

- `Meta().Rate = {RequestsPerSecond: 0.5, Burst: 1, Concurrency: 1}` — 1
  request every 2 seconds, single concurrent. The worker pool enforces this
  via the shared per-provider rate limiter.
- Internal 2s `rate` field is a defense-in-depth: even if the pool's limiter
  is misconfigured, the provider stays polite.
- Polling interval: 1h (chapter releases are scheduled, not real-time).

## Testing strategy

**Fixture-based, no network.** Every parser tested against captured HTML
snapshots.

```
internal/provider/text/dreamy/testdata/
├── story_imc.html          # full story page for "Be Careful When Installing Mods"
├── story_rev.html          # story page for "I Don't Need A Guillotine..."
├── chapter_imc_0.html      # chapter page (prologue, 0-indexed start)
├── chapter_rev_1.html      # chapter page (1-indexed start)
├── story_rev_p2.html       # paginated chapter list page 2
└── cloudflare_challenge.html
```

Tests:

```go
func TestFetchSeriesMetadata_TitleAuthor(t *testing.T) { ... }
func TestFetchSeriesMetadata_CoverURL(t *testing.T) { ... }
func TestPollUpdates_FindsAllFreeChapters(t *testing.T) {
    // rev has 258 free chapters; assert we get 258, none premium
}
func TestPollUpdates_SkipsPremium(t *testing.T) {
    // imc has 204 free + 20 premium; assert we get 204
}
func TestPollUpdates_Paginates(t *testing.T) {
    // multi-page story; assert we walk every page
}
func TestPollUpdates_Dedup(t *testing.T) {
    // if a chapter URL appears twice on the page, return once
}
func TestFetchChapterContent_ExtractsBody(t *testing.T) { ... }
func TestFetchChapterContent_PremiumReturnsTypedError(t *testing.T) { ... }
func TestFetchSeriesMetadata_HandlesKoreanAuthor(t *testing.T) {
    // 네뮤 — make sure UTF-8 isn't mangled
}
func TestCloudflareDetection(t *testing.T) { ... }
func TestSlugExtraction(t *testing.T) {
    // "/novel/imc", "/novel/imc/chapter/0", "https://.../novel/saaft"
}
```

All tests use `goquery.NewDocumentFromReader(strings.NewReader(fixture))`.
No test ever hits the network.

**Capture process** (one-time, before writing tests):

```bash
curl -A "Mozilla/5.0 ..." https://dreamy-translations.com/novel/imc > testdata/story_imc.html
curl -A "Mozilla/5.0 ..." https://dreamy-translations.com/novel/imc/chapter/0 > testdata/chapter_imc_0.html
# etc.
```

## Implementation phases

| Phase | Task | Est |
|---|---|---|
| 1 | Capture HTML fixtures (5 pages) | 0.25d |
| 2 | Investigate Supabase REST: extract anon key, test `chapters?novel_id=eq.X` endpoint | 0.5d |
| 3 | `dreamy.go` + `parse.go` — base, Meta, MatchURL, slug helpers, init() | 0.25d |
| 4 | `metadata.go` + tests — title/author/synopsis/cover | 0.5d |
| 5 | `chapters.go` + tests — chapter list scrape, premium filter, pagination, dedup | 1.0d |
| 6 | `content.go` + tests — title + body extraction, premium detection | 0.5d |
| 7 | (if phase 2 succeeded) `supabase.go` — REST shortcut path | 0.5d |
| 8 | Cloudflare detection + FlareSolverr fallback | 0.25d |
| 9 | Wire blank import in `cmd/main.go` | 0.1d |
| 10 | Update `registry_integration_test.go` to assert the new provider | 0.1d |
| 11 | Manual smoke test against live site | 0.25d |
| **Total** | | **~4 days** |

## Acceptance criteria

For each example URL the user provided (`rev`, `iwmfasiadf`, `imc`, `saaft`,
`ibonr`):

1. `MatchURL` returns true
2. `FetchSeriesMetadata` returns the correct title, author, cover URL
3. `PollUpdates` returns every free chapter (premium skipped or flagged)
4. `FetchChapterContent` on the lowest-numbered chapter returns non-empty HTML
5. `FetchChapterContent` on the highest-numbered chapter returns non-empty HTML
6. Re-running `PollUpdates` returns the same set (idempotent)

Run the full list through the plugin's smoke test before declaring done.

## Edge cases

| Case | Handling |
|---|---|
| Premium-only novel (all chapters paywalled) | `PollUpdates` returns 0 chapters + log warning; surface in UI |
| Novel with 0 chapters | `PollUpdates` returns empty slice (no error) |
| Chapter N returns 404 | `FetchChapterContent` returns typed `ErrChapterNotFound`; archiver marks it skipped |
| Cloudflare challenge (intermittent) | `ErrCloudflareChallenge`; if FlareSolverr configured, retry through it |
| Site HTML layout changes | Semantic selectors + XPath fallback; tests catch regression on next fixture refresh |
| Chapter with images in body | Pass through; archiver's image extraction (`downloadImages`) handles via `safefetch` |
| Korean / CJK characters in titles | UTF-8 throughout, no special handling |
| Rate-limited (HTTP 429) | Shared retry logic in `providers/http.go` honors `Retry-After` |
| Pagination format changes | Tests against `story_rev_p2.html` fixture catch it |
| Supabase anon key rotates | Re-extract on every `FetchSeriesMetadata` call (cheap); cache per-process with TTL |

## Rollout

1. Land plugin behind the existing `init()` registration pattern.
2. Plugin appears automatically in `/admin/plugins` with capability badges.
3. User adds a dreamy-translations URL via `+` in the nav bar → `MatchURL`
   routes to the new provider → metadata fetched → series added.
4. Scheduler picks it up on next cycle (default 1h for this provider).
5. No DB migration, no schema change, no UI change. Pure additive Go package.

## Cross-cutting prerequisite: `ChapterContent` + `ContentFetcher`

Before the dreamy plugin ships, the plugin system needs the new
`ContentFetcher` interface and `ChapterContent` type. This is a small but
non-trivial change to the registry:

- [ ] New file `internal/plugin/content.go` defining `ChapterContent` +
      `ContentFetcher`.
- [ ] Default adapter in the same file: any provider implementing the
      legacy `HTMLFetcher` automatically satisfies `ContentFetcher` by
      wrapping the returned HTML in `ChapterContent.BodyHTML`.
- [ ] Archiver worker (`internal/worker/archiver.go`) switches to calling
      `ContentFetcher.FetchChapter` when present, falling back to
      `HTMLFetcher.FetchChapterContent` otherwise.
- [ ] Schema migration: add `word_count INTEGER DEFAULT 0`,
      `premium BOOLEAN DEFAULT 0`, and surface `published_at` (already
      present) on the `chapters` table.
- [ ] One test verifying the adapter + one test verifying direct
      `ContentFetcher` implementation.

Estimated extra effort: ~0.5 days. Unlocks every future plugin to return
structured content instead of raw HTML.

## Open questions

1. **Supabase REST schema** — anon key found + auth works, but table names
  are non-obvious (`novels`/`chapters` rejected with `42P01`). Phase 2
  tries the PostgREST OpenAPI root (`/rest/v1/`) or sourcemaps. **Default:**
  HTML scraping; upgrade to API only if schema is recovered cleanly.
2. **Pagination format** — irrelevant for chapter CONTENT (each chapter is
  one URL, one fetch, full body). Matters only for chapter LIST discovery:
  long novels split the chapter list across multiple pages. Plugin walks
  every page during `PollUpdates` so the discovered set is complete.
3. **XPath fallback dep** — not needed. User confirmed XPaths were just
  for identification. CSS selectors against semantic tags (`<main>`,
  `<article>`, `<h1>`) cover every extraction; no `gxpath` dependency.
4. **Storage layer migration** — `ChapterContent` requires a small schema
  addition (word_count, premium, published_at columns on `chapters`) and
  a new `ContentFetcher` interface on the plugin system. This is a
  cross-cutting change, not dreamy-specific; tracked separately below.
