package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/worker"
)

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

func (h *Handler) SyncSeriesNow(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/series/")
	idStr = strings.TrimSuffix(idStr, "/sync")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "invalid id"})
		return
	}

	series, err := h.store.GetSeriesByID(id)
	if err != nil || series == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"success": false, "error": "series not found"})
		return
	}

	p, ok := h.pool.GetProvider(series.ProviderName)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "provider not found"})
		return
	}

	chapters, err := p.PollUpdates(*series)
	if err != nil {
		logging.Error("[sync] manual sync for %s failed: %v", series.Title, err)
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	inserted := 0
	if len(chapters) > 0 {
		inserted, err = h.store.InsertChapters(series.ID, chapters)
		if err != nil {
			logging.Error("[sync] inserting chapters for %s failed: %v", series.Title, err)
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "failed to save chapters"})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "inserted": inserted, "total": len(chapters)})
}
