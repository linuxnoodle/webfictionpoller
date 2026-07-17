package handlers

import (
	"net/http"
	"sort"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

// PluginsPage renders /admin/plugins: every registered provider with its
// Meta, capability badges, auth modes, and (for text providers) the per-
// provider polling interval override form. Replaces the older /admin/providers
// page for catalog browsing; the old page remains for cookie/credential config.
func (h *Handler) PluginsPage(w http.ResponseWriter, r *http.Request) {
	all := plugin.Default.All()
	rows := make([]pluginRow, 0, len(all))

	// Snapshot worker metrics if a pool is wired.
	var metricsByName map[string]MetricsView
	if pool := h.pluginMetricsSnapshot(); pool != nil {
		metricsByName = pool
	}

	for _, p := range all {
		m := p.Meta()
		row := pluginRow{
			Name:                m.Name,
			DisplayName:         m.DisplayName,
			Kind:                string(m.Kind),
			Homepage:            m.Homepage,
			FaviconURL:          plugin.Default.FaviconServedPath(m.Name),
			AuthModes:           authModeStrings(m.AuthModes),
			PollIntervalDefault: m.PollIntervalDefault,
			Rate: rateView{
				RequestsPerSecond: m.Rate.RequestsPerSecond,
				Burst:             m.Rate.Burst,
				Concurrency:       m.Rate.Concurrency,
			},
			Capabilities: capabilitiesOf(p),
			Declarative:  isDeclarative(p),
		}
		// Text providers expose a per-provider interval override (settings).
		if m.Kind == plugin.KindText {
			row.PollIntervalCurrent = h.store.GetSetting("poll_interval:" + m.Name)
		}
		if mv, ok := metricsByName[m.Name]; ok {
			row.Metrics = &mv
		}
		rows = append(rows, row)
	}

	// Stable sort: text first, then comic, alphabetical within kind.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Name < rows[j].Name
	})

	renderTemplate(w, r, "plugins", map[string]interface{}{
		"Page":      "plugins",
		"Providers": rows,
	})
}

// SavePluginPollInterval handles POST /admin/plugins/poll-interval. Body
// params: name, interval. Empty interval clears the override (revert to
// provider default or global).
func (h *Handler) SavePluginPollInterval(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	interval := r.FormValue("interval")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if _, ok := plugin.Default.Get(name); !ok {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}
	if interval != "" {
		if _, err := time.ParseDuration(interval); err != nil {
			http.Error(w, "invalid duration", http.StatusBadRequest)
			return
		}
		if err := h.store.SetSetting("poll_interval:"+name, interval); err != nil {
			internalError(w, r, err)
			return
		}
	} else {
		// Clearing the override = empty value; SetSetting stores "" which the
		// scheduler treats as "fall through to Meta/global".
		_ = h.store.SetSetting("poll_interval:"+name, "")
	}
	w.Header().Set("HX-Redirect", "/admin/plugins")
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// View helpers + DTOs
// ---------------------------------------------------------------------------

type pluginRow struct {
	Name                string      `json:"name"`
	DisplayName         string      `json:"display_name"`
	Kind                string      `json:"kind"`
	Homepage            string      `json:"homepage"`
	FaviconURL          string      `json:"favicon_url"`
	AuthModes           []string    `json:"auth_modes"`
	PollIntervalDefault string      `json:"poll_interval_default"`
	PollIntervalCurrent string      `json:"poll_interval_current"`
	Rate                rateView    `json:"rate"`
	Capabilities        []string    `json:"capabilities"`
	Declarative         bool        `json:"declarative"`
	Metrics             *MetricsView `json:"metrics,omitempty"`
}

type rateView struct {
	RequestsPerSecond float64 `json:"requests_per_second"`
	Burst             int     `json:"burst"`
	Concurrency       int     `json:"concurrency"`
}

// MetricsView is the worker-metrics snapshot shape used by the plugins page
// and exposed for main.go to adapt from worker.MetricsSnapshot.
type MetricsView struct {
	LastPollAt       time.Time `json:"last_poll_at"`
	LastErrorAt      time.Time `json:"last_error_at"`
	LastError        string    `json:"last_error"`
	LastChapterCount int64     `json:"last_chapter_count"`
	TotalPolls       int64     `json:"total_polls"`
	TotalErrors      int64     `json:"total_errors"`
	TotalChapters    int64     `json:"total_chapters"`
}

// pluginMetricsSnapshot returns worker metrics keyed by provider name, or nil
// if the pool isn't wired (tests, isolated handlers). We go through a method
// so the dependency on *worker.WorkerPool stays optional.
func (h *Handler) pluginMetricsSnapshot() map[string]MetricsView {
	if h.poolMetricsFn == nil {
		return nil
	}
	return h.poolMetricsFn()
}

// SetPoolMetricsFn injects the metrics-fetcher. main.go wires this to call
// pool.MetricsSnapshots and adapt the result.
func (h *Handler) SetPoolMetricsFn(fn func() map[string]MetricsView) { h.poolMetricsFn = fn }

// authModeStrings converts []plugin.AuthMode to plain strings for templates.
func authModeStrings(in []plugin.AuthMode) []string {
	if len(in) == 0 {
		return []string{"none"}
	}
	out := make([]string, len(in))
	for i, m := range in {
		out[i] = string(m)
	}
	return out
}

// capabilitiesOf reports which capability interfaces p implements. We probe
// the common set; results are surfaced as badges in the UI.
func capabilitiesOf(p plugin.Provider) []string {
	caps := []string{}
	checks := []struct {
		name string
		ptr  interface{}
	}{
		{"poller", (*plugin.Poller)(nil)},
		{"series-lister", (*plugin.SeriesLister)(nil)},
		{"html-fetcher", (*plugin.HTMLFetcher)(nil)},
		{"comments", (*plugin.CommentFetcher)(nil)},
		{"searcher", (*plugin.Searcher)(nil)},
		{"chapter-lister", (*plugin.ChapterLister)(nil)},
		{"page-lister", (*plugin.PageLister)(nil)},
		{"login", (*plugin.LoginAuth)(nil)},
		{"cookies", (*plugin.CookieAuth)(nil)},
	}
	for _, c := range checks {
		if plugin.DefaultImplements(p, c.ptr) {
			caps = append(caps, c.name)
		}
	}
	return caps
}

// isDeclarative reports whether p is a TOML-driven provider. We can't import
// the declarative subtype from the plugin package without a cycle (it's
// internal); we expose it via an interface probe instead.
func isDeclarative(p plugin.Provider) bool {
	type declarativeMarker interface {
		IsDeclarative() bool
	}
	if dm, ok := p.(declarativeMarker); ok {
		return dm.IsDeclarative()
	}
	return false
}
