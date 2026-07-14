package providers

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

var ao3UpdatedRe = regexp.MustCompile(`<!--\s*updated_at=(\d+)\s*-->`)

const ao3MinDelay = 2 * time.Second

var (
	ao3Mu       sync.Mutex
	ao3LastReq  time.Time
)

type AO3Provider struct {
	client *http.Client
}

func NewAO3Provider() *AO3Provider {
	return &AO3Provider{
		client: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

func (p *AO3Provider) Name() string { return "ao3" }

func (p *AO3Provider) ao3Get(url string) (*http.Response, error) {
	ao3Mu.Lock()
	elapsed := time.Since(ao3LastReq)
	if elapsed < ao3MinDelay {
		time.Sleep(ao3MinDelay - elapsed)
	}
	ao3LastReq = time.Now()
	ao3Mu.Unlock()

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "WebfictionPoller/1.0 (bots@archiveofourown.org)")
	return p.client.Do(req)
}

func (p *AO3Provider) MatchURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Host
	return host == "archiveofourown.org" || host == "www.archiveofourown.org"
}

func (p *AO3Provider) RequiresAuth() bool { return false }

func (p *AO3Provider) SetCookies(_ string) error { return nil }

func (p *AO3Provider) SupportsLogin() bool { return false }

func (p *AO3Provider) Login(_, _ string) error { return fmt.Errorf("not supported") }

func (p *AO3Provider) FetchComments(_ string) ([]Comment, error) {
	return nil, nil
}

func (p *AO3Provider) FetchSeriesMetadata(rawURL string) (models.Series, error) {
	if strings.Contains(rawURL, "/series/") {
		return p.fetchSeriesMetadata(rawURL)
	}

	return p.fetchWorkMetadata(rawURL)
}

func (p *AO3Provider) fetchSeriesMetadata(rawURL string) (models.Series, error) {
	var series models.Series
	cleanURL := p.cleanURL(rawURL)
	fetchURL := cleanURL + "?view_adult=true"

	resp, err := p.ao3Get(fetchURL)
	if err != nil {
		return series, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return series, fmt.Errorf("status %d for %s", resp.StatusCode, fetchURL)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return series, fmt.Errorf("parsing html: %w", err)
	}

	title := strings.TrimSpace(doc.Find("h2.heading").First().Text())
	if title == "" {
		title = p.extractTitleFromURL(rawURL)
	}

	author := ""
	doc.Find("dl.series.meta dd a[rel='author']").Each(func(i int, s *goquery.Selection) {
		if i == 0 {
			author = strings.TrimSpace(s.Text())
		}
	})

	desc := ""
	doc.Find("div.series.meta blockquote.userstuff").Each(func(i int, s *goquery.Selection) {
		if i == 0 {
			html, _ := s.Html()
			desc = strings.TrimSpace(html)
		}
	})

	imageURL := ""
	if og, ok := doc.Find("meta[property='og:image']").Attr("content"); ok && og != "" {
		imageURL = og
	}

	series = models.Series{
		Title:        title,
		Author:       author,
		SourceURL:    cleanURL,
		ProviderName: p.Name(),
		Status:       "active",
		Summary:      desc,
		ImageURL:     imageURL,
	}

	return series, nil
}

func (p *AO3Provider) fetchWorkMetadata(rawURL string) (models.Series, error) {
	var series models.Series
	cleanURL := p.cleanURL(rawURL)
	fetchURL := cleanURL + "?view_adult=true"

	resp, err := p.ao3Get(fetchURL)
	if err != nil {
		return series, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return series, fmt.Errorf("status %d for %s", resp.StatusCode, fetchURL)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return series, fmt.Errorf("parsing html: %w", err)
	}

	title := strings.TrimSpace(doc.Find("h2.title.heading").First().Text())
	if title == "" {
		title = p.extractTitleFromURL(rawURL)
	}

	author := ""
	doc.Find("a[rel='author']").Each(func(i int, s *goquery.Selection) {
		if i == 0 {
			author = strings.TrimSpace(s.Text())
		}
	})

	summary := ""
	doc.Find("div.summary.module blockquote.userstuff").Each(func(i int, s *goquery.Selection) {
		if i == 0 {
			html, _ := s.Html()
			summary = strings.TrimSpace(html)
		}
	})

	imageURL := ""
	if og, ok := doc.Find("meta[property='og:image']").Attr("content"); ok && og != "" {
		imageURL = og
	}

	series = models.Series{
		Title:        title,
		Author:       author,
		SourceURL:    cleanURL,
		ProviderName: p.Name(),
		Status:       "active",
		Summary:      summary,
		ImageURL:     imageURL,
	}

	return series, nil
}

func (p *AO3Provider) PollUpdates(series models.Series) ([]models.Chapter, error) {
	if strings.Contains(series.SourceURL, "/series/") {
		return p.pollSeries(series)
	}
	return p.pollWork(series)
}

