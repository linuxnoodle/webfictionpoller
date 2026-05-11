package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/opml"
	"github.com/linuxnoodle/webfictionpoller/internal/providers"
	"github.com/linuxnoodle/webfictionpoller/internal/worker"
)

type Handler struct {
	store         *Store
	pool          *worker.WorkerPool
	logDir        string
	updateChecker *UpdateChecker
}

func NewHandler(store *Store, pool *worker.WorkerPool, logDir string) *Handler {
	return &Handler{store: store, pool: pool, logDir: logDir, updateChecker: NewUpdateChecker()}
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	sortBy := r.URL.Query().Get("sort")
	if sortBy != "received" {
		sortBy = "published"
	}

	seriesView, err := h.store.GetSeriesView()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	timeChapters, err := h.store.GetTimeView(0, 50, sortBy)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	stats, err := h.store.GetDashboardStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	renderTemplate(w, "dashboard", map[string]interface{}{
		"Groups":      seriesView,
		"TimeView":    timeChapters,
		"TimeGroups":  groupByDay(timeChapters, sortBy),
		"Stats":       stats,
		"TimePage":    0,
		"HasMoreTime": len(timeChapters) == 50,
		"SortBy":      sortBy,
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

	chapters, err := h.store.GetTimeView(page, 50, sortBy)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"TimeGroups":  groupByDay(chapters, sortBy),
		"TimePage":    page,
		"HasMoreTime": len(chapters) == 50,
		"SortBy":      sortBy,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "time_page_partial", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ch == nil {
		http.Error(w, "chapter not found", http.StatusNotFound)
		return
	}

	content := ch.PreviewHTML
	if content == "" {
		p, ok := h.pool.GetProvider(ch.ProviderName)
		if !ok {
			http.Error(w, "provider not found", http.StatusBadRequest)
			return
		}

		fetched, err := p.FetchChapterContent(ch.URL)
		if err != nil {
			logging.Error("[preview] error fetching content for chapter %d: %v", id, err)
			content = fmt.Sprintf("<p class='text-red-400 text-sm'>Failed to load preview: %s</p>", err.Error())
		} else {
			content = fetched
			go func() {
				if saveErr := h.store.SavePreviewHTML(id, content); saveErr != nil {
					logging.Error("[preview] error saving preview for chapter %d: %v", id, saveErr)
				}
			}()
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "chapter_preview", map[string]interface{}{
		"Chapter":    ch,
		"Content":    template.HTML(content),
		"FaviconURL": models.ProviderFavicon(ch.ProviderName),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) LogsPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "logs", nil)
}

func (h *Handler) LogsData(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines <= 0 {
		lines = 500
	}

	logs, err := logging.ReadLogs(h.logDir, lines)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"lines": logs,
		"total": len(logs),
	})
}

func (h *Handler) SeriesList(w http.ResponseWriter, r *http.Request) {
	series, err := h.store.ListSeries()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	buckets, err := h.store.GetRatingDistribution()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dist := make([]int, 11)
	var maxCount int
	for _, b := range buckets {
		idx := int(b.Rating)
		if idx >= 0 && idx <= 10 {
			dist[idx] = b.Count
			if b.Count > maxCount {
				maxCount = b.Count
			}
		}
	}
	renderTemplate(w, "series", map[string]interface{}{
		"Series":     series,
		"RatingDist": dist,
		"MaxRating":  maxCount,
	})
}

func (h *Handler) AddSeriesPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "add_series", map[string]interface{}{
		"Providers": h.pool.AllProviders(),
	})
}

