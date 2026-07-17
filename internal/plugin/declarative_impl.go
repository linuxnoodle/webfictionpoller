package plugin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/safefetch"
)

// declarativeProvider is the runtime implementation backed by a DeclarativeSpec.
// It implements plugin.Provider (Meta + MatchURL) plus the Poller, SeriesLister,
// and HTMLFetcher capabilities. It deliberately does not implement auth,
// comments, or search — declarative providers are read-only RSS+selector scrapers.
type declarativeProvider struct {
	spec DeclarativeSpec
	host string
}

func newDeclarativeProvider(spec DeclarativeSpec) *declarativeProvider {
	return &declarativeProvider{
		spec: spec,
		host: spec.Hostname(),
	}
}

func (p *declarativeProvider) Meta() Meta {
	rate := p.spec.Rate.ToRateSpec()
	authMode := AuthNone
	m := Meta{
		Name:        p.spec.Name,
		DisplayName: p.spec.DisplayName,
		Kind:        KindText,
		Homepage:    p.spec.Homepage,
		FaviconURL:  strings.TrimRight(p.spec.Homepage, "/") + "/favicon.ico",
		AuthModes:   []AuthMode{authMode},
		Rate:        rate,
	}
	if p.spec.Poll.Interval != "" {
		m.PollIntervalDefault = p.spec.Poll.Interval
	}
	if m.DisplayName == "" {
		m.DisplayName = p.spec.Name
	}
	return m
}

func (p *declarativeProvider) MatchURL(rawURL string) bool {
	if p.host == "" {
		return false
	}
	return HostMatch(rawURL, p.host)
}

