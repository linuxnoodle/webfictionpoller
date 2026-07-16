package handlers

import (
	"archive/zip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/linuxnoodle/webfictionpoller/internal/comics"
	"github.com/linuxnoodle/webfictionpoller/internal/download"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

var comicProviders = map[string]comics.ComicProvider{}

func RegisterComicProvider(p comics.ComicProvider) {
	comicProviders[p.Name()] = p
}

// comicProviderByName looks up a comic provider by name. It returns (nil, false)
// when the provider is unknown — there is deliberately NO fallback to "pick any"
// provider, because with multiple comic providers registered that behaviour
// would silently attribute a series to the wrong source.
//
// Callers should surface a clear error when this returns false rather than
// guessing. main.go populates comicProviders from plugin.Default at startup,
// so the registry is the source of truth.
func comicProviderByName(name string) (comics.ComicProvider, bool) {
	p, ok := comicProviders[name]
	return p, ok
}

// LookupComicProvider is the exported form of comicProviderByName for callers
// outside the handlers package (scheduler, future API layer).
func LookupComicProvider(name string) (comics.ComicProvider, bool) {
	return comicProviderByName(name)
}

// resolveComicProvider returns the provider for a series or an error explaining
// why it could not be resolved. Replaces the old "pick any registered provider"
// fallback, which silently misattributed series to the wrong source when more
// than one comic provider was registered.
func resolveComicProvider(series *comics.ComicSeries) (comics.ComicProvider, error) {
	if series == nil {
		return nil, errors.New("series is nil")
	}
	p, ok := comicProviderByName(series.ProviderName)
	if !ok || p == nil {
		return nil, fmt.Errorf("comic provider %q not registered", series.ProviderName)
	}
	return p, nil
}

func (h *Handler) ComicBrowsePage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, r, "comic_browse", map[string]interface{}{
		"Page": "comics",
	})
}