func (h *Handler) AddSeries(w http.ResponseWriter, r *http.Request) {
	url := r.FormValue("url")
	if url == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}

	for _, p := range h.pool.AllProviders() {
		if p.MatchURL(url) {
			meta, err := p.FetchSeriesMetadata(url)
			if err != nil {
				http.Error(w, "fetching metadata: "+err.Error(), http.StatusInternalServerError)
				return
			}
			meta.Rating = 0
			meta.Status = "active"
			id, err := h.store.AddSeries(meta)
			if err != nil {
				http.Error(w, "saving series: "+err.Error(), http.StatusInternalServerError)
				return
			}
			h.pool.Submit(worker.Job{Series: meta, Provider: p})
			logging.Info("[handler] added series %q (id=%d)", meta.Title, id)
			w.Header().Set("HX-Redirect", "/series")
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	http.Error(w, "no provider matched url: "+url, http.StatusBadRequest)
}

func (h *Handler) MarkChapterRead(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/chapters/")
	idStr = strings.TrimSuffix(idStr, "/read")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	redirectURL, err := h.store.MarkChapterRead(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, redirectURL)
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
	if status == "" {
		http.Error(w, "status required", http.StatusBadRequest)
		return
	}
	if err := h.store.UpdateSeriesStatus(id, status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/series")
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
	if err != nil || rating < 0 || rating > 10 {
		http.Error(w, "rating must be 0-10", http.StatusBadRequest)
		return
	}
	if err := h.store.UpdateSeriesRating(id, rating); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/series/")
	idStr = strings.TrimSuffix(idStr, "/read-all")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.store.MarkAllSeriesRead(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) MarkAllChaptersRead(w http.ResponseWriter, r *http.Request) {
	if err := h.store.MarkAllChaptersRead(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/")
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/series")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) ProviderConfigPage(w http.ResponseWriter, r *http.Request) {
	providers := h.pool.AllProviders()
	configs := make(map[string]string)
	for name := range providers {
		pc, _ := h.store.GetProviderConfig(name)
		if pc != nil {
			configs[name] = pc.CookieData
		}
	}
	renderTemplate(w, "provider_config", map[string]interface{}{
		"Providers": providers,
		"Configs":   configs,
	})
}

func (h *Handler) SaveProviderConfig(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("provider_name")
	cookieData := r.FormValue("cookie_data")
	if name == "" {
		http.Error(w, "provider_name required", http.StatusBadRequest)
		return
	}
	if err := h.store.UpsertProviderConfig(name, cookieData); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p, ok := h.pool.GetProvider(name); ok && p.RequiresAuth() {
		_ = p.SetCookies(cookieData)
	}
	logging.Info("[handler] updated provider config for %s", name)
	w.Header().Set("HX-Redirect", "/admin/providers")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) PollNow(w http.ResponseWriter, r *http.Request) {
	all, err := h.store.GetAllActiveSeries()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	count := 0
	for _, s := range all {
		p, ok := h.pool.GetProvider(s.ProviderName)
		if !ok {
			continue
		}
		h.pool.Submit(worker.Job{Series: s, Provider: p})
		count++
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"queued": count})
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (h *Handler) ImportOPMLPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, "import_opml", nil)
}

func (h *Handler) ImportOPML(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("opml_file")
	if err != nil {
		http.Error(w, "opml_file required: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	feeds, err := opml.Parse(file)
	if err != nil {
		http.Error(w, "parsing OPML: "+err.Error(), http.StatusBadRequest)
		return
	}

	imported := 0
	skipped := 0
	unmatched := 0

	for _, feed := range feeds {
		threadURL := h.resolveThreadURL(feed.FeedURL)
		if threadURL == "" {
			threadURL = feed.SiteURL
		}

		provider := h.matchProvider(threadURL)
		if provider == nil {
			provider = h.matchProvider(feed.FeedURL)
		}
		if provider == nil {
			unmatched++
			continue
		}

		existing, err := h.store.GetSeriesBySourceURL(threadURL)
		if err != nil {
			logging.Error("[import] error checking series %s: %v", threadURL, err)
			continue
		}
		if existing != nil {
			skipped++
			continue
		}

		series := models.Series{
			Title:        feed.Title,
			SourceURL:    threadURL,
			ProviderName: provider.Name(),
			Status:       "active",
			Rating:       0,
		}

		id, err := h.store.AddSeries(series)
		if err != nil {
			if !strings.Contains(err.Error(), "UNIQUE constraint") {
				logging.Error("[import] error adding series %q: %v", feed.Title, err)
			} else {
				skipped++
			}
			continue
		}

		if id > 0 {
			h.pool.Submit(worker.Job{Series: models.Series{ID: id, Title: feed.Title, SourceURL: threadURL, ProviderName: provider.Name()}, Provider: provider})
		}
		imported++
	}

	logging.Info("[import] complete: %d imported, %d skipped, %d unmatched", imported, skipped, unmatched)
	http.Redirect(w, r, "/series", http.StatusSeeOther)
}

func (h *Handler) matchProvider(rawURL string) providers.Provider {
	for _, p := range h.pool.AllProviders() {
		if p.MatchURL(rawURL) {
			return p
		}
	}
	return nil
}

func (h *Handler) resolveThreadURL(feedURL string) string {
	if strings.Contains(feedURL, "/threadmarks.rss") {
		return strings.Split(feedURL, "/threadmarks.rss")[0]
	}
	if strings.Contains(feedURL, "/syndication/") {
		parts := strings.Split(feedURL, "/syndication/")
		if len(parts) >= 2 {
			fictionID := strings.Split(parts[1], "/")[0]
			return "https://www.royalroad.com/fiction/" + fictionID
		}
	}
	if strings.Contains(feedURL, "/unread.rss") {
		return strings.Split(feedURL, "/unread.rss")[0]
	}
	return feedURL
}

func (h *Handler) ExportOPML(w http.ResponseWriter, r *http.Request) {
	series, err := h.store.ListSeries()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data, err := opml.BuildOPML(series)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=webfictionpoller.opml")
	w.Write(data)
}
