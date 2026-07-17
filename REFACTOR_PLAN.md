# iOS App — Implementation Plan

This document captures the design for the native iOS client that consumes the
`/api/v1/*` surface shipped on the backend. It is a **plan only** — no code
has been written yet.

The backend's refactor (plugin registry, BlobStore, per-device tokens,
Postgres support, multi-source failover) is complete and on `master`; this
plan assumes that surface is stable.

## goals

A first-class native iOS client for webfiction_poller that lets the user:

- Browse their tracked series (text + comics) and chapter feeds
- Read chapters offline (cached HTML for text, cached CBZ pages for comics)
- Trigger downloads and watch progress
- Receive push notifications when new chapters land
- Manage API tokens and per-device credentials
- Survive backgrounding / network changes gracefully

The web UI and the iOS app are equal first-class clients over the same API.

## platform & tooling

| Choice | Decision | Rationale |
|---|---|---|
| Min iOS | 17.0 | SwiftData, Observation framework, modern SwiftUI |
| Language | Swift 5.10+ | Standard |
| UI | SwiftUI + Observation (`@Observable`) | iOS 17 frees us from Combine/ObservableObject boilerplate |
| Persistence | SwiftData (local cache) + UserDefaults (settings) | Native, no external deps |
| Networking | `URLSession` + `async/await` | No third-party HTTP libs |
| Background work | `BGTaskScheduler` (refresh + download completion) | Apple-blessed background execution |
| Push notifications | `UserNotifications` + a server-side push token → api_tokens binding | See §push |
| Concurrency | Swift structured concurrency (`Task`, `TaskGroup`, actors) | Standard |
| Project tool | Xcode 16 + SPM | No CocoaPods |

**Intentionally not adopted:**
- **Third-party DI frameworks** — Swift's `@Environment` + protocol-witness pattern is enough.
- **Core Data** — SwiftData covers the same ground with less ceremony on iOS 17+.
- **Realm / GRDB** — overkill; we cache, not transact.
- **Alamofire** — `URLSession.async` covers our needs.

## architecture

```
webfiction/                          (Xcode project root)
├── App/
│   ├── WebFictionApp.swift          entry point, scene + environment wiring
│   └── RootView.swift               tab container (Library / Feed / Downloads / Settings)
├── Core/
│   ├── Networking/
│   │   ├── APIClient.swift          bearer-token URLSession wrapper
│   │   ├── APIError.swift           structured {error, detail} envelope mapping
│   │   ├── Endpoints.swift          typed URL builders for every /api/v1 route
│   │   └── AuthMiddleware.swift     Authorization header + 401 → re-login flow
│   ├── Models/                      Codable DTOs (mirror of server-side v1 dtos)
│   │   ├── Series.swift             text series + comic series + sources
│   │   ├── Chapter.swift
│   │   ├── ComicChapter.swift
│   │   ├── ProviderInfo.swift
│   │   └── DownloadStatus.swift
│   ├── Storage/
│   │   ├── SwiftDataModels.swift    @Model classes mirroring the DTOs
│   │   ├── ModelCache.swift         SwiftData container + actor-isolated writes
│   │   ├── BlobStore.swift          on-disk page image / EPUB / CBZ cache
│   │   └── KeychainStore.swift      bearer-token storage (Keychain, not UserDefaults)
│   ├── Auth/
│   │   ├── TokenManager.swift       login + token storage + refresh
│   │   └── DeviceID.swift           UIDevice / IDFV-derived stable identifier
│   └── Util/
│       ├── Logger.swift             os.Logger wrapper
│       └── RetryPolicy.swift        exponential backoff for transient failures
├── Features/
│   ├── Library/                     list + detail + add-by-URL
│   ├── Reader/                      text chapter reader (HTML + scroll progress)
│   ├── ComicReader/                 paged image reader (CBZ-style)
│   ├── Feed/                        time-sorted chapter feed
│   ├── Downloads/                   download manager UI
│   ├── Sources/                     multi-source management per series
│   ├── Settings/                    server config, tokens, polling prefs
│   └── Auth/                        login screen
├── Resources/
│   ├── Assets.xcassets              app icon, accent color
│   └── Info.plist                   URL schemes, background modes
└── Tests/
    ├── APIClientTests               stubbed URLSession tests
    ├── ModelCacheTests              SwiftData round-trip
    └── ReaderViewModelTests
```

### Layering rule

Views (`Features/*`) depend only on ViewModels + models. ViewModels depend on
`APIClient` + `ModelCache` via protocols, never on URLSession/SwiftData
directly. This keeps tests fast (no network, no disk) and lets us swap
backends in tests.

## API consumption

Every endpoint listed in the backend's `/api/v1/openapi.json` maps to a typed
Swift function. The full surface:

