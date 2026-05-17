package providers

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/mmcdole/gofeed"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

type RoyalRoadProvider struct {
	client *http.Client
}

func NewRoyalRoadProvider() *RoyalRoadProvider {
	return &RoyalRoadProvider{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (p *RoyalRoadProvider) Name() string { return "royalroad" }

func (p *RoyalRoadProvider) MatchURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Host
	return host == "royalroad.com" || host == "www.royalroad.com" || strings.HasSuffix(host, ".royalroad.com")
}

func (p *RoyalRoadProvider) RequiresAuth() bool { return false }

func (p *RoyalRoadProvider) SetCookies(_ string) error { return nil }

func (p *RoyalRoadProvider) SupportsLogin() bool { return false }

func (p *RoyalRoadProvider) Login(_, _ string) error { return fmt.Errorf("not supported") }

func (p *RoyalRoadProvider) FetchSeriesMetadata(url string) (models.Series, error) {
	var series models.Series
	resp, err := doGet(p.client, url)
	if err != nil {
		return series, fmt.Errorf("fetching page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return series, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return series, fmt.Errorf("parsing html: %w", err)
	}

	series.Title = doc.Find(".fic-title h1").Text()
	series.Author = strings.TrimSpace(doc.Find(".fic-header .author a").Text())
	series.SourceURL = url
	series.ProviderName = p.Name()
	series.Status = "active"

	return series, nil
}

func (p *RoyalRoadProvider) FetchChapterContent(url string) (string, error) {
	resp, err := doGet(p.client, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("parsing html: %w", err)
	}

	content := doc.Find(".chapter-content")
	if content.Length() == 0 {
		content = doc.Find(".portlet-body")
	}
	if content.Length() == 0 {
		return "", fmt.Errorf("no chapter content found at %s", url)
	}

	html, err := content.First().Html()
	if err != nil {
		return "", err
	}

	logging.Info("[royalroad] fetched chapter content from %s (%d chars)", url, len(html))
	return html, nil
}

func (p *RoyalRoadProvider) PollUpdates(series models.Series) ([]models.Chapter, error) {
	rssURL := p.buildRSSURL(series.SourceURL)
	if rssURL != "" {
		chapters, err := p.pollRSS(rssURL, series.ID)
		if err == nil {
			return chapters, nil
		}
		logging.Error("[royalroad] RSS poll failed for series %d, falling back to scrape: %v", series.ID, err)
	}

	return p.pollScrape(series)
}

func (p *RoyalRoadProvider) buildRSSURL(fictionURL string) string {
	parts := strings.Split(fictionURL, "/")
	for i, part := range parts {
		if part == "fiction" && i+1 < len(parts) {
			fictionID := strings.Split(parts[i+1], "-")[0]
			return fmt.Sprintf("https://www.royalroad.com/syndication/%s", fictionID)
		}
	}
	return ""
}

func (p *RoyalRoadProvider) pollRSS(rssURL string, seriesID int64) ([]models.Chapter, error) {
	fp := gofeed.NewParser()
	resp, err := doGet(p.client, rssURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, nil
	}

	feed, err := fp.Parse(resp.Body)
	if err != nil {
		return nil, err
	}

	var chapters []models.Chapter
	for _, item := range feed.Items {
		pubAt := time.Now()
		if item.PublishedParsed != nil {
			pubAt = *item.PublishedParsed
		}
		ch := models.Chapter{
			SeriesID:    seriesID,
			Title:       item.Title,
			URL:         item.Link,
			PublishedAt: pubAt,
		}
		chapters = append(chapters, ch)
	}
	return chapters, nil
}

func (p *RoyalRoadProvider) pollScrape(series models.Series) ([]models.Chapter, error) {
	resp, err := doGet(p.client, series.SourceURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	var chapters []models.Chapter
	doc.Find("#chapters .chapter-row").Each(func(i int, s *goquery.Selection) {
		link := s.Find("a")
		href, exists := link.Attr("href")
		if !exists {
			return
		}
		title := strings.TrimSpace(link.Text())
		timeStr, _ := s.Find("time").Attr("datetime")
		pubAt := time.Now()
		if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
			pubAt = t
		}
		if !strings.HasPrefix(href, "http") {
			href = "https://www.royalroad.com" + href
		}
		chapters = append(chapters, models.Chapter{
			SeriesID:    series.ID,
			Title:       title,
			URL:         href,
			PublishedAt: pubAt,
		})
	})
	return chapters, nil
}
