package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/microcosm-cc/bluemonday"

	"github.com/linuxnoodle/webfictionpoller/internal/crypto"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/opds"
	"github.com/linuxnoodle/webfictionpoller/internal/opml"
	"github.com/linuxnoodle/webfictionpoller/internal/providers"
	"github.com/linuxnoodle/webfictionpoller/internal/worker"
)

var contentPolicy = bluemonday.UGCPolicy()

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	logging.Error("[handler] %s %s: %v", r.Method, r.URL.Path, err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

type Handler struct {
	store         *Store
	pool          *worker.WorkerPool
	logDir        string
	updateChecker *UpdateChecker
	vault         *crypto.Vault
	opdsCatalog   *opds.Catalog
	archiver      *worker.Archiver
}

func NewHandler(store *Store, pool *worker.WorkerPool, logDir string, vault *crypto.Vault) *Handler {
	return &Handler{
		store:         store,
		pool:          pool,
		logDir:        logDir,
		updateChecker: NewUpdateChecker(),
		vault:         vault,
		opdsCatalog:   opds.NewCatalog(store),
	}
}

func (h *Handler) SetArchiver(a *worker.Archiver) {
	h.archiver = a
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	sortBy := r.URL.Query().Get("sort")
	if sortBy != "received" {
		sortBy = "published"
	}

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

	timeChapters, err := h.store.GetTimeView(0, 50, sortBy)
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

	chapters, err := h.store.GetTimeView(page, 50, sortBy)
	if err != nil {
		internalError(w, r, err)
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
		"FaviconURL": models.ProviderFavicon(ch.ProviderName),
	}); err != nil {
		internalError(w, r, err)
	}
}

func (h *Handler) LogsPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, r, "logs", nil)
}

func (h *Handler) LogsData(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines <= 0 {
		lines = 500
	}

	logs, err := logging.ReadLogs(h.logDir, lines)
	if err != nil {
		internalError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"lines": logs,
		"total": len(logs),
	})
}

func (h *Handler) AddSeriesPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, r, "add_series", map[string]interface{}{
		"Providers": h.pool.AllProviders(),
	})
}

func (h *Handler) AddSeries(w http.ResponseWriter, r *http.Request) {
	rawURL := r.FormValue("url")
	if rawURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "URL is required"})
		return
	}

	for _, p := range h.pool.AllProviders() {
		if p.MatchURL(rawURL) {
			meta, err := p.FetchSeriesMetadata(rawURL)
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
			chapters, err := p.PollUpdates(meta)
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

			writeJSON(w, http.StatusOK, map[string]interface{}{
				"success":  true,
				"title":    meta.Title,
				"chapters": chapterCount,
			})
			return
		}
	}

	writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "No provider matched the given URL. Check the URL and try again."})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
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
		internalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, redirectURL)
}

func (h *Handler) UnreadCountAPI(w http.ResponseWriter, r *http.Request) {
	stats, err := h.store.GetDashboardStats()
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"unread": stats.UnreadChapter})
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