### Auth + tokens
```swift
APIClient.login(username, password, label, deviceID) -> LoginResponse
APIClient.me() -> User
APIClient.tokens() -> [APIToken]
APIClient.issueToken(label, deviceID) -> (plaintext, APIToken)
APIClient.revokeToken(id) -> Void
```

### Library + chapters
```swift
APIClient.library(kind: .text | .comic) -> [SeriesSummary]
APIClient.seriesDetail(id, kind: ...) -> SeriesDetail
APIClient.chapters(page: Int, unread: Bool) -> [ChapterFeedItem]
APIClient.chapter(id) -> Chapter
APIClient.chapterContent(id) -> ChapterContent  // html + cached flag
APIClient.markChapterRead(id) -> Void
APIClient.unreadCount() -> Int
```

### Polling + metrics
```swift
APIClient.pollStatus() -> PollProgress
APIClient.pollNow() -> PollResult
APIClient.providerMetrics() -> [ProviderMetric]
APIClient.providers() -> [ProviderInfo]
```

### Sources (multi-source failover)
```swift
APIClient.listSources(seriesID) -> [SeriesSource]
APIClient.addSource(seriesID, providerName, sourceURL, priority) -> SeriesSource
APIClient.updateSource(id, priority: Int?, disabled: Bool?) -> Void
APIClient.deleteSource(id) -> Void
APIClient.promoteSource(id) -> Void
```

### Downloads
```swift
APIClient.startComicDownload(chapterID) -> DownloadStatus
APIClient.comicDownloadStatus(chapterID) -> DownloadStatus
APIClient.comicChapterCBZ(chapterID) -> URL   // streamed to disk
```

## auth flow

1. **First launch**: `LoginView` prompts for server URL + username + password.
2. POST `/api/v1/auth/login` with `label` ("iPhone — Alice") and `deviceID`
   (a UUID stored in UserDefaults; stable across launches).
3. Server returns a plaintext bearer token (`wfp_…`) shown once.
4. Token stored in **Keychain** (kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
   — survives reboot, not synced to other devices).
5. Every request adds `Authorization: Bearer <token>`.
6. On 401: present re-login sheet; old token is implicitly dead.
7. Multiple servers supported (server list in Settings); active server is
   a UserDefaults selection.

### Device ID
```swift
let deviceID: UUID = {
    if let stored = UserDefaults.standard.string(forKey: "deviceID").map(UUID.init) {
        return stored
    }
    let fresh = UUID()
    UserDefaults.standard.set(fresh.uuidString, forKey: "deviceID")
    return fresh
}()
```

## data model + cache

### SwiftData `@Model` classes

- `CachedSeries` — id, title, author, sourceURL, providerName, kind, rating,
  status, summary, imageURL, lastSyncedAt
- `CachedChapter` — id, seriesID, title, url, publishedAt, isRead, contentCached
- `CachedComicChapter` — id, seriesID, title, chapterNum, pages, isRead,
  downloaded, cbzPath
- `CachedSource` — id, seriesID, providerName, sourceURL, priority, isPrimary,
  consecutiveFails, disabled

Cache writes go through an actor (`ModelCache`) so background refreshes
don't fight the UI's main-actor viewContext.

### Refresh policy
- On launch: refresh library + feed (parallel `Task`s).
- On pull-to-refresh: explicit refresh.
- Background: `BGAppRefreshTask` every ~30 min (OS may delay/batch —
  best-effort, not realtime).
- On chapter read: optimistic local update + fire-and-forget POST.

### Cache invalidation
- `lastSyncedAt` per series; skip refresh if younger than 5 min.
- Manual "force refresh" via long-press on the library list.

## offline reading