func (h *Handler) ComicSearchAPI(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	provider := r.URL.Query().Get("provider")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if query == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"mangas": []interface{}{}})
		return
	}

	p, ok := comicProviderByName(provider)
	if !ok {
		// No provider selected. If exactly one is registered, use it; otherwise
		// surface the choice rather than silently searching the wrong catalog.
		if len(comicProviders) == 1 {
			for _, single := range comicProviders {
				p = single
			}
		} else {
			names := make([]string, 0, len(comicProviders))
			for n := range comicProviders {
				names = append(names, n)
			}
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error":    "provider parameter required",
				"providers": names,
			})
			return
		}
	}
	if p == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"mangas": []interface{}{}})
		return
	}

	results, err := p.SearchManga(query, page)
	if err != nil {
		logging.Error("[comic] search error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (h *Handler) ComicAddSeriesAPI(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceID     string `json:"source_id"`
		ProviderName string `json:"provider_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid request"})
		return
	}

	p, ok := comicProviderByName(req.ProviderName)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "unknown provider"})
		return
	}

	existing, err := h.store.GetComicSeriesBySourceURL("https://mangadex.org/title/" + req.SourceID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if existing != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"id": existing.ID, "status": "already_exists"})
		return
	}

	details, err := p.MangaDetails(req.SourceID)
	if err != nil {
		internalError(w, r, err)
		return
	}

	id, err := h.store.AddComicSeries(*details)
	if err != nil {
		internalError(w, r, err)
		return
	}

	chapters, err := p.ChapterList(req.SourceID)
	if err != nil {
		logging.Error("[comic] chapter list error for %s: %v", req.SourceID, err)
	} else if len(chapters) > 0 {
		inserted, err := h.store.UpsertComicChapters(id, chapters)
		if err != nil {
			logging.Error("[comic] insert chapters error: %v", err)
		}
		logging.Info("[comic] added %q (%s) with %d/%d chapters", details.Title, req.SourceID, inserted, len(chapters))
	} else {
		logging.Info("[comic] added %q (%s) with 0 chapters", details.Title, req.SourceID)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"id": id, "status": "added", "title": details.Title})
}

func (h *Handler) ComicLibraryAPI(w http.ResponseWriter, r *http.Request) {
	series, err := h.store.ListComicSeries()
	if err != nil {
		internalError(w, r, err)
		return
	}

	type seriesWithStats struct {
		comics.ComicSeries
		TotalChapters      int `json:"total_chapters"`
		ReadChapters       int `json:"read_chapters"`
		DownloadedChapters int `json:"downloaded_chapters"`
	}

	var result []seriesWithStats
	for _, s := range series {
		total, read, downloaded, _ := h.store.GetComicChapterCounts(s.ID)
		result = append(result, seriesWithStats{
			ComicSeries:        s,
			TotalChapters:      total,
			ReadChapters:       read,
			DownloadedChapters: downloaded,
		})
	}

	if result == nil {
		result = []seriesWithStats{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"series": result})
}

func (h *Handler) ComicSeriesDetailAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}

	series, err := h.store.GetComicSeriesByID(id)
	if err != nil || series == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "not found"})
		return
	}

	chapters, err := h.store.GetComicChapters(id)
	if err != nil {
		internalError(w, r, err)
		return
	}

	progressChapter, progressPage, _ := h.store.GetComicReadingProgress(id)

	if chapters == nil {
		chapters = []comics.ComicChapter{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"series":           series,
		"chapters":         chapters,
		"progress_chapter": progressChapter,
		"progress_page":    progressPage,
	})
}

func (h *Handler) ComicReaderPage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	renderTemplate(w, r, "comic_reader", map[string]interface{}{
		"Page":      "comics",
		"ChapterID": id,
	})
}

func (h *Handler) ComicChapterPagesAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}

	chapter, err := h.store.GetComicChapterByID(id)
	if err != nil || chapter == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "chapter not found"})
		return
	}

	pages, err := h.store.GetComicChapterPages(id)
	if err != nil {
		internalError(w, r, err)
		return
	}

	if len(pages) > 0 && pages[0].ImageURL != "" {
		type pageInfo struct {
			Index int    `json:"index"`
			URL   string `json:"url"`
		}
		var ps []pageInfo
		for _, p := range pages {
			ps = append(ps, pageInfo{Index: p.Index, URL: "/comics/page/" + strconv.FormatInt(id, 10) + "/" + strconv.Itoa(p.Index)})
		}
		if ps == nil {
			ps = []pageInfo{}
		}

		prev, next, _ := h.store.GetAdjacentComicChapterIDs(id)

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"chapter":      chapter,
			"pages":        ps,
			"prev_chapter": prev,
			"next_chapter": next,
		})
		return
	}

	var p comics.ComicProvider
	series, _ := h.store.GetComicSeriesByID(chapter.SeriesID)
	if series != nil {
		p, _ = comicProviderByName(series.ProviderName)
	}
	if p == nil {
		name := ""
		if series != nil {
			name = series.ProviderName
		}
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": fmt.Sprintf("comic provider %q not registered", name)})
		return
	}

	if p == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "no provider"})
		return
	}

	providerPages, err := p.PageList(chapter.SourceID)
	if err != nil {
		internalError(w, r, err)
		return
	}

	type pageInfo struct {
		Index int    `json:"index"`
		URL   string `json:"url"`
	}
	var ps []pageInfo
	for _, pg := range providerPages {
		h.store.SaveComicPage(id, pg.Index, pg.ImageURL, nil, "")
		ps = append(ps, pageInfo{Index: pg.Index, URL: "/comics/page/" + strconv.FormatInt(id, 10) + "/" + strconv.Itoa(pg.Index)})
	}
	if ps == nil {
		ps = []pageInfo{}
	}

	prev, next, _ := h.store.GetAdjacentComicChapterIDs(id)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"chapter":      chapter,
		"pages":        ps,
		"prev_chapter": prev,
		"next_chapter": next,
	})
}

func (h *Handler) ComicServePage(w http.ResponseWriter, r *http.Request) {
	chapterID, err := strconv.ParseInt(chi.URLParam(r, "chapterId"), 10, 64)
	if err != nil {
		http.Error(w, "invalid chapter id", http.StatusBadRequest)
		return
	}
	pageIndex, err := strconv.Atoi(chi.URLParam(r, "pageIndex"))
	if err != nil {
		http.Error(w, "invalid page index", http.StatusBadRequest)
		return
	}

	data, contentType, err := h.store.GetComicPageData(chapterID, pageIndex)
	if err != nil {
		internalError(w, r, err)
		return
	}

	if data != nil && len(data) > 0 {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data)
		return
	}

	chapter, err := h.store.GetComicChapterByID(chapterID)
	if err != nil || chapter == nil {
		http.Error(w, "chapter not found", http.StatusNotFound)
		return
	}

	pages, err := h.store.GetComicChapterPages(chapterID)
	if err != nil {
		internalError(w, r, err)
		return
	}

	var targetURL string
	for _, pg := range pages {
		if pg.Index == pageIndex {
			targetURL = pg.ImageURL
			break
		}
	}

	if pages == nil || len(pages) == 0 {
		var series *comics.ComicSeries
		series, _ = h.store.GetComicSeriesByID(chapter.SeriesID)
		var provider comics.ComicProvider
		if series != nil {
			provider, _ = comicProviderByName(series.ProviderName)
		}
		if provider == nil {
			name := ""
			if series != nil {
				name = series.ProviderName
			}
			logging.Error("[comic] cannot serve page %d/%d: provider %q not registered", chapterID, pageIndex, name)
			http.Error(w, "provider not registered", http.StatusInternalServerError)
			return
		}
		if provider != nil {
			providerPages, perr := provider.PageList(chapter.SourceID)
			if perr == nil {
				for _, pg := range providerPages {
					h.store.SaveComicPage(chapterID, pg.Index, pg.ImageURL, nil, "")
					if pg.Index == pageIndex {
						targetURL = pg.ImageURL
					}
				}
				pages, _ = h.store.GetComicChapterPages(chapterID)
			}
		}
	}

	if targetURL == "" {
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}

	resp, err := http.Get(targetURL)
	if err != nil {
		http.Error(w, "failed to fetch image", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	imgData, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}

	go func() {
		if err := h.store.SaveComicPage(chapterID, pageIndex, targetURL, imgData, ct); err != nil {
			logging.Error("[comic] failed to cache page %d/%d: %v", chapterID, pageIndex, err)
		}
		allCached := true
		for _, pg := range pages {
			if pg.Index == pageIndex {
				continue
			}
			d, _, derr := h.store.GetComicPageData(chapterID, pg.Index)
			if derr != nil || d == nil {
				allCached = false
				break
			}
		}
		if allCached {
			h.store.MarkComicChapterDownloaded(chapterID)
		}
	}()

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(imgData)
}

func (h *Handler) ComicMarkReadAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}
	if err := h.store.MarkComicChapterRead(id); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (h *Handler) ComicSaveProgressAPI(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SeriesID  int64 `json:"series_id"`
		ChapterID int64 `json:"chapter_id"`
		PageIndex int   `json:"page_index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid request"})
		return
	}
	if err := h.store.SaveComicReadingProgress(req.SeriesID, req.ChapterID, req.PageIndex); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (h *Handler) ComicDeleteSeriesAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}
	if err := h.store.DeleteComicSeries(id); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (h *Handler) ComicMarkAllReadAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}
	if err := h.store.MarkComicAllRead(id); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (h *Handler) ComicRefreshChaptersAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}

	series, err := h.store.GetComicSeriesByID(id)
	if err != nil || series == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "not found"})
		return
	}

	p, ok := comicProviderByName(series.ProviderName)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "provider not available"})
		return
	}

	n, err := h.store.RefreshComicChapters(id, p, series.SourceID)
	if err != nil {
		internalError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"added": n})
}

