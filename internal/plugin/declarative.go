// declarative.go implements the TOML-driven generic provider. It lets users
// add support for "simple" sites (RSS feed + CSS selectors) without writing
// Go code or recompiling. Complex sites (QQ auth, FFN FlareSolverr, MangaDex
// API) still need a compiled-in provider.
//
// TOML files live in data/providers/*.toml and are loaded at startup. The
// package scans the directory, parses each file, and registers a
// DeclarativeProvider for each via plugin.Default.Register.
//
// All output from declarative scrapers is routed through safefetch (SSRF
// guard) and bluemonday (HTML sanitizer) — never raw-inserted into responses.

package plugin

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// DeclarativeSpec is the parsed TOML representation. Field names match the
// TOML keys exactly (lowercase, snake_case) via struct tags.
type DeclarativeSpec struct {
	Name        string `toml:"name"`
	DisplayName string `toml:"display_name"`
	Homepage    string `toml:"homepage"`
	Kind        string `toml:"kind"` // "text" only for now

	Poll   DeclarativePoll   `toml:"poll"`
	Scrape DeclarativeScrape `toml:"scrape"`

	// Rate is optional; zero falls back to the same defaults as compiled-in
	// providers (1 RPS / burst 1 / concurrency 1).
	Rate DeclarativeRate `toml:"rate"`
}

// DeclarativePoll configures update polling.
type DeclarativePoll struct {
	// RSSTemplate is the feed URL template. The literal "{id}" is replaced
	// with the series source ID extracted from the add-by-URL flow. If empty,
	// PollUpdates falls back to scraping the chapter list selector from the
	// series page.
	RSSTemplate string `toml:"rss_feed_template"`
	// Interval is the suggested polling interval (e.g. "30m"). Optional.
	Interval string `toml:"interval"`
}

// DeclarativeScrape holds CSS selectors used to extract structured data
// from raw HTML. Selectors are goquery-compatible Cascadia expressions.
type DeclarativeScrape struct {
	SeriesTitleSelector    string `toml:"series_title_selector"`
	SeriesAuthorSelector   string `toml:"series_author_selector"`
	SeriesSummarySelector  string `toml:"series_summary_selector"`
	ChapterListSelector    string `toml:"chapter_list_selector"`
	ChapterTitleSelector   string `toml:"chapter_title_selector"`
	ChapterContentSelector string `toml:"chapter_content_selector"`
	// ChapterURLAttribute is the attribute on chapter_list_selector matches
	// that yields the chapter URL. Defaults to "href".
	ChapterURLAttribute string `toml:"chapter_url_attribute"`
}

// DeclarativeRate is the user-facing subset of RateSpec.
type DeclarativeRate struct {
	RequestsPerSecond float64 `toml:"requests_per_second"`
	Burst             int     `toml:"burst"`
	Concurrency       int     `toml:"concurrency"`
}

// ToRateSpec converts the declarative rate to a plugin.RateSpec.
func (d DeclarativeRate) ToRateSpec() RateSpec {
	return RateSpec{
		RequestsPerSecond: d.RequestsPerSecond,
		Burst:             d.Burst,
		Concurrency:       d.Concurrency,
	}
}

// Validate returns an error if the spec is missing required fields or has
// contradictory settings. Called at load time so a typo in a TOML file
// surfaces immediately rather than at first use.
func (s DeclarativeSpec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("declarative provider: missing required field `name`")
	}
	if s.Homepage == "" {
		return fmt.Errorf("provider %q: missing required field `homepage`", s.Name)
	}
	u, err := url.Parse(s.Homepage)
	if err != nil || u.Host == "" {
		return fmt.Errorf("provider %q: homepage %q is not a valid absolute URL", s.Name, s.Homepage)
	}
	if s.Kind != "" && s.Kind != string(KindText) {
		return fmt.Errorf("provider %q: kind %q unsupported (declarative providers are text-only for now)", s.Name, s.Kind)
	}
	if s.Poll.RSSTemplate == "" && s.Scrape.ChapterListSelector == "" {
		return fmt.Errorf("provider %q: must define either poll.rss_feed_template or scrape.chapter_list_selector", s.Name)
	}
	return nil
}

// Hostname extracts the registered hostname (without scheme) for use in
// MatchURL. Cached at parse time so MatchURL stays allocation-free.
func (s DeclarativeSpec) Hostname() string {
	u, err := url.Parse(s.Homepage)
	if err != nil {
		return ""
	}
	return u.Host
}

// LoadDeclarativeProviders scans dir for *.toml files, parses each into a
// DeclarativeSpec, validates it, and registers a provider for each into the
// supplied registry. Returns the count registered and a slice of errors for
// files that failed validation (registration of the rest proceeds).
//
// Missing dir is not an error — it just means no declarative providers.
func LoadDeclarativeProviders(dir string, reg *Registry) (int, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, []error{fmt.Errorf("scanning %s: %w", dir, err)}
	}

	registered := 0
	var errs []error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".toml") {
			continue
		}
		path := filepath.Join(dir, name)
		spec, err := parseDeclarativeFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if err := spec.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if _, exists := reg.Get(spec.Name); exists {
			errs = append(errs, fmt.Errorf("%s: provider %q already registered (compiled-in or duplicate TOML)", name, spec.Name))
			continue
		}
		reg.Register(newDeclarativeProvider(spec))
		registered++
	}
	return registered, errs
}

func parseDeclarativeFile(path string) (DeclarativeSpec, error) {
	var spec DeclarativeSpec
	if _, err := toml.DecodeFile(path, &spec); err != nil {
		return spec, fmt.Errorf("decoding TOML: %w", err)
	}
	// Apply defaults.
	if spec.Kind == "" {
		spec.Kind = string(KindText)
	}
	if spec.Scrape.ChapterURLAttribute == "" {
		spec.Scrape.ChapterURLAttribute = "href"
	}
	return spec, nil
}
