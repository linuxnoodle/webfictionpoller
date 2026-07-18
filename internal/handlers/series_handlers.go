package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	sortBy := r.URL.Query().Get("sort")
	if sortBy != "received" {
		sortBy = "published"
	}
	unreadOnly := r.URL.Query().Get("unread") == "true"

	seriesSort := r.URL.Query().Get("seriesSort")

	seriesView, err := h.store.GetSeriesView()
	if err != nil {
		internalError(w, r, err)
		return
	}

	allSeries, err := h.store.ListSeriesSorted(seriesSort)
	if err != nil {
		internalError(w, r, err)
		return
	}

	buckets, err := h.store.GetRatingDistribution()
	if err != nil {
		internalError(w, r, err)
		return
	}
	dist := make([]int, 101)
	var maxCount int
	for _, b := range buckets {
		idx := int(math.Round(b.Rating * 10))
		if idx >= 0 && idx <= 100 {
			dist[idx] = b.Count
			if b.Count > maxCount {
				maxCount = b.Count
			}
		}
	}

	timeChapters, err := h.store.GetTimeView(0, 50, sortBy, unreadOnly)
	if err != nil {
		internalError(w, r, err)
		return
	}

	stats, err := h.store.GetDashboardStats()
	if err != nil {
		internalError(w, r, err)
		return
	}

	renderTemplate(w, r, "dashboard", map[string]interface{}{
		"Groups":      seriesView,
		"AllSeries":   allSeries,
		"RatingDist":  dist,
		"MaxRating":   maxCount,
		"TimeView":    timeChapters,
		"TimeGroups":  groupByDay(timeChapters, sortBy),
		"Stats":       stats,
		"TimePage":    0,
		"HasMoreTime": len(timeChapters) == 50,
		"SortBy":      sortBy,
		"UnreadOnly":  unreadOnly,
		"SeriesSort":  seriesSort,
	})
}

func (h *Handler) TimePagePartial(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	sortBy := r.URL.Query().Get("sort")
	if sortBy != "received" {
		sortBy = "published"
	}
	unreadOnly := r.URL.Query().Get("unread") == "true"

	chapters, err := h.store.GetTimeView(page, 50, sortBy, unreadOnly)
	if err != nil {
		internalError(w, r, err)
		return
	}

	data := map[string]interface{}{
		"TimeGroups":  groupByDay(chapters, sortBy),
		"TimePage":    page,
		"HasMoreTime": len(chapters) == 50,
		"SortBy":      sortBy,
		"UnreadOnly":  unreadOnly,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "time_page_partial", data); err != nil {
		internalError(w, r, err)
	}
}

func (h *Handler) ChapterPreview(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/chapters/")
	idStr = strings.TrimSuffix(idStr, "/preview")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	ch, err := h.store.GetChapterWithProvider(id)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if ch == nil {
		http.Error(w, "chapter not found", http.StatusNotFound)
		return
	}

	var content string

	archived, _ := h.store.GetChapterArchivedContent(id)
	if archived != "" {
		content = contentPolicy.Sanitize(archived)
	}

	if content == "" {
		p, ok := h.pool.GetProvider(ch.ProviderName)
		if ok {
			fetched, err := p.FetchChapterContent(ch.URL)
			if err != nil {
				logging.Error("[preview] error fetching content for chapter %d: %v", id, err)
			} else {
				content = contentPolicy.Sanitize(fetched)
				go func() {
					if saveErr := h.store.SavePreviewHTML(id, content); saveErr != nil {
						logging.Error("[preview] error saving preview for chapter %d: %v", id, saveErr)
					}
				}()
			}
		}
	}

	if content == "" {
		content = contentPolicy.Sanitize(ch.PreviewHTML)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "chapter_preview", map[string]interface{}{
		"Chapter":    ch,
		"Content":    template.HTML(content),
		"FaviconURL": plugin.Default.FaviconServedPath(ch.ProviderName),
	}); err != nil {
		internalError(w, r, err)
	}
}

