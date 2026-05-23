package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/linuxnoodle/webfictionpoller/internal/comics"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
)

var comicProviders = map[string]comics.ComicProvider{}

func RegisterComicProvider(p comics.ComicProvider) {
	comicProviders[p.Name()] = p
}

func comicProviderByName(name string) (comics.ComicProvider, bool) {
	p, ok := comicProviders[name]
	return p, ok
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
		for _, p = range comicProviders {
			break
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
		for _, prov := range comicProviders {
			p = prov
			break
		}
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
			for _, p := range comicProviders {
				provider = p
				break
			}
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
	var provider comics.ComicProvider
	if series != nil {
		provider, _ = comicProviderByName(series.ProviderName)
	}
	if provider == nil {
		for _, p := range comicProviders {
			provider = p
			break
		}
	}
	if provider == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "no provider"})
		return
	}

	pages, err := provider.PageList(chapter.SourceID)
	if err != nil {
		internalError(w, r, err)
		return
	}

	go func() {
		for _, pg := range pages {
			existing, _, _ := h.store.GetComicPageData(id, pg.Index)
			if existing != nil {
				continue
			}

			resp, perr := http.Get(pg.ImageURL)
			if perr != nil {
				logging.Error("[comic] download page %d: %v", pg.Index, perr)
				continue
			}
			data, perr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if perr != nil {
				logging.Error("[comic] read page %d: %v", pg.Index, perr)
				continue
			}

			ct := resp.Header.Get("Content-Type")
			if ct == "" {
				ct = "image/jpeg"
			}

			h.store.SaveComicPage(id, pg.Index, pg.ImageURL, data, ct)
			logging.Info("[comic] cached page %d/%d for chapter %d", pg.Index, len(pages), id)
		}
		h.store.MarkComicChapterDownloaded(id)
		logging.Info("[comic] finished downloading chapter %d (%d pages)", id, len(pages))
	}()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":    true,
		"pages": len(pages),
		"msg":   fmt.Sprintf("Downloading %d pages in background", len(pages)),
	})
}
