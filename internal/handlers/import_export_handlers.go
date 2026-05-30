package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/opml"
	"github.com/linuxnoodle/webfictionpoller/internal/providers"
	"github.com/linuxnoodle/webfictionpoller/internal/worker"
)

func (h *Handler) ImportOPMLPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, r, "import_opml", nil)
}

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
