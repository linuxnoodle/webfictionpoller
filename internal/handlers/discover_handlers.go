package handlers

import (
	"net/http"
	"strconv"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

// DiscoverPage renders /discover: a search UI over text providers that
// implement SeriesSearcher. Users pick a provider badge, run a query, and
// add hits straight to the library via /series/add.
func (h *Handler) DiscoverPage(w http.ResponseWriter, r *http.Request) {
	// Only text providers that implement SeriesSearcher are searchable here.
	var providers []plugin.Meta
	for _, p := range plugin.Default.WithCapability((*plugin.SeriesSearcher)(nil)) {
		if m := p.Meta(); m.Kind == plugin.KindText {
			providers = append(providers, m)
		}
	}

	activeProvider := ""
	activeProviderName := ""
	if len(providers) > 0 {
		activeProvider = providers[0].Name
		activeProviderName = providers[0].DisplayName
	}

	renderTemplate(w, r, "discover", map[string]interface{}{
		"Page":               "discover",
		"Providers":          providers,
		"ActiveProvider":     activeProvider,
		"ActiveProviderName": activeProviderName,
	})
}

// DiscoverSearchAPI runs a free-text query against the active provider.
// GET /api/discover/search?provider=<name>&q=<query>&page=<n>
// Returns {"results": [SeriesSearchResult...]}.
func (h *Handler) DiscoverSearchAPI(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	providerName := r.URL.Query().Get("provider")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if query == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"results": []interface{}{}})
		return
	}

	p, ok := plugin.Default.Get(providerName)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "unknown provider"})
		return
	}
	searcher, ok := p.(plugin.SeriesSearcher)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "provider does not support search"})
		return
	}

	results, err := searcher.Search(query, page)
	if err != nil {
		logging.Error("[discover] search %q on %s: %v", query, providerName, err)
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}
	if results == nil {
		results = []plugin.SeriesSearchResult{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"results": results})
}