func (h *Handler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/series/")
	idStr = strings.TrimSuffix(idStr, "/read-all")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.store.MarkAllSeriesRead(id); err != nil {
		internalError(w, r, err)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) MarkAllChaptersRead(w http.ResponseWriter, r *http.Request) {
	if err := h.store.MarkAllChaptersRead(); err != nil {
		internalError(w, r, err)
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
		internalError(w, r, err)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) ProviderConfigPage(w http.ResponseWriter, r *http.Request) {
	providers := h.pool.AllProviders()
	configs := make(map[string]string)
	usernames := make(map[string]string)
	loginTested := make(map[string]bool)
	for name := range providers {
		pc, _ := h.store.GetProviderConfig(name)
		if pc != nil {
			configs[name] = pc.CookieData
			usernames[name] = pc.Username
			loginTested[name] = pc.LoginTested
		}
	}
	renderTemplate(w, r, "provider_config", map[string]interface{}{
		"Providers":   providers,
		"Configs":     configs,
		"Usernames":   usernames,
		"LoginTested": loginTested,
	})
}

func (h *Handler) SaveProviderConfig(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("provider_name")
	cookieData := r.FormValue("cookie_data")
	username := r.FormValue("username")
	password := r.FormValue("password")
	if name == "" {
		http.Error(w, "provider_name required", http.StatusBadRequest)
		return
	}

	var encryptedPassword string
	if h.vault != nil && password != "" {
		enc, err := h.vault.Encrypt(password)
		if err != nil {
			logging.Error("[handler] encrypting password for %s: %v", name, err)
			http.Error(w, "encryption error", http.StatusInternalServerError)
			return
		}
		encryptedPassword = enc
	} else if h.vault != nil && password == "" {
		existing, _ := h.store.GetProviderConfig(name)
		if existing != nil {
			encryptedPassword = existing.EncryptedPassword
		}
	}

	if err := h.store.UpsertProviderConfig(name, cookieData, username, encryptedPassword); err != nil {
		internalError(w, r, err)
		return
	}

	p, ok := h.pool.GetProvider(name)
	if !ok {
		logging.Info("[handler] updated provider config for %s", name)
		w.Header().Set("HX-Redirect", "/admin/providers")
		w.WriteHeader(http.StatusOK)
		return
	}

	if username != "" && encryptedPassword != "" && p.SupportsLogin() && h.vault != nil {
		plainPass, err := h.vault.Decrypt(encryptedPassword)
		if err != nil {
			logging.Error("[handler] decrypting password for %s: %v", name, err)
		} else if err := p.Login(username, plainPass); err != nil {
			logging.Error("[handler] login failed for %s: %v", name, err)
		}
	} else if p.RequiresAuth() && cookieData != "" {
		_ = p.SetCookies(cookieData)
	}

	logging.Info("[handler] updated provider config for %s", name)
	w.Header().Set("HX-Redirect", "/admin/providers")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) CheckAuthProvider(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("provider_name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "provider_name required"})
		return
	}

	p, ok := h.pool.GetProvider(name)
	if !ok || !p.SupportsLogin() {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "provider does not support login"})
		return
	}

	pc, err := h.store.GetProviderConfig(name)
	if err != nil || pc == nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "no credentials saved"})
		return
	}

	if pc.Username == "" || pc.EncryptedPassword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "username and password required"})
		return
	}

	if h.vault == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "encryption not available"})
		return
	}

	plainPass, err := h.vault.Decrypt(pc.EncryptedPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "failed to decrypt password"})
		return
	}

	if err := p.Login(pc.Username, plainPass); err != nil {
		_ = h.store.SetLoginTested(name, false)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Login failed: invalid credentials"})
		return
	}

	_ = h.store.SetLoginTested(name, true)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (h *Handler) GetProviderPassword(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("provider")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "provider required"})
		return
	}

	pc, err := h.store.GetProviderConfig(name)
	if err != nil || pc == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "no config found"})
		return
	}

	if pc.EncryptedPassword == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"password": ""})
		return
	}

	if h.vault == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "encryption not available"})
		return
	}

	plainPass, err := h.vault.Decrypt(pc.EncryptedPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "failed to decrypt"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"password": plainPass})
}

func (h *Handler) PollNow(w http.ResponseWriter, r *http.Request) {
	all, err := h.store.GetAllActiveSeries()
	if err != nil {
		internalError(w, r, err)
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
	h.pool.StartPoll(count)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"queued": count})
}

func (h *Handler) PollProgress(w http.ResponseWriter, r *http.Request) {
	status := h.pool.PollProgress()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
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

func (h *Handler) ImportOPMLPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, r, "import_opml", nil)
}

const maxUploadSize = 10 << 20

