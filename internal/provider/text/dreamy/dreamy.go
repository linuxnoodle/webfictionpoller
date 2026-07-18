// Package dreamy implements a sync plugin for dreamy-translations.com.
//
// Site shape (verified from live captures):
//
//   - Story page:   https://dreamy-translations.com/novel/{slug}
//     Contains the full free-chapter list as <a data-chapter-index="N">.
//     Premium chapters are not in the list view (filtered out by the site).
//   - Chapter page: https://dreamy-translations.com/novel/{slug}/chapter/{N}
//     Title in <main> <button> ... <span>; body in <main> <article class="chapter-content">.
//
// The plugin implements four capabilities:
//
//   - base:          Meta + MatchURL
//   - SeriesLister:  FetchSeriesMetadata (title, author, synopsis, cover)
//   - Poller:        PollUpdates (chapter list; free only by construction)
//   - ContentFetcher: FetchChapter -> canonical ChapterContent
//
// No comments capability (the site has no comments). No auth (free content
// only); premium chapters surface as ChapterContent{Premium: true} when
// encountered at fetch time.
//
// Cloudflare note: the site is behind Cloudflare but a plain HTTP GET with
// a browser User-Agent returns the real HTML. safefetch sends that UA. If
// Cloudflare starts challenging, switch FLARESOLVERR_URL on and the plugin
// will detect the challenge page and surface a clear error.
package dreamy

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
	"github.com/linuxnoodle/webfictionpoller/internal/safefetch"
)

const (
	homepage    = "https://dreamy-translations.com"
	providerName = "dreamytranslations"
)

// slugRe extracts the novel slug from any URL shape under /novel/.
var slugRe = regexp.MustCompile(`^/novel/([^/?#]+)/?(?:chapter/([^/?#]+))?/?$`)

// Provider is the dreamy-translations plugin instance. One per process;
// self-registers via init() below.
type Provider struct {
	client *http.Client
}

func init() {
	plugin.Default.Register(&Provider{
		client: &http.Client{Timeout: 30 * time.Second},
	})
}

func (p *Provider) Meta() plugin.Meta {
	return plugin.Meta{
		Name:                providerName,
		DisplayName:         "Dreamy Translations",
		Kind:                plugin.KindText,
		Homepage:            homepage,
		FaviconURL:          homepage + "/favicon.ico",
		AuthModes:           []plugin.AuthMode{plugin.AuthNone},
		Rate:                plugin.RateSpec{RequestsPerSecond: 0.5, Burst: 1, Concurrency: 1},
		PollIntervalDefault: "1h", // releases are scheduled, hourly is plenty
	}
}

func (p *Provider) MatchURL(rawURL string) bool {
	// Host must be dreamy-translations.com AND path must start with /novel/.
	// The site has other sections (/series, /store, /latest) that aren't
	// novel URLs and shouldn't match.
	if !plugin.HostMatch(rawURL, "dreamy-translations.com") {
		return false
	}
	u, err := parseURL(rawURL)
	if err != nil {
		return false
	}
	return strings.HasPrefix(u.Path, "/novel/")
}

// slugFromURL extracts "imc" from "/novel/imc" or "/novel/imc/chapter/0".
// Returns "" + false when the URL is not a novel URL.
func slugFromURL(rawURL string) (string, bool) {
	u, err := parseURL(rawURL)
	if err != nil {
		return "", false
	}
	m := slugRe.FindStringSubmatch(u.Path)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// storyURL builds the absolute story URL for a slug.
func storyURL(slug string) string { return homepage + "/novel/" + slug }

// chapterURL builds the absolute chapter URL for a slug + index.
func chapterURL(slug string, index int) string {
	return homepage + "/novel/" + slug + "/chapter/" + strconv.Itoa(index)
}

// fetchDoc retrieves a URL via safefetch (SSRF-guarded, browser UA) and
// returns a parsed goquery document. Detects Cloudflare challenge pages
// and surfaces a clear error so the operator knows to wire FlareSolverr.
func (p *Provider) fetchDoc(url string) (*goquery.Document, error) {
	resp, err := safefetch.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
		// Cloudflare challenge pages return 403 or 503 with the interstitial.
		body, _ := readSnippet(resp.Body, 4096)
		if isCloudflareChallenge(body) {
			return nil, errCloudflare
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errStatus(url, resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}
	// Also detect the challenge in the body even when status is 200 (some
	// CF configurations serve the interstitial with 200).
	if title := strings.ToLower(strings.TrimSpace(doc.Find("title").First().Text())); strings.Contains(title, "just a moment") {
		return nil, errCloudflare
	}
	return doc, nil
}

// FetchComments returns nil — the site has no per-chapter comments.
func (p *Provider) FetchComments(_ string) ([]models.Comment, error) {
	return nil, nil
}

// compile-time capability assertions.
var (
	_ plugin.Provider         = (*Provider)(nil)
	_ plugin.SeriesLister     = (*Provider)(nil)
	_ plugin.Poller           = (*Provider)(nil)
	_ plugin.ContentFetcher   = (*Provider)(nil)
	_ plugin.CommentFetcher   = (*Provider)(nil)
)

// errCloudflare is returned when a fetch hits a Cloudflare challenge page.
// Callers should surface this distinctly so operators know to set
// FLARESOLVERR_URL (not yet wired for this provider; future work).
var errCloudflare = newTypedErr("cloudflare challenge detected — set FLARESOLVERR_URL if this persists")

func newTypedErr(msg string) error {
	logging.Error("[dreamy] %s", msg)
	return &pluginError{msg: msg}
}

type pluginError struct{ msg string }

func (e *pluginError) Error() string { return e.msg }

func isCloudflareChallenge(body string) bool {
	return strings.Contains(body, "Just a moment...") ||
		strings.Contains(body, "cf-challenge") ||
		strings.Contains(body, "cdn-cgi/challenge")
}

func errStatus(url string, code int) error {
	return &pluginError{msg: "dreamy: " + url + " returned status " + strconv.Itoa(code)}
}
