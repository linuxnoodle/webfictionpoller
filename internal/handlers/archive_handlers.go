package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
)

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

func (h *Handler) ArchiverStatusAPI(w http.ResponseWriter, r *http.Request) {
	if h.archiver == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"active": false})
		return
	}
	writeJSON(w, http.StatusOK, h.archiver.GetStatus())
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
