package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func writeTOML(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDeclarativeProvidersValid(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "site1.toml", `
name = "site1"
display_name = "Site One"
homepage = "https://site1.example"
kind = "text"

[poll]
rss_feed_template = "https://site1.example/rss/{id}"
interval = "20m"

[scrape]
series_title_selector    = "h1.title"
chapter_content_selector = "div.body"
`)

	r := NewRegistry()
	count, errs := LoadDeclarativeProviders(dir, r)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %+v", errs)
	}
	if count != 1 {
		t.Fatalf("expected 1 provider, got %d", count)
	}
	p, ok := r.Get("site1")
	if !ok {
		t.Fatal("provider not registered")
	}
	if p.Meta().DisplayName != "Site One" {
		t.Errorf("DisplayName = %q", p.Meta().DisplayName)
	}
	if !p.MatchURL("https://site1.example/fic/123/x") {
		t.Error("MatchURL should accept site1 host")
	}
	if p.MatchURL("https://other.example/x") {
		t.Error("MatchURL should reject other host")
	}
}

func TestLoadDeclarativeProvidersMissingDir(t *testing.T) {
	r := NewRegistry()
	count, errs := LoadDeclarativeProviders("/nonexistent/path/providers", r)
	if count != 0 {
		t.Errorf("expected 0 providers for missing dir, got %d", count)
	}
	if len(errs) != 0 {
		t.Errorf("expected no errors for missing dir, got %+v", errs)
	}
}

func TestLoadDeclarativeProvidersValidationErrors(t *testing.T) {
	dir := t.TempDir()
	// Missing required fields.
	writeTOML(t, dir, "bad.toml", `display_name = "No name or homepage"
`)
	// No poll/scrape config.
	writeTOML(t, dir, "nochannels.toml", `
name = "nochannels"
homepage = "https://nochannels.example"
`)
	// Invalid kind.
	writeTOML(t, dir, "badkind.toml", `
name = "badkind"
homepage = "https://badkind.example"
kind = "comic"

[poll]
rss_feed_template = "https://badkind.example/rss/{id}"
`)

	r := NewRegistry()
	count, errs := LoadDeclarativeProviders(dir, r)
	if count != 0 {
		t.Errorf("expected 0 valid providers, got %d", count)
	}
	if len(errs) != 3 {
		t.Errorf("expected 3 errors, got %d: %+v", len(errs), errs)
	}
}

func TestLoadDeclarativeProvidersSkipsNonTOML(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "site.toml", `
name = "site"
homepage = "https://site.example"

[poll]
rss_feed_template = "https://site.example/rss/{id}"
`)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	count, errs := LoadDeclarativeProviders(dir, r)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	if count != 1 {
		t.Errorf("expected 1 provider, got %d", count)
	}
}

func TestLoadDeclarativeProvidersDuplicateName(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "first.toml", `
name = "dup"
homepage = "https://first.example"

[poll]
rss_feed_template = "https://first.example/rss/{id}"
`)
	writeTOML(t, dir, "second.toml", `
name = "dup"
homepage = "https://second.example"

[poll]
rss_feed_template = "https://second.example/rss/{id}"
`)
	r := NewRegistry()
	count, errs := LoadDeclarativeProviders(dir, r)
	if count != 1 {
		t.Errorf("expected exactly 1 provider (first wins), got %d", count)
	}
	if len(errs) != 1 {
		t.Errorf("expected 1 error for duplicate, got %d", len(errs))
	}
}

func TestLoadDeclarativeProvidersIgnoresCompiledIn(t *testing.T) {
	dir := t.TempDir()
	// Pre-register a compiled-in provider with the same name.
	r := NewRegistry()
	r.Register(&stubProvider{meta: Meta{Name: "site1", Kind: KindText}})

	writeTOML(t, dir, "site1.toml", `
name = "site1"
homepage = "https://site1.example"

[poll]
rss_feed_template = "https://site1.example/rss/{id}"
`)
	count, errs := LoadDeclarativeProviders(dir, r)
	if count != 0 {
		t.Errorf("expected 0 (name collides with compiled-in), got %d", count)
	}
	if len(errs) != 1 {
		t.Errorf("expected 1 collision error, got %d", len(errs))
	}
}

func TestDeclarativeSpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    DeclarativeSpec
		wantErr string
	}{
		{"empty name", DeclarativeSpec{Homepage: "https://x.example"}, "missing required field `name`"},
		{"empty homepage", DeclarativeSpec{Name: "x"}, "missing required field `homepage`"},
		{"bad homepage", DeclarativeSpec{Name: "x", Homepage: "not a url"}, "not a valid absolute URL"},
		{"no poll config", DeclarativeSpec{Name: "x", Homepage: "https://x.example"}, "must define either"},
		{"ok rss", DeclarativeSpec{
			Name: "x", Homepage: "https://x.example",
			Poll: DeclarativePoll{RSSTemplate: "https://x.example/rss"},
		}, ""},
		{"ok scrape", DeclarativeSpec{
			Name: "x", Homepage: "https://x.example",
			Scrape: DeclarativeScrape{ChapterListSelector: ".ch"},
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.spec.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", c.wantErr)
				return
			}
			if !contains(err.Error(), c.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestSeriesSourceID(t *testing.T) {
	cases := map[string]string{
		"https://example.com/fic/12345/title":    "12345",
		"https://example.com/works/67890":        "67890",
		"https://example.com/s/9/chapters":       "9",
		"https://example.com/numeric-only/42":    "42",
		"https://example.com/no/numbers/here":    "here",
		"https://example.com/":                   "https://example.com/",
	}
	for in, want := range cases {
		got := seriesSourceID(in)
		if got != want {
			t.Errorf("seriesSourceID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeclarativeResolveRSSTemplate(t *testing.T) {
	p := newDeclarativeProvider(DeclarativeSpec{
		Name:     "x",
		Homepage: "https://x.example",
		Poll:     DeclarativePoll{RSSTemplate: "https://x.example/rss/{id}"},
	})
	got := p.resolveRSSTemplate(seriesStubMeta("https://x.example/fic/12345/title"))
	if got != "https://x.example/rss/12345" {
		t.Errorf("resolveRSSTemplate = %q", got)
	}

	// Template without {id} passes through unchanged.
	p2 := newDeclarativeProvider(DeclarativeSpec{
		Poll: DeclarativePoll{RSSTemplate: "https://x.example/feed.xml"},
	})
	if got := p2.resolveRSSTemplate(seriesStubMeta("https://x.example/anything")); got != "https://x.example/feed.xml" {
		t.Errorf("passthrough = %q", got)
	}
}

// seriesStubMeta returns a minimal Series for template resolution tests.
func seriesStubMeta(sourceURL string) models.Series {
	return models.Series{SourceURL: sourceURL}
}

// helper since we can't import models here (cycle).
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