func (h *Handler) ImportOPML(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	file, _, err := r.FormFile("opml_file")
	if err != nil {
		http.Error(w, "opml_file required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	feeds, err := opml.Parse(file)
	if err != nil {
		logging.Error("[handler] parsing OPML: %v", err)
		http.Error(w, "invalid OPML file", http.StatusBadRequest)
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
			Rating:       models.UnratedRating,
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
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
		internalError(w, r, err)
		return
	}

	data, err := opml.BuildOPML(series)
	if err != nil {
		internalError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=webfictionpoller.opml")
	w.Write(data)
}

func (h *Handler) ExportBackup(w http.ResponseWriter, r *http.Request) {
	backup, err := h.store.ExportBackup()
	if err != nil {
		internalError(w, r, err)
		return
	}

	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		internalError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=webfictionpoller-backup.json")
	w.Write(data)
}

func (h *Handler) ImportBackup(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	file, _, err := r.FormFile("backup_file")
	if err != nil {
		http.Error(w, "backup_file required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		internalError(w, r, err)
		return
	}

	var backup models.Backup
	if err := json.Unmarshal(data, &backup); err != nil {
		http.Error(w, "invalid backup file", http.StatusBadRequest)
		return
	}

	imported, skipped, err := h.store.ImportBackup(&backup)
	if err != nil {
		logging.Error("[handler] import error: %v", err)
		http.Error(w, "import failed", http.StatusInternalServerError)
		return
	}

	logging.Info("[backup-import] complete: %d imported, %d updated/skipped", imported, skipped)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) UpdateSeriesArchive(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/series/")
	idStr = strings.TrimSuffix(idStr, "/archive")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	archive := r.FormValue("archive") == "true"
	if err := h.store.UpdateSeriesArchive(id, archive); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) OPDSRoot(w http.ResponseWriter, r *http.Request) {
	h.opdsCatalog.ServeRoot(w, r)
}

func (h *Handler) OPDSCover(w http.ResponseWriter, r *http.Request) {
	h.opdsCatalog.ServeCover(w, r)
}

func (h *Handler) OPDSEpub(w http.ResponseWriter, r *http.Request) {
	h.opdsCatalog.ServeEPUB(w, r)
}

func (h *Handler) OPDSImage(w http.ResponseWriter, r *http.Request) {
	h.opdsCatalog.ServeImage(w, r)
}

func (h *Handler) ArchiverStatusAPI(w http.ResponseWriter, r *http.Request) {
	if h.archiver == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"active": false})
		return
	}
	writeJSON(w, http.StatusOK, h.archiver.GetStatus())
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

func (h *Handler) ArchiveAllAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		archiveAll := h.store.GetSetting("archive_all") == "true"
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": archiveAll})
		return
	}
	enabled := r.FormValue("enabled") == "true"
	if err := h.store.SetSetting("archive_all", fmt.Sprintf("%v", enabled)); err != nil {
		internalError(w, r, err)
		return
	}
	logging.Info("[handler] archive_all setting updated to %v", enabled)
	writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": enabled})
}

func (h *Handler) DeleteSeriesArchive(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	series, err := h.store.GetSeriesByID(id)
	if err != nil || series == nil {
		http.Error(w, "series not found", http.StatusNotFound)
		return
	}

	if err := h.store.DeleteSeriesArchive(id); err != nil {
		internalError(w, r, err)
		return
	}

	logging.Info("[handler] deleted archive for series %q (id=%d)", series.Title, id)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "title": series.Title})
}

func (h *Handler) DeleteChapterArchive(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := h.store.DeleteChapterArchive(id); err != nil {
		internalError(w, r, err)
		return
	}

	logging.Info("[handler] deleted archive for chapter id=%d", id)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (h *Handler) ReArchiveSeries(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	series, err := h.store.GetSeriesByID(id)
	if err != nil || series == nil {
		http.Error(w, "series not found", http.StatusNotFound)
		return
	}

	count, err := h.store.TriggerReArchive(id)
	if err != nil {
		internalError(w, r, err)
		return
	}

	logging.Info("[handler] triggered re-archive for series %q (id=%d, %d chapters)", series.Title, id, count)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "title": series.Title, "chapters": count})
}

func (h *Handler) StorageInfoAPI(w http.ResponseWriter, r *http.Request) {
	info, err := h.store.GetStorageInfo()
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *Handler) TriggerArchiveNow(w http.ResponseWriter, r *http.Request) {
	if h.archiver == nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "archiver not available"})
		return
	}
	go h.archiver.RunOnce()
	writeJSON(w, http.StatusOK, map[string]interface{}{"started": true})
}

