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
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
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

func (p *RoyalRoadProvider) Meta() plugin.Meta {
	return plugin.Meta{
		Name:              "royalroad",
		DisplayName:       "Royal Road",
		Kind:              plugin.KindText,
		Homepage:          "https://www.royalroad.com",
		FaviconURL:        "https://www.royalroad.com/favicon.ico",
		AuthModes:         []plugin.AuthMode{plugin.AuthNone},
		Rate:              plugin.RateSpec{RequestsPerSecond: 1.0, Burst: 2, Concurrency: 1},
		PollIntervalDefault: "15m",
	}
}

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

func (p *RoyalRoadProvider) FetchComments(url string) ([]Comment, error) {
	resp, err := doGet(p.client, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	var comments []Comment
	doc.Find(".comment, .review-card, .fiction-comment").Each(func(i int, s *goquery.Selection) {
		author := strings.TrimSpace(s.Find(".comment-name, .username, .author a").First().Text())
		content := strings.TrimSpace(s.Find(".comment-content, .comment-body, .text").First().Text())
		date := strings.TrimSpace(s.Find("time").First().AttrOr("title", ""))
		avatarURL, _ := s.Find("img.avatar, img.profile-pic").First().Attr("src")
		if avatarURL != "" && strings.HasPrefix(avatarURL, "//") {
			avatarURL = "https:" + avatarURL
		}
		if content != "" {
			comments = append(comments, Comment{
				Author:    author,
				Content:   content,
				Date:      date,
				AvatarURL: avatarURL,
			})
		}
	})

	return comments, nil
}

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

	doc.Find(".description").Each(func(i int, s *goquery.Selection) {
		if i == 0 {
			series.Summary = strings.TrimSpace(s.Text())
		}
	})

	if cover, ok := doc.Find(".fic-header img").First().Attr("src"); ok && cover != "" {
		if strings.HasPrefix(cover, "//") {
			cover = "https:" + cover
		}
		series.ImageURL = cover
	}

	return series, nil
}

func (p *RoyalRoadProvider) FetchChapterContent(url string) (string, error) {
	c, err := p.FetchChapter(url)
	if err != nil {
		return "", err
	}
	return c.BodyHTML, nil
}