func (p *AO3Provider) pollSeries(series models.Series) ([]models.Chapter, error) {
	fetchURL := series.SourceURL + "?view_adult=true"
	resp, err := p.ao3Get(fetchURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d for %s", resp.StatusCode, fetchURL)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing html: %w", err)
	}

	var chapters []models.Chapter
	doc.Find("li.work.blurb").Each(func(i int, s *goquery.Selection) {
		link := s.Find("h4.heading a").First()
		href, exists := link.Attr("href")
		if !exists {
			return
		}
		title := strings.TrimSpace(link.Text())
		if !strings.HasPrefix(href, "http") {
			href = "https://archiveofourown.org" + href
		}

		pubAt := time.Now()
		headerHTML, _ := s.Find("div.header.module").Html()
		if m := ao3UpdatedRe.FindStringSubmatch(headerHTML); len(m) == 2 {
			if ts, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				pubAt = time.Unix(ts, 0)
			}
		} else {
			s.Find("p.datetime").Each(func(_ int, dt *goquery.Selection) {
				if t, err := time.Parse("02 Jan 2006", strings.TrimSpace(dt.Text())); err == nil {
					pubAt = t
				}
			})
		}

		chapters = append(chapters, models.Chapter{
			SeriesID:    series.ID,
			Title:       title,
			URL:         href,
			PublishedAt: pubAt,
		})
	})

	if len(chapters) == 0 {
		logging.Info("[ao3] polled series %s: 0 works found (blurb count=%d)", series.SourceURL, doc.Find("li.work.blurb").Length())
	} else {
		logging.Info("[ao3] polled series %s: %d works", series.SourceURL, len(chapters))
	}
	return chapters, nil
}

func (p *AO3Provider) pollWork(series models.Series) ([]models.Chapter, error) {
	fetchURL := series.SourceURL + "?view_adult=true"
	resp, err := p.ao3Get(fetchURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d for %s", resp.StatusCode, fetchURL)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing html: %w", err)
	}

	chapStat := ""
	doc.Find("dd.chapters").Each(func(i int, s *goquery.Selection) {
		if i == 0 {
			chapStat = strings.TrimSpace(s.Text())
		}
	})

	parts := strings.Split(chapStat, "/")
	logging.Info("[ao3] polling work %s: chapStat=%q parts=%d", series.SourceURL, chapStat, len(parts))
	if len(parts) == 2 {
		total, _ := strconv.Atoi(parts[0])
		if total <= 1 {
			return []models.Chapter{{
				SeriesID:    series.ID,
				Title:       strings.TrimSpace(doc.Find("h2.title.heading").First().Text()),
				URL:         series.SourceURL,
				PublishedAt: time.Now(),
			}}, nil
		}
	}

	chapterLinks := doc.Find("select#selected_id option")
	if chapterLinks.Length() > 0 {
		return p.pollMultiChapterWork(series, doc)
	}

	return []models.Chapter{{
		SeriesID:    series.ID,
		Title:       strings.TrimSpace(doc.Find("h2.title.heading").First().Text()),
		URL:         series.SourceURL,
		PublishedAt: time.Now(),
	}}, nil
}

func (p *AO3Provider) pollMultiChapterWork(series models.Series, doc *goquery.Document) ([]models.Chapter, error) {
	var chapters []models.Chapter

	doc.Find("ol#chapter_index li a, div#chapter-index li a").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists {
			return
		}
		title := strings.TrimSpace(s.Text())
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		if !strings.HasPrefix(href, "http") {
			href = "https://archiveofourown.org" + href
		}
		chapters = append(chapters, models.Chapter{
			SeriesID:    series.ID,
			Title:       title,
			URL:         href,
			PublishedAt: time.Now(),
		})
	})

	if len(chapters) > 0 {
		logging.Info("[ao3] polled multi-chapter work %s: %d chapters", series.SourceURL, len(chapters))
		return chapters, nil
	}

	navigateURL := series.SourceURL + "/navigate"
	resp, err := p.ao3Get(navigateURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	navDoc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing navigate page: %w", err)
	}

	navDoc.Find("ol.chapter index a, ol.index a, li a[href*='/chapters/']").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists {
			return
		}
		if !strings.Contains(href, "/chapters/") {
			return
		}
		title := strings.TrimSpace(s.Text())
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		if !strings.HasPrefix(href, "http") {
			href = "https://archiveofourown.org" + href
		}
		chapters = append(chapters, models.Chapter{
			SeriesID:    series.ID,
			Title:       title,
			URL:         href,
			PublishedAt: time.Now(),
		})
	})

	logging.Info("[ao3] polled work %s: %d chapters (via navigate)", series.SourceURL, len(chapters))
	return chapters, nil
}

func (p *AO3Provider) FetchChapterContent(chapterURL string) (string, error) {
	fetchURL := chapterURL
	if !strings.Contains(fetchURL, "?") {
		fetchURL += "?view_adult=true"
	} else {
		fetchURL += "&view_adult=true"
	}

	resp, err := p.ao3Get(fetchURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d for %s", resp.StatusCode, fetchURL)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("parsing html: %w", err)
	}

	content := doc.Find("div.userstuff")
	if content.Length() == 0 {
		content = doc.Find("#chapters div.userstuff")
	}
	if content.Length() == 0 {
		return "", fmt.Errorf("no content found at %s", chapterURL)
	}

	html, err := content.First().Html()
	if err != nil {
		return "", err
	}

	logging.Info("[ao3] fetched chapter content from %s (%d chars)", chapterURL, len(html))
	return html, nil
}

func (p *AO3Provider) cleanURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Del("view_adult")
	q.Del("view_full_work")
	q.Del("show_comments")
	u.RawQuery = q.Encode()
	return u.String()
}

func (p *AO3Provider) extractTitleFromURL(rawURL string) string {
	parts := strings.Split(strings.TrimSuffix(rawURL, "/"), "/")
	for i, part := range parts {
		if (part == "works" || part == "series") && i+1 < len(parts) {
			return part + " " + parts[i+1]
		}
	}
	return rawURL
}