func (h *Handler) ComicUpdateRatingAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}
	var req struct {
		Rating float64 `json:"rating"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid request"})
		return
	}
	if err := h.store.UpdateComicSeriesRating(id, req.Rating); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (h *Handler) ComicUpdateStatusAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid request"})
		return
	}
	if err := h.store.UpdateComicSeriesStatus(id, req.Status); err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// comicDownloadKey is the tracker key namespace for comic chapter downloads.
// Kept stable so a client can poll status by chapter id alone.
func comicDownloadKey(chapterID int64) string {
	return fmt.Sprintf("comic-chapter:%d", chapterID)
}

func (h *Handler) ComicDownloadChapterAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}

	chapter, err := h.store.GetComicChapterByID(id)
	if err != nil || chapter == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "chapter not found"})
		return
	}

	series, _ := h.store.GetComicSeriesByID(chapter.SeriesID)
	provider, err := resolveComicProvider(series)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}

	store := h.store
	status := h.downloads.Start(comicDownloadKey(id), 0, func(ctx context.Context, rep download.Reporter) error {
		// Defer page enumeration until inside the job so the total reflects the
		// upstream response (page lists can change between requests).
		pages, err := provider.PageList(chapter.SourceID)
		if err != nil {
			return fmt.Errorf("fetching page list: %w", err)
		}
		rep.SetTotal(len(pages))
		if len(pages) == 0 {
			return errors.New("upstream returned zero pages")
		}

		for _, pg := range pages {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Skip pages already in the blob store — resume after interruption.
			if store.ComicPageBlobStored(ctx, id, pg.Index) {
				rep.Inc()
				continue
			}
			if err := fetchAndCacheComicPage(ctx, store, provider, id, pg); err != nil {
				logging.Error("[comic] download page %d of chapter %d: %v", pg.Index, id, err)
				// Continue rather than abort: partial downloads are useful.
				continue
			}
			rep.Inc()
		}
		// Mark downloaded only if every page is now cached.
		allCached := true
		for _, pg := range pages {
			if !store.ComicPageBlobStored(ctx, id, pg.Index) {
				allCached = false
				break
			}
		}
		if allCached {
			if err := store.MarkComicChapterDownloaded(id); err != nil {
				logging.Error("[comic] mark downloaded %d: %v", id, err)
			}
		}
		return nil
	})

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"ok":     true,
		"key":    comicDownloadKey(id),
		"status": status,
	})
}

// ComicDownloadStatusAPI returns the current progress of a chapter download.
// GET /api/comics/chapter/{id}/download/status
func (h *Handler) ComicDownloadStatusAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}
	status, ok := h.downloads.Status(comicDownloadKey(id))
	if !ok {
		// No job recorded. Report based on the chapter's stored flag so the
		// client gets a sensible answer on cold-start poll.
		chapter, _ := h.store.GetComicChapterByID(id)
		state := download.StateFailed
		if chapter != nil && chapter.Downloaded {
			state = download.StateComplete
		}
		status = download.Status{Key: comicDownloadKey(id), State: state}
	}
	writeJSON(w, http.StatusOK, status)
}

// ComicDownloadCancelAPI cancels a running chapter download.
// POST /api/comics/chapter/{id}/download/cancel
func (h *Handler) ComicDownloadCancelAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}
	h.downloads.Cancel(comicDownloadKey(id))
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"ok": true})
}

// ComicChapterCBZAPI streams a CBZ (zip of page images) for offline reading.
// GET /api/comics/chapter/{id}/cbz
//
// Pages are read from the blob store on the fly; the response is streamed so
// memory use stays flat regardless of chapter size. Returns 404 if no pages
// are cached locally.
func (h *Handler) ComicChapterCBZAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}
	chapter, err := h.store.GetComicChapterByID(id)
	if err != nil || chapter == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "chapter not found"})
		return
	}
	pages, err := h.store.GetComicChapterPages(id)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if len(pages) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "no pages cached; download the chapter first"})
		return
	}

	// Build a sensible filename. Use chapter number when present, fall back to id.
	filename := fmt.Sprintf("chapter-%d.cbz", id)
	if chapter.ChapterNum != "" {
		filename = fmt.Sprintf("chapter-%s.cbz", sanitizeForFilename(chapter.ChapterNum))
	}

	w.Header().Set("Content-Type", "application/vnd.comicbook+zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	// Streaming zip — no Content-Length, client shows indeterminate progress.

	zw := zip.NewWriter(w)
	ctx := r.Context()
	for _, pg := range pages {
		if ctx.Err() != nil {
			return
		}
		ext := extensionForPageIndex(pg.Index)
		name := fmt.Sprintf("%05d.%s", pg.Index, ext)
		fw, err := zw.Create(name)
		if err != nil {
			logging.Error("[comic] cbz create %q: %v", name, err)
			return
		}
		rc, _, err := h.store.GetComicPageReader(ctx, id, pg.Index)
		if err != nil {
			logging.Error("[comic] cbz read page %d: %v", pg.Index, err)
			return
		}
		if _, err := io.Copy(fw, rc); err != nil {
			rc.Close()
			logging.Error("[comic] cbz copy page %d: %v", pg.Index, err)
			return
		}
		rc.Close()
	}
	// Include a ComicInfo.xml metadata file so readers display title/series.
	if fw, err := zw.Create("ComicInfo.xml"); err == nil {
		writeComicInfoXML(fw, chapter, pages)
	}
	if err := zw.Close(); err != nil {
		logging.Error("[comic] cbz close: %v", err)
	}
}

// fetchAndCacheComicPage downloads one page from upstream and writes it to
// the store (blob + metadata).
func fetchAndCacheComicPage(ctx context.Context, store *Store, provider comics.ComicProvider, chapterID int64, pg models.ComicPage) error {
	req, err := http.NewRequestWithContext(ctx, "GET", pg.ImageURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20))
	if err != nil {
		return err
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	return store.SaveComicPage(chapterID, pg.Index, pg.ImageURL, data, ct)
}

func writeComicInfoXML(w io.Writer, chapter *models.ComicChapter, pages []models.ComicPage) {
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<ComicInfo xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">
  <PageCount>%d</PageCount>
  <Number>%s</Number>
</ComicInfo>
`, len(pages), escapeXML(chapter.ChapterNum))
}

func escapeXML(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "chapter"
	}
	return b.String()
}

func extensionForPageIndex(idx int) string {
	_ = idx
	// We don't store per-page extension yet; default to jpg. Future: read from
	// comic_pages.content_type and map.
	return "jpg"
}