### Text chapters
- Reader requests `/chapters/{id}/content` (returns cached HTML).
- HTML rendered via `WKWebView` with a small CSS shim for serif fonts +
  dark mode. Scroll position is saved locally and reported to
  `/api/v1/progress` (when that endpoint lands — currently the web UI's
  `/api/reader/progress` exists; we'll either reuse it or extend v1).
- **Aggressive cache**: if `content_cached == false`, fetch + store on
  disk (`Library/Caches/chapters/{id}.html`). Subsequent opens hit disk.
- Background prefetch: next 3 unread chapters of the current series.

### Comic chapters
- Two reading modes:
  - **Streaming**: `/comics/page/{chapterId}/{pageIndex}` proxied through
    the server. No pre-download needed; needs network.
  - **Offline CBZ**: `/downloads/comics/{chapterID}/cbz` streamed to disk;
    reader unzips on the fly. Available only after explicit download.
- Comic reader uses `UIPageViewController` with each page rendered as
  `UIImage` (pinch-to-zoom via `UIScrollView`).

### Storage limits
- Per-series cap (default 500 MB) configurable in Settings.
- LRU eviction when cap exceeded (oldest-accessed CBZs removed first).
- Settings: total disk usage, clear-cache button.

## push notifications

**Out of scope for v1 of the app, but design leaves room.**

Future flow:
1. App registers with APNs → gets a device push token.
2. POST `/api/v1/tokens/{id}/push-token` (new endpoint) to bind the token.
3. Server's scheduler, when it inserts new chapters for a series the user
   tracks, fires a push via APNs (silent push → app wakes, refreshes).
4. App fetches the new chapter count and bumps the badge.

The api_tokens table already has `device_id` so the binding is one column
away. The new endpoint and APNs sender are server-side work, not app-side.

## background execution

| Task type | API | Trigger | Action |
|---|---|---|---|
| Refresh | `BGAppRefreshTask` | ~30 min, OS-scheduled | Poll library + feed; badge unread |
| Download completion | `BGProcessingTask` | Charger + WiFi | Finish interrupted CBZ downloads |
| Background image fetch | `URLSession` background config | Per download | Stream CBZ to disk even if app is backgrounded |

iOS kills background tasks aggressively; design must tolerate interruption
(partial CBZ is fine — reader shows "download incomplete" and offers resume).

## error handling

- `APIError` enum: `.unauthorized`, `.rateLimited`, `.server(status, detail)`,
  `.network(URLError)`, `.decoding(Error)`.
- All endpoints throw `APIError`; UI maps to localized messages.
- 401 triggers re-login flow centrally, not per-call.
- 429 / Retry-After honored by `RetryPolicy` (exponential backoff capped at 60s).

## testing strategy

- **APIClientTests**: stub `URLProtocol` to return canned JSON; assert
  endpoint + headers + body shaping.
- **ModelCacheTests**: in-memory SwiftData container; verify round-trip +
  LRU eviction.
- **ReaderViewModelTests**: feed fixture HTML; assert scroll-progress math.
- **Integration smoke test** (disabled by default): real server, real
  login, real chapter fetch — run manually before release.

No UI tests in v1; SnapshotTesting could come later if the design stabilizes.

## build + release

- **Distribution**: TestFlight for beta, App Store for general release.
  Self-hosted users install via TestFlight invite link (no per-user build
  needed; one binary talks to any server URL).
- **Config**: server URL is user-entered; no compile-time defaults. No
  entitlements beyond push (when added).
- **ATS**: server must serve HTTPS in production. Local dev can use the
  simulator's `NSAllowsLocalNetworking` exception.
- **Privacy**: app reads no contacts, no location, no analytics. Privacy
  manifest is trivial.

## open questions

1. **EPUB integration** — do we surface the existing OPDS EPUB feed via
   Apple Books / third-party readers, or render in-app? **Tentative:**
   in-app for consistency; EPUB export can be a Settings action.
2. **Reader progress sync** — the web UI saves scroll position server-side
   via `/api/reader/progress`. Should the iOS app push to the same
   endpoint, or should we add `/api/v1/progress`? **Tentative: add v1
   endpoint** for cleanliness.
3. **Comments** — text chapters have a `/reader/chapter/{id}/comments`
   endpoint (web UI). Do we surface in the app? **Tentative: no for v1**;
   read-only view if added.
4. **Push notifications** — see §push; server-side work needed before the
   app can use them. **Tentative: defer to v1.1.**
5. **Multiple accounts on one server** — web UI is single-user per install.
   App could support multiple profiles (school + personal). **Tentative:
   no for v1**, single active token per server.
6. **iPad / Mac (Designed for iPad)** — SwiftUI scales; min effort to
   support iPad layout. Mac target via Designed for iPad is free. **Tentative:
   ship universal; defer native Mac to v1.1.**

## effort estimate

| Area | Est |
|---|---|
| Project scaffold + SPM + CI | 0.5d |
| APIClient + auth + Keychain | 1.5d |
| SwiftData cache + refresh policy | 1.5d |
| Library + series detail + add-by-URL | 1.5d |
| Text reader (HTML + progress + cache) | 2.0d |
| Comic reader (paged image + CBZ) | 2.0d |
| Feed + unread-count badge | 1.0d |
| Download manager UI | 1.0d |
| Sources management UI | 0.5d |
| Settings + token management | 1.0d |
| Background tasks (refresh + download) | 1.0d |
| Polish (icons, dark mode, accessibility) | 1.0d |
| Tests | 1.5d |
| TestFlight beta setup + App Store metadata | 0.5d |
| **Total** | **~17 days** |

Sequence: scaffold → APIClient/auth → cache → library → reader (text +
comic) → feed → downloads → sources → settings → background → polish → ship.
Each feature is independently demoable after its phase, so the work can be
reviewed incrementally rather than big-bang.

---

**Backend is feature-complete for this app's v1.** The v1 API surface,
OpenAPI spec, bearer-token auth, multi-source failover, BlobStore downloads,
and per-device tokens all shipped on `master`. No server-side work is
required before starting the iOS app, except optionally extending
`/api/v1/progress` (open question 2) and the future push-token binding.