// FetchChapter implements plugin.ContentFetcher. Fetches the chapter page,
// extracts body + title + image list into the canonical ChapterContent
// shape. Title is read from the chapter's h1 (Royal Road renders the
// chapter title in an h1 inside the chapter header).
func (p *RoyalRoadProvider) FetchChapter(url string) (plugin.ChapterContent, error) {
	resp, err := doGet(p.client, url)
	if err != nil {
		return plugin.ChapterContent{}, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return plugin.ChapterContent{}, fmt.Errorf("parsing html: %w", err)
	}

	content := doc.Find(".chapter-content")
	if content.Length() == 0 {
		content = doc.Find(".portlet-body")
	}
	if content.Length() == 0 {
		return plugin.ChapterContent{}, fmt.Errorf("no chapter content found at %s", url)
	}

	html, err := content.First().Html()
	if err != nil {
		return plugin.ChapterContent{}, err
	}

	// Title: prefer an explicit chapter-title element; fall back to the
	// first h1 on the page; last resort the URL path tail.
	title := plugin.TextOrEmpty(doc.Find("h1.font-white").First())
	if title == "" {
		title = plugin.TextOrEmpty(doc.Find("h1").First())
	}

	bodyText := plugin.HTMLToText(html)
	logging.Info("[royalroad] fetched chapter content from %s (%d chars, %d words)",
		url, len(html), plugin.CountWords(bodyText))

	return plugin.ChapterContent{
		Title:     title,
		BodyHTML:  html,
		BodyText:  bodyText,
		WordCount: plugin.CountWords(bodyText),
		Images:    plugin.ExtractImageURLs(content.First(), url),
		SourceURL: url,
	}, nil
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

// Search implements plugin.SeriesSearcher. Scrapes Royal Road's fiction search.
func (p *RoyalRoadProvider) Search(query string, page int) ([]plugin.SeriesSearchResult, error) {
	searchURL := fmt.Sprintf("https://www.royalroad.com/fictions/search?page=%d&search=%s", page, url.QueryEscape(query))
	resp, err := doGet(p.client, searchURL)
	if err != nil {
		return nil, fmt.Errorf("fetching search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing html: %w", err)
	}
	var results []plugin.SeriesSearchResult
	doc.Find(".fiction-list-item, .fiction-list .fiction").Each(func(i int, s *goquery.Selection) {
		titleSel := s.Find("h2 a, .fiction-title a").First()
		title := strings.TrimSpace(titleSel.Text())
		if title == "" {
			return
		}
		href, _ := titleSel.Attr("href")
		if href != "" && !strings.HasPrefix(href, "http") {
			href = "https://www.royalroad.com" + href
		}
		author := strings.TrimSpace(s.Find(".author a").First().Text())
		summary := strings.TrimSpace(s.Find(".fiction-description, .text").First().Text())
		if len(summary) > 300 {
			summary = summary[:300] + "…"
		}
		img, _ := s.Find("img").First().Attr("src")
		if img != "" && strings.HasPrefix(img, "//") {
			img = "https:" + img
		}
		// Rating
		var rating float64
		if rText, ok := s.Find(".star").First().Attr("data-rating"); ok && rText != "" {
			fmt.Sscanf(rText, "%f", &rating)
		}
		if rating == 0 {
			if metaText := s.Find(".fiction-meta .stars").Text(); metaText != "" {
				fmt.Sscanf(metaText, "%f", &rating)
			}
		}
		// Tags
		var tags []string
		s.Find(".tags .tag, .fiction-tags .tag").Each(func(j int, t *goquery.Selection) {
			if tag := strings.TrimSpace(t.Text()); tag != "" {
				tags = append(tags, tag)
			}
		})
		status := strings.TrimSpace(s.Find(".fiction-meta .status").Text())
		results = append(results, plugin.SeriesSearchResult{
			Title: title, SourceURL: href, Author: author, Summary: summary,
			ImageURL: img, Rating: rating, Status: status, Tags: tags,
		})
	})
	return results, nil
}

// BrowseCategories implements plugin.SeriesBrowser. Royal Road exposes a
// fixed set of "best of" listing pages under /fictions/<key>.
func (p *RoyalRoadProvider) BrowseCategories() []plugin.BrowseCategory {
	return []plugin.BrowseCategory{
		{Key: "trending", Label: "Trending"},
		{Key: "best-rated", Label: "Best Rated"},
		{Key: "popular", Label: "Popular"},
		{Key: "latest-updates", Label: "Latest Updates"},
	}
}

// Browse implements plugin.SeriesBrowser. It scrapes a Royal Road listing
// page (trending, best-rated, etc.) using the same selectors as Search.
func (p *RoyalRoadProvider) Browse(category string, page int) ([]plugin.SeriesBrowseResult, error) {
	if page < 1 {
		page = 1
	}
	browseURL := fmt.Sprintf("https://www.royalroad.com/fictions/%s?page=%d", category, page)
	resp, err := doGet(p.client, browseURL)
	if err != nil {
		return nil, fmt.Errorf("fetching browse: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing html: %w", err)
	}
	var results []plugin.SeriesBrowseResult
	doc.Find(".fiction-list-item, .fiction-list .fiction").Each(func(i int, s *goquery.Selection) {
		titleSel := s.Find("h2 a, .fiction-title a").First()
		title := strings.TrimSpace(titleSel.Text())
		if title == "" {
			return
		}
		href, _ := titleSel.Attr("href")
		if href != "" && !strings.HasPrefix(href, "http") {
			href = "https://www.royalroad.com" + href
		}
		author := strings.TrimSpace(s.Find(".author a").First().Text())
		summary := strings.TrimSpace(s.Find(".fiction-description, .text").First().Text())
		if len(summary) > 300 {
			summary = summary[:300] + "…"
		}
		img, _ := s.Find("img").First().Attr("src")
		if img != "" && strings.HasPrefix(img, "//") {
			img = "https:" + img
		}
		var rating float64
		if rText, ok := s.Find(".star").First().Attr("data-rating"); ok && rText != "" {
			fmt.Sscanf(rText, "%f", &rating)
		}
		if rating == 0 {
			if metaText := s.Find(".fiction-meta .stars").Text(); metaText != "" {
				fmt.Sscanf(metaText, "%f", &rating)
			}
		}
		var tags []string
		s.Find(".tags .tag, .fiction-tags .tag").Each(func(j int, t *goquery.Selection) {
			if tag := strings.TrimSpace(t.Text()); tag != "" {
				tags = append(tags, tag)
			}
		})
		status := strings.TrimSpace(s.Find(".fiction-meta .status").Text())
		results = append(results, plugin.SeriesBrowseResult{
			Title: title, SourceURL: href, Author: author, Summary: summary,
			ImageURL: img, Rating: rating, Status: status, Tags: tags,
		})
	})
	return results, nil
}
