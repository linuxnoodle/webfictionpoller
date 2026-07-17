package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

// SourcesPage renders the per-series source management UI.
// GET /admin/series/{id}/sources
func (h *Handler) SourcesPage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	series, err := h.store.GetSeriesByID(id)
	if err != nil || series == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	sources, err := h.store.ListSources(id)
	if err != nil {
		internalError(w, r, err)
		return
	}
	renderTemplate(w, r, "sources", map[string]interface{}{
		"Page":    "sources",
		"Series":  series,
		"Sources": sources,
		// For the add-source form: list every registered text provider so the
		// user picks from a known set rather than typing provider names.
		"TextProviders": textProviderCatalog(),
	})
}

// AddSourceForm handles POST /admin/series/{id}/sources. Validates the
// provider+URL pair against the registry, then delegates to store.AddSource.
func (h *Handler) AddSourceForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	providerName := r.FormValue("provider_name")
	sourceURL := r.FormValue("source_url")
	priorityStr := r.FormValue("priority")
	priority := 100
	if priorityStr != "" {
		if n, err := strconv.Atoi(priorityStr); err == nil {
			priority = n
		}
	}

	if providerName == "" || sourceURL == "" {
		http.Error(w, "provider_name and source_url required", http.StatusBadRequest)
		return
	}
	p, ok := plugin.Default.Get(providerName)
	if !ok {
		logging.Error("[sources] unknown provider %q", providerName)
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}
	if !p.MatchURL(sourceURL) {
		logging.Error("[sources] URL %q doesn't match provider %q", sourceURL, providerName)
		http.Error(w, "URL doesn't match provider", http.StatusBadRequest)
		return
	}
	if _, err := h.store.AddSource(id, providerName, sourceURL, priority); err != nil {
		internalError(w, r, err)
		return
	}
	w.Header().Set("HX-Redirect", "/admin/series/"+strconv.FormatInt(id, 10)+"/sources")
	w.WriteHeader(http.StatusOK)
}

// PromoteSourceForm handles POST /admin/series/{id}/sources/{sourceID}/promote.
func (h *Handler) PromoteSourceForm(w http.ResponseWriter, r *http.Request) {
	seriesID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	sourceID, _ := strconv.ParseInt(chi.URLParam(r, "sourceID"), 10, 64)
	if err := h.store.PromoteSource(sourceID); err != nil {
		logging.Error("[sources] promote %d: %v", sourceID, err)
	}
	w.Header().Set("HX-Redirect", "/admin/series/"+strconv.FormatInt(seriesID, 10)+"/sources")
	w.WriteHeader(http.StatusOK)
}

// ToggleSourceForm handles POST /admin/series/{id}/sources/{sourceID}/toggle.
// Reads form value "disabled" to set the new state.
func (h *Handler) ToggleSourceForm(w http.ResponseWriter, r *http.Request) {
	seriesID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	sourceID, _ := strconv.ParseInt(chi.URLParam(r, "sourceID"), 10, 64)
	disabled := r.FormValue("disabled") == "1"
	// priority=-1 signals "leave unchanged" to UpdateSource.
	if err := h.store.UpdateSource(sourceID, -1, disabled); err != nil {
		logging.Error("[sources] toggle %d: %v", sourceID, err)
	}
	w.Header().Set("HX-Redirect", "/admin/series/"+strconv.FormatInt(seriesID, 10)+"/sources")
	w.WriteHeader(http.StatusOK)
}

// DeleteSourceForm handles POST /admin/series/{id}/sources/{sourceID}/delete.
func (h *Handler) DeleteSourceForm(w http.ResponseWriter, r *http.Request) {
	seriesID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	sourceID, _ := strconv.ParseInt(chi.URLParam(r, "sourceID"), 10, 64)
	if err := h.store.DeleteSource(sourceID); err != nil {
		logging.Error("[sources] delete %d: %v", sourceID, err)
		http.Error(w, "cannot delete the only remaining source", http.StatusConflict)
		return
	}
	w.Header().Set("HX-Redirect", "/admin/series/"+strconv.FormatInt(seriesID, 10)+"/sources")
	w.WriteHeader(http.StatusOK)
}

// textProviderCatalog returns metadata for every registered text provider,
// for the add-source dropdown.
func textProviderCatalog() []plugin.Meta {
	var out []plugin.Meta
	for _, p := range plugin.Default.ByKind(plugin.KindText) {
		out = append(out, p.Meta())
	}
	return out
}
