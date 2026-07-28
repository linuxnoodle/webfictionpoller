// Package noveldex implements a sync plugin for noveldex.io.
//
// Site shape:
//
//   - Series:  https://noveldex.io/series/novel/{slug}
//   - Chapter: https://noveldex.io/series/novel/{slug}/chapter/{number}
//
// NovelDex is a Next.js app behind Cloudflare. Pages embed a
// #​__NEXT_DATA__ JSON blob with the pre-rendered props; we parse it
// defensively (type-asserting each level of the nested map) and fall back
// to DOM selectors when the blob is absent or shaped differently.
//
// Capabilities implemented:
//
//   - base:            Meta + MatchURL
//   - SeriesLister:    FetchSeriesMetadata
//   - Poller:          PollUpdates
//   - ContentFetcher:  FetchChapter
//   - SeriesSearcher:  Search
//   - SeriesBrowser:   BrowseCategories + Browse
//
// Cloudflare: when FLARESOLVERR_URL is set, fetches route through
// FlareSolverr (CF solves ~30-60s each). Otherwise a direct browser-UA GET
// via safefetch is attempted and Cloudflare interstitials surface as a
// distinct error so operators know to wire FlareSolverr.
package noveldex

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

const (
	homepage     = "https://noveldex.io"
	providerName = "noveldex"
)

// chapterPathRe matches /series/{kind}/{slug}/chapter/{number} on NovelDex.
var chapterPathRe = regexp.MustCompile(`^/series/(novel|comic)/([^/]+)/chapter/(\d+)`)

// Provider is the NovelDex plugin instance. One per process; self-registers
// via init() below.
type Provider struct {
	client   *http.Client
	flareURL string // empty => direct fetch only
}

func init() {
	plugin.Default.Register(&Provider{
		client:   &http.Client{Timeout: 90 * time.Second},
		flareURL: flareSolverrURL(),
	})
}

func (p *Provider) Meta() plugin.Meta {
	return plugin.Meta{
		Name:        providerName,
		DisplayName: "NovelDex",
		Kind:        plugin.KindText,
		Homepage:    homepage,
		FaviconURL:  homepage + "/favicon.ico",
		AuthModes:   []plugin.AuthMode{plugin.AuthFlareSolverr},
		// Cloudflare solves are expensive when FlareSolverr is wired; the
		// direct path is cheap. 0.5 rps / burst 1 / concurrency 1 keeps us
		// polite under both regimes.
		Rate:                plugin.RateSpec{RequestsPerSecond: 0.5, Burst: 1, Concurrency: 1},
		PollIntervalDefault: "30m",
	}
}

func (p *Provider) MatchURL(rawURL string) bool {
	if !plugin.HostMatch(rawURL, "noveldex.io") {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	// Match series + chapter pages; the homepage and other sections
	// (/community, /search, /store) are not series URLs and shouldn't match.
	return strings.HasPrefix(u.Path, "/series/") || strings.HasPrefix(u.Path, "/novel/")
}

// fetchDoc retrieves a URL via fetchHTML and returns a parsed goquery
// document. Callers that need the raw HTML (e.g. for __NEXT_DATA__ probes
// independent of the DOM) should call fetchHTML directly.
func (p *Provider) fetchDoc(target string) (*goquery.Document, error) {
	html, err := p.fetchHTML(target)
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("noveldex: parsing html for %s: %w", target, err)
	}
	return doc, nil
}

// compile-time capability assertions.
var (
	_ plugin.Provider       = (*Provider)(nil)
	_ plugin.SeriesLister   = (*Provider)(nil)
	_ plugin.Poller         = (*Provider)(nil)
	_ plugin.ContentFetcher = (*Provider)(nil)
	_ plugin.SeriesSearcher = (*Provider)(nil)
	_ plugin.SeriesBrowser  = (*Provider)(nil)
)
