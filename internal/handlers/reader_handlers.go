package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

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
		"chapters":            result,
		"progress_chapter_id": progressChapterID,
		"scroll_position":     scrollPos,
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
		comments = []models.Comment{}
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
