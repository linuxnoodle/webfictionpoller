package handlers

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// navPages are full documents that must include the top nav partial.
var navPages = []string{
	"dashboard", "add_series", "discover", "tokens", "plugins", "sources",
	"import_opml", "version", "library", "logs", "provider_config", "comic_browse",
}

// navlessPages are full documents that intentionally omit the nav.
var navlessPages = []string{"login", "setup", "reader", "comic_reader"}

// TestTemplateNavGuard statically asserts every page that should include the
// nav uses the CORRECT template name ("nav", not "nav.html") and loads the
// shared head_assets partial (htmx + CSRF header listener). Regression guard
// for the P1a bug where {{template "nav.html"}} rendered an empty nav and the
// htmx-missing-on-nav-pages drift.
func TestTemplateNavGuard(t *testing.T) {
	for _, p := range navPages {
		b, err := os.ReadFile(filepath.Join("templates", p+".html"))
		if err != nil {
			t.Fatalf("read %s.html: %v", p, err)
		}
		if !bytes.Contains(b, []byte(`{{template "nav" .}}`)) {
			t.Errorf("%s.html: must include {{template \"nav\" .}} (nav.html is an empty template)", p)
		}
		if !bytes.Contains(b, []byte(`{{template "head_assets" .}}`)) {
			t.Errorf("%s.html: must include {{template \"head_assets\" .}} (htmx + csrf listener)", p)
		}
	}
}

// TestTemplateThemeGuard asserts every full document uses the unified theme
// class, catching drift back to hardcoded neutrals or the bare "dark" class.
func TestTemplateThemeGuard(t *testing.T) {
	for _, p := range append(append([]string{}, navPages...), navlessPages...) {
		b, err := os.ReadFile(filepath.Join("templates", p+".html"))
		if err != nil {
			continue
		}
		if bytes.Contains(b, []byte(`<html lang="en" class="bg-neutral-950`)) ||
			bytes.Contains(b, []byte(`<html lang="en" class="dark">`)) {
			t.Errorf("%s.html: non-standard <html> theme class; use bg-background text-white", p)
		}
	}
}

// TestPagesExecute renders every full page + fragment with representative
// data and asserts it executes cleanly and (where expected) includes the nav.
// Catches missing-template names and data-shape regressions at parse time.
func TestPagesExecute(t *testing.T) {
	if err := InitTemplates(); err != nil {
		t.Fatalf("InitTemplates: %v", err)
	}
	type tc struct {
		name    string
		data    map[string]interface{}
		wantNav bool
	}
	cases := []tc{
		{"dashboard", map[string]interface{}{
			"Groups":      []interface{}{},
			"AllSeries":   []interface{}{},
			"RatingDist":  []interface{}{},
			"MaxRating":   0,
			"TimeView":    []interface{}{},
			"TimeGroups":  []interface{}{},
			"Stats":       models.DashboardStats{},
			"TimePage":    0,
			"HasMoreTime": false,
			"SortBy":      "published",
			"UnreadOnly":  false,
			"SeriesSort":  "title",
		}, true},
		{"add_series", map[string]interface{}{"Providers": []interface{}{}}, true},
		{"discover", map[string]interface{}{
			"Providers": []interface{}{}, "ActiveProvider": "", "ActiveProviderName": "",
		}, true},
		{"tokens", map[string]interface{}{"Tokens": []interface{}{}}, true},
		{"plugins", map[string]interface{}{"Providers": []interface{}{}}, true},
		{"sources", map[string]interface{}{
			"Series": &models.Series{Title: "X"}, "Sources": []interface{}{},
			"TextProviders": []interface{}{},
		}, true},
		{"import_opml", map[string]interface{}{}, true},
		{"version", map[string]interface{}{}, true},
		{"library", map[string]interface{}{
			"ArchiveStats": []interface{}{}, "ArchiveAll": false, "OPDSURL": "http://example/opds",
		}, true},
		{"logs", map[string]interface{}{}, true},
		{"provider_config", map[string]interface{}{
			"Providers": []interface{}{}, "Configs": map[string]string{},
			"Usernames": map[string]string{}, "LoginTested": map[string]bool{},
		}, true},
		{"comic_browse", map[string]interface{}{}, true},
		{"login", map[string]interface{}{}, false},
		{"setup", map[string]interface{}{}, false},
		// fragments (htmx partials) — render but no nav by design
		{"time_page_partial", map[string]interface{}{
			"TimeGroups": []interface{}{}, "TimePage": 1, "HasMoreTime": false,
			"SortBy": "published", "UnreadOnly": false,
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			var buf bytes.Buffer
			if err := tmpl.ExecuteTemplate(&buf, c.name+".html", withCSRF(req, c.data)); err != nil {
				t.Fatalf("execute %s.html: %v", c.name, err)
			}
			if c.wantNav && !strings.Contains(buf.String(), "<nav") {
				t.Errorf("%s.html: nav missing from rendered output", c.name)
			}
		})
	}
}