// absoluteURL resolves a possibly-relative URL against the provider homepage.
func (p *declarativeProvider) absoluteURL(href string) (string, error) {
	if href == "" {
		return "", fmt.Errorf("empty href")
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	if u.IsAbs() {
		return href, nil
	}
	base, err := url.Parse(p.spec.Homepage)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(u).String(), nil
}

// FetchSeriesMetadata implements SeriesLister. Scrapes the series page using
// the configured selectors.
func (p *declarativeProvider) FetchSeriesMetadata(seriesURL string) (models.Series, error) {
	var s models.Series
	s.SourceURL = seriesURL
	s.ProviderName = p.spec.Name

	resp, err := safefetch.Get(seriesURL)
	if err != nil {
		return s, fmt.Errorf("fetching series page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s, fmt.Errorf("series page status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return s, fmt.Errorf("parsing series HTML: %w", err)
	}

	if sel := p.spec.Scrape.SeriesTitleSelector; sel != "" {
		s.Title = strings.TrimSpace(doc.Find(sel).First().Text())
	}
	if sel := p.spec.Scrape.SeriesAuthorSelector; sel != "" {
		s.Author = strings.TrimSpace(doc.Find(sel).First().Text())
	}
	if sel := p.spec.Scrape.SeriesSummarySelector; sel != "" {
		s.Summary = strings.TrimSpace(doc.Find(sel).First().Text())
	}

	if s.Title == "" {
		return s, fmt.Errorf("series title selector %q did not match", p.spec.Scrape.SeriesTitleSelector)
	}
	return s, nil
}

// PollUpdates implements Poller. Prefers RSS when configured; falls back to
// scraping the chapter list selector from the series page.
func (p *declarativeProvider) PollUpdates(series models.Series) ([]models.Chapter, error) {
	if p.spec.Poll.RSSTemplate != "" {
		return p.pollViaRSS(series)
	}
	return p.pollViaScrape(series)
}

func (p *declarativeProvider) pollViaRSS(series models.Series) ([]models.Chapter, error) {
	feedURL := p.resolveRSSTemplate(series)
	fp := gofeed.NewParser()
	feed, err := fp.ParseURL(feedURL)
	if err != nil {
		return nil, fmt.Errorf("parsing RSS %s: %w", feedURL, err)
	}
	chapters := make([]models.Chapter, 0, len(feed.Items))
	for _, item := range feed.Items {
		ch := models.Chapter{
			SeriesID: series.ID,
			Title:    item.Title,
			URL:      item.Link,
		}
		if item.PublishedParsed != nil {
			ch.PublishedAt = *item.PublishedParsed
		} else if item.UpdatedParsed != nil {
			ch.PublishedAt = *item.UpdatedParsed
		}
		if ch.Title == "" {
			ch.Title = item.Title
		}
		chapters = append(chapters, ch)
	}
	return chapters, nil
}

// resolveRSSTemplate substitutes {id} in the template with a per-series
// identifier extracted from the series URL. The id is the last path segment,
// or the whole URL if no path is present.
func (p *declarativeProvider) resolveRSSTemplate(series models.Series) string {
	tmpl := p.spec.Poll.RSSTemplate
	if !strings.Contains(tmpl, "{id}") {
		return tmpl
	}
	id := seriesSourceID(series.SourceURL)
	return strings.ReplaceAll(tmpl, "{id}", url.PathEscape(id))
}

// seriesSourceID extracts the last meaningful path segment. For
// https://example.com/fiction/12345/title it returns "12345". Falls back to
// the entire URL if no numeric segment is found.
func seriesSourceID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// Walk from the end, return the first numeric segment.
	for i := len(parts) - 1; i >= 0; i-- {
		if _, err := url.PathUnescape(parts[i]); err == nil {
			isNum := true
			for _, r := range parts[i] {
				if r < '0' || r > '9' {
					isNum = false
					break
				}
			}
			if isNum && parts[i] != "" {
				return parts[i]
			}
		}
	}
	// No numeric segment; use the last non-empty segment.
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return rawURL
}

func (p *declarativeProvider) pollViaScrape(series models.Series) ([]models.Chapter, error) {
	if p.spec.Scrape.ChapterListSelector == "" {
		return nil, fmt.Errorf("provider %q has no RSS template and no chapter_list_selector", p.spec.Name)
	}
	resp, err := safefetch.Get(series.SourceURL)
	if err != nil {
		return nil, fmt.Errorf("fetching series page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("series page status %d", resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing HTML: %w", err)
	}

	var chapters []models.Chapter
	doc.Find(p.spec.Scrape.ChapterListSelector).Each(func(_ int, sel *goquery.Selection) {
		href, exists := sel.Attr(p.spec.Scrape.ChapterURLAttribute)
		if !exists {
			return
		}
		abs, err := p.absoluteURL(href)
		if err != nil {
			return
		}
		title := strings.TrimSpace(sel.Text())
		if title == "" && p.spec.Scrape.ChapterTitleSelector != "" {
			title = strings.TrimSpace(sel.Find(p.spec.Scrape.ChapterTitleSelector).First().Text())
		}
		chapters = append(chapters, models.Chapter{
			SeriesID: series.ID,
			Title:    title,
			URL:      abs,
		})
	})
	if len(chapters) == 0 {
		logging.Info("[declarative:%s] chapter_list_selector matched 0 elements on %s", p.spec.Name, series.SourceURL)
	}
	return chapters, nil
}

// FetchChapterContent implements HTMLFetcher. Extracts the configured content
// selector from the chapter page. Output is NOT re-sanitized here — callers
// (archiver, reader) run bluemonday downstream, which is the right place to
// enforce policy uniformly across compiled-in and declarative providers.
func (p *declarativeProvider) FetchChapterContent(chapterURL string) (string, error) {
	if p.spec.Scrape.ChapterContentSelector == "" {
		return "", fmt.Errorf("provider %q has no chapter_content_selector; cannot fetch chapter HTML", p.spec.Name)
	}
	resp, err := safefetch.Get(chapterURL)
	if err != nil {
		return "", fmt.Errorf("fetching chapter: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chapter status %d", resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("parsing chapter HTML: %w", err)
	}
	html, err := doc.Find(p.spec.Scrape.ChapterContentSelector).First().Html()
	if err != nil {
		return "", fmt.Errorf("extracting content: %w", err)
	}
	return html, nil
}

// FetchComments always returns nil for declarative providers.
func (p *declarativeProvider) FetchComments(_ string) ([]models.Comment, error) {
	return nil, nil
}

// IsDeclarative marks the provider as TOML-driven so the plugins page can
// badge it distinctly from compiled-in providers. The method is matched by
// interface probe in handlers.isDeclarative (we don't export the type).
func (p *declarativeProvider) IsDeclarative() bool { return true }

// Compile-time capability assertions: declarativeProvider implements Provider
// plus the Poller, SeriesLister, HTMLFetcher, and CommentFetcher interfaces.
var (
	_ Provider        = (*declarativeProvider)(nil)
	_ Poller          = (*declarativeProvider)(nil)
	_ SeriesLister    = (*declarativeProvider)(nil)
	_ HTMLFetcher     = (*declarativeProvider)(nil)
	_ CommentFetcher  = (*declarativeProvider)(nil)
)