func (h *Handler) AddSeriesPage(w http.ResponseWriter, r *http.Request) {
	// Show every registered text provider from the plugin registry, not
	// just the legacy pool — dreamy and future providers that don't
	// implement the full legacy interface would be invisible otherwise.
	type providerDisplay struct {
		Name        string
		DisplayName string
		Homepage    string
	}
	var providers []providerDisplay
	for _, p := range plugin.Default.ByKind(plugin.KindText) {
		m := p.Meta()
		providers = append(providers, providerDisplay{
			Name:        m.Name,
			DisplayName: m.DisplayName,
			Homepage:    m.Homepage,
		})
	}
	renderTemplate(w, r, "add_series", map[string]interface{}{
		"Providers": providers,
	})
}

func (h *Handler) AddSeries(w http.ResponseWriter, r *http.Request) {
	rawURL := r.FormValue("url")
	if rawURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "URL is required"})
		return
	}

	// Use the plugin registry to find a matching provider — this covers
	// ALL registered text providers including dreamy and future providers
	// that don't implement the legacy providers.Provider interface.
	p, ok := plugin.Default.ByURL(rawURL)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "No provider matched the given URL. Check the URL and try again."})
		return
	}

	// SeriesLister capability — required for adding a series.
	sl, ok := p.(plugin.SeriesLister)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Provider does not support series metadata fetching"})
		return
	}

	meta, err := sl.FetchSeriesMetadata(rawURL)
	if err != nil {
		logging.Error("[handler] fetching metadata for %s: %v", rawURL, err)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{"success": false, "error": "Failed to fetch series metadata. Check the URL and try again."})
		return
	}
	meta.Rating = models.UnratedRating
	meta.Status = "active"
	id, err := h.store.AddSeries(meta)
	if err != nil {
		logging.Error("[handler] saving series: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "Failed to save series"})
		return
	}
	if id == 0 {
		writeJSON(w, http.StatusConflict, map[string]interface{}{"success": false, "error": "This series is already tracked"})
		return
	}
	meta.ID = id

	var chapterCount int
	// Poller capability — optional but lets us pre-populate chapters.
	if poller, ok := p.(plugin.Poller); ok {
		chapters, err := poller.PollUpdates(meta)
		if err != nil {
			logging.Error("[handler] initial poll for %q (id=%d): %v", meta.Title, id, err)
		} else if len(chapters) > 0 {
			inserted, insertErr := h.store.InsertChapters(id, chapters)
			if insertErr != nil {
				logging.Error("[handler] inserting chapters for %q (id=%d): %v", meta.Title, id, insertErr)
			}
			chapterCount = inserted
			logging.Info("[handler] added series %q (id=%d) with %d chapters", meta.Title, id, inserted)
		} else {
			logging.Info("[handler] added series %q (id=%d) with 0 chapters", meta.Title, id)
		}
	} else {
		logging.Info("[handler] added series %q (id=%d); provider has no Poller capability, skipping initial poll", meta.Title, id)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"title":    meta.Title,
		"chapters": chapterCount,
	})
}

func (h *Handler) UpdateSeriesStatus(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/series/")
	parts := strings.Split(idStr, "/")
	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	status := r.FormValue("status")
	validStatuses := map[string]bool{"active": true, "binge": true, "hiatus": true, "dropped": true}
	if !validStatuses[status] {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	if err := h.store.UpdateSeriesStatus(id, status); err != nil {
		internalError(w, r, err)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) UpdateSeriesRating(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/series/")
	parts := strings.Split(idStr, "/")
	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	rating, err := strconv.ParseFloat(r.FormValue("rating"), 64)
	if err != nil || rating < -1 || rating > 10 {
		http.Error(w, "rating must be -1 to 10", http.StatusBadRequest)
		return
	}
	if err := h.store.UpdateSeriesRating(id, rating); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) DeleteSeries(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/series/")
	idStr = strings.TrimSuffix(idStr, "/delete")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.store.DeleteSeries(id); err != nil {
		internalError(w, r, err)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) SearchSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]struct{}{})
		return
	}
	results, err := h.store.SearchSeries(q)
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (h *Handler) LibraryPage(w http.ResponseWriter, r *http.Request) {
	archiveAll := h.store.GetSetting("archive_all") == "true"
	stats, err := h.store.GetArchiveStats(archiveAll)
	if err != nil {
		internalError(w, r, err)
		return
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host

	renderTemplate(w, r, "library", map[string]interface{}{
		"ArchiveStats": stats,
		"ArchiveAll":   archiveAll,
		"OPDSURL":      fmt.Sprintf("%s://USERNAME:PASSWORD@%s/opds", scheme, host),
	})
}
