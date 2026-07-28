# Discovery Feature — Implementation Plan

## Overview
Add provider-powered discovery/search to both web UI and iOS app. Each text provider implements its own search. Results preview metadata + chapter list. "Download All" button bulk-archives to local storage.

---

## Phase 1: Plugin Capability — `SeriesSearcher`

### New interface (`internal/plugin/registry.go`)
```go
type SeriesSearchResult struct {
    Title       string
    SourceURL   string   // canonical URL to pass to SeriesLister
    Author      string
    Summary     string
    ImageURL    string
    Rating      float64
    Status      string   // "Ongoing", "Completed", etc.
    Tags        []string
    UpdatedAt   string   // human-readable
}

type SeriesSearcher interface {
    Search(query string, page int) ([]SeriesSearchResult, error)
}
```

### Implementations (per provider)
| Provider | Search URL | Method | Difficulty |
|---|---|---|---|
| royalroad | `/api/rrserver/api/search` or scrape `/search?term=` | JSON API or HTML | Medium |
| ao3 | `/works/search?work_search[query]=` | HTML scrape | Medium |
| spacebattles | `/search/search?keywords=&type=thread` | HTML scrape | Medium |
| sufficientvelocity | same as SB (XenForo) | HTML scrape | Easy (shared code) |
| questionablequesting | same (XenForo, needs auth) | HTML scrape | Hard (auth) |
| fanfictionnet | `/search.php` | FlareSolverr | Hard (CF) |
| dreamytranslations | `/search?q=` or browse page | HTML scrape | Easy |

Start with: **royalroad, ao3, spacebattles** (highest traffic, most useful).

---

## Phase 2: API Endpoints (`internal/api/v1/server.go`)

```
GET  /api/v1/discover/providers        → list of providers with SeriesSearcher capability
GET  /api/v1/discover/search           → ?provider=royalroad&q=query&page=1
                                         → { results: [...], has_next: bool }
```

DTO in `dto.go`:
```go
type discoverResult struct {
    Title     string `json:"title"`
    SourceURL string `json:"source_url"`
    Author    string `json:"author"`
    Summary   string `json:"summary"`
    ImageURL  string `json:"image_url"`
    Rating    float64 `json:"rating"`
    Status    string `json:"status"`
    Tags      []string `json:"tags"`
}
```

---

## Phase 3: Web UI

### New files
- `internal/handlers/discover_handlers.go` — `DiscoverPage`, `DiscoverSearchAPI`
- `internal/handlers/templates/discover.html` — provider tabs, search, results grid

### Changes
- `nav.html` — add "Discover" link between Dashboard and Library
- `handlers.go` — register `/discover` route + `/api/discover/search`

### UX
- Provider chip row (badges with favicons)
- Search input (HTMX, hx-get on submit)
- Results grid: cover image, title, author, rating, summary, tags
- Click result → modal with full metadata + "Add to Library" button
- After adding → link to series detail page

---

## Phase 4: iOS App

### New files
- `Sources/WebFiction/Features/Discover/DiscoverView.swift`
- `Sources/WebFiction/Features/Discover/DiscoverViewModel.swift`

### Changes
- `RootView.swift` — add Discovery tab (between Library and Feed)
- `APIClient.swift` — add `discoverProviders()` + `discoverSearch(provider:q:page:)` methods
- `APIClient.swift` — add `DiscoverResult` model

### UX
- Provider picker (horizontal scroll chips)
- Search bar
- Results: LazyVStack of cards (cover, title, author, rating, summary)
- Tap → sheet/navigation with full detail + "Add to Library" + "Download All"

---

## Phase 5: Bulk Download ("Download All" on TOC)

### Server-side archival (web UI)
- `POST /api/v1/library/{id}/archive` — triggers background archival via download.Tracker
- `GET /api/v1/library/{id}/archive/status` — progress
- Worker pool fetches all chapters' content via ContentFetcher, stores in DB

### iOS (DownloadManager)
- `func downloadAllChapters(api, seriesID, chapters: [Chapter])` 
- Loops through chapters, calls ContentManager.getContent for each
- Updates progress: `@Published var bulkProgress: [Int: Double]` (seriesID → fraction)
- Results persist via ChapterCache
- Discovery detail "Download All" → calls this → progress bar → done notification

---

## Implementation Order

1. ✅ Define `SeriesSearcher` interface + `SeriesSearchResult` type
2. ✅ Implement RoyalRoad search
3. ✅ Implement AO3 search  
4. ✅ Implement SpaceBattles/SV (XenForo) search
5. ✅ API endpoints: `/api/v1/discover/providers` + `/api/v1/discover/search`
6. ✅ Web UI: discover page + handler + nav link
7. ✅ iOS: APIClient methods + DiscoveryView tab
8. ✅ iOS: bulk download in DownloadManager
9. ✅ iOS: download button in series detail / discovery detail
10. ✅ Web UI: bulk archive endpoint + button

---

## Architecture Decisions

- **Server-side search**: All provider searches happen on the server (residential IP, no Cloudflare on most). iOS never talks to providers directly for discovery.
- **Reuse existing patterns**: `Searcher` (comics) pattern + `ComicBrowseAPI` handler as template.
- **No new DB tables**: Discovery is ephemeral (search → results → add by URL). Once added, existing series/chapter flow handles everything.
- **Bulk download**: iOS uses existing `ContentManager` chain (local→server→direct fetch). Server uses `download.Tracker` for background archival on web UI.