func (h *Handler) ReaderPage(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	series, err := h.store.GetSeriesByID(id)
	if err != nil || series == nil {
		http.Error(w, "series not found", http.StatusNotFound)
		return
	}

	settings, _ := h.store.GetReaderSettings()
	if settings == "" {
		settings = "{}"
	}

	renderTemplate(w, r, "reader", map[string]interface{}{
		"Series":         series,
		"InitChapterID":  r.URL.Query().Get("chapter"),
		"ReaderSettings": settings,
	})
}

func (h *Handler) ReaderChaptersAPI(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}

	chapters, err := h.store.GetReaderChapters(id)
	if err != nil {
		internalError(w, r, err)
		return
	}

	progressChapterID, scrollPos, err := h.store.GetReadingProgress(id)
	if err != nil {
		progressChapterID = 0
		scrollPos = 0
	}

	type chapterJSON struct {
		ID          int64     `json:"id"`
		Title       string    `json:"title"`
		URL         string    `json:"url"`
		PublishedAt time.Time `json:"published_at"`
		IsRead      bool      `json:"is_read"`
		HasContent  bool      `json:"has_content"`
	}

	result := make([]chapterJSON, len(chapters))
	for i, ch := range chapters {
		result[i] = chapterJSON{
			ID:          ch.ID,
			Title:       ch.Title,
			URL:         ch.URL,
			PublishedAt: ch.PublishedAt,
			IsRead:      ch.IsRead,
			HasContent:  ch.ContentHTML != "",
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"chapters":           result,
		"progress_chapter_id": progressChapterID,
		"scroll_position":    scrollPos,
	})
}

func (h *Handler) ReaderChapterContentAPI(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}

	content, seriesID, err := h.store.GetReaderChapterContent(id)
	if err != nil {
		internalError(w, r, err)
		return
	}

	prevID, nextID, _ := h.store.GetAdjacentChapterIDs(id)

	if content == "" {
		ch, err := h.store.GetChapterWithProvider(id)
		if err != nil || ch == nil {
			writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "not found"})
			return
		}
		p, ok := h.pool.GetProvider(ch.ProviderName)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "provider not found"})
			return
		}
		fetched, fetchErr := p.FetchChapterContent(ch.URL)
		if fetchErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "failed to fetch"})
			return
		}
		content = contentPolicy.Sanitize(fetched)
	} else {
		content = contentPolicy.Sanitize(content)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"content":   content,
		"series_id": seriesID,
		"prev_id":   prevID,
		"next_id":   nextID,
	})
}

func (h *Handler) ReaderChapterCommentsAPI(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid id"})
		return
	}

	ch, err := h.store.GetChapterWithProvider(id)
	if err != nil || ch == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "not found"})
		return
	}

	p, ok := h.pool.GetProvider(ch.ProviderName)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]interface{}{"comments": []interface{}{}})
		return
	}

	comments, err := p.FetchComments(ch.URL)
	if err != nil {
		logging.Error("[handler] fetch comments for chapter %d: %v", id, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"comments": []interface{}{}})
		return
	}

	if comments == nil {
		comments = []providers.Comment{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"comments": comments})
}

func (h *Handler) ReaderSaveProgressAPI(w http.ResponseWriter, r *http.Request) {
	seriesID, err := strconv.ParseInt(r.FormValue("series_id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid series_id"})
		return
	}
	chapterID, err := strconv.ParseInt(r.FormValue("chapter_id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid chapter_id"})
		return
	}
	scrollPos := 0.0
	if sp := r.FormValue("scroll_position"); sp != "" {
		scrollPos, _ = strconv.ParseFloat(sp, 64)
	}

	if err := h.store.SaveReadingProgress(seriesID, chapterID, scrollPos); err != nil {
		internalError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (h *Handler) ReaderSettingsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		settings, err := h.store.GetReaderSettings()
		if err != nil {
			internalError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(settings))
		return
	}

	var settings map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid json"})
		return
	}

	data, err := json.Marshal(settings)
	if err != nil {
		internalError(w, r, err)
		return
	}

	if err := h.store.SaveReaderSettings(string(data)); err != nil {
		internalError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}
