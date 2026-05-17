package providers

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/mmcdole/gofeed"
)

type XenForoProvider struct {
	name     string
	baseURL  string
	domain   string
	client   *http.Client
	requires bool
}

func NewSpaceBattlesProvider() *XenForoProvider {
	return newXenForoProvider("spacebattles", "https://forums.spacebattles.com", "forums.spacebattles.com", false)
}

func NewSufficientVelocityProvider() *XenForoProvider {
	return newXenForoProvider("sufficientvelocity", "https://forums.sufficientvelocity.com", "forums.sufficientvelocity.com", false)
}

func NewQuestionableQuestingProvider() *XenForoProvider {
	return newXenForoProvider("questionablequesting", "https://forum.questionablequesting.com", "forum.questionablequesting.com", true)
}

func newXenForoProvider(name, baseURL, domain string, requiresAuth bool) *XenForoProvider {
	jar, _ := cookiejar.New(nil)
	return &XenForoProvider{
		name:    name,
		baseURL: baseURL,
		domain:  domain,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
		},
		requires: requiresAuth,
	}
}

func (p *XenForoProvider) Name() string { return p.name }

func (p *XenForoProvider) MatchURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Host == p.domain || strings.HasSuffix(u.Host, "."+p.domain)
}

func (p *XenForoProvider) RequiresAuth() bool { return p.requires }

func (p *XenForoProvider) SupportsLogin() bool { return p.requires }

func (p *XenForoProvider) SetCookies(cookieStr string) error {
	if cookieStr == "" {
		return nil
	}
	cookies := p.parseCookies(cookieStr)
	u, _ := url.Parse("https://" + p.domain)
	p.client.Jar.SetCookies(u, cookies)
	return nil
}

func (p *XenForoProvider) parseCookies(raw string) []*http.Cookie {
	var cookies []*http.Cookie
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			cookies = append(cookies, &http.Cookie{
				Name:  strings.TrimSpace(kv[0]),
				Value: strings.TrimSpace(kv[1]),
			})
		}
	}
	return cookies
}

func (p *XenForoProvider) Login(username, password string) error {
	loginURL := p.baseURL + "/login/"
	resp, err := doGet(p.client, loginURL)
	if err != nil {
		return fmt.Errorf("fetching login page: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return fmt.Errorf("parsing login page: %w", err)
	}

	xfToken := ""
	doc.Find("input[name='_xfToken']").Each(func(i int, s *goquery.Selection) {
		if v, ok := s.Attr("value"); ok && v != "" {
			xfToken = v
		}
	})

	form := url.Values{}
	form.Set("login", username)
	form.Set("password", password)
	form.Set("_xfToken", xfToken)
	form.Set("remember", "1")

	req, err := http.NewRequest("POST", p.baseURL+"/login/login", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("creating login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	loginResp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer loginResp.Body.Close()

	if loginResp.StatusCode != http.StatusOK {
		return fmt.Errorf("login returned status %d", loginResp.StatusCode)
	}

	loginDoc, err := goquery.NewDocumentFromReader(loginResp.Body)
	if err != nil {
		return fmt.Errorf("parsing login response: %w", err)
	}

	if loginDoc.Find(".blockMessage--error").Length() > 0 {
		return fmt.Errorf("login failed: invalid credentials")
	}

	logging.Info("[%s] successfully logged in as %s", p.name, username)
	return nil
}

func (p *XenForoProvider) FetchSeriesMetadata(rawURL string) (models.Series, error) {
	var series models.Series
	threadURL := p.normalizeThreadURL(rawURL)

	resp, err := doGet(p.client, threadURL)
	if err != nil {
		return series, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return series, fmt.Errorf("status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return series, fmt.Errorf("parsing html: %w", err)
	}

	title := strings.TrimSpace(doc.Find(".p-title-value h1").Text())
	if title == "" {
		title = strings.TrimSpace(doc.Find("h1.p-title-value").Text())
	}
	if title == "" {
		title = p.extractTitleFromURL(rawURL)
	}

	series = models.Series{
		Title:        title,
		SourceURL:    threadURL,
		ProviderName: p.Name(),
		Status:       "active",
	}
	return series, nil
}

func (p *XenForoProvider) FetchChapterContent(chapterURL string) (string, error) {
	resp, err := doGet(p.client, chapterURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("parsing html: %w", err)
	}

	content := doc.Find(".message-body .bbWrapper")
	if content.Length() == 0 {
		content = doc.Find(".message-body")
	}
	if content.Length() == 0 {
		return "", fmt.Errorf("no chapter content found at %s", chapterURL)
	}

	html, err := content.First().Html()
	if err != nil {
		return "", err
	}

	logging.Info("[%s] fetched chapter content from %s (%d chars)", p.name, chapterURL, len(html))
	return html, nil
}

func (p *XenForoProvider) normalizeThreadURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	pathParts := strings.Split(strings.TrimSuffix(u.Path, "/"), "/")
	var cleanParts []string
	for _, part := range pathParts {
		if part == "unread" || part == "latest" || strings.HasPrefix(part, "page-") || part == "threadmarks.rss" {
			continue
		}
		cleanParts = append(cleanParts, part)
	}
	u.Path = strings.Join(cleanParts, "/")
	if u.Path == "" || u.Path == "/" {
		return rawURL
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (p *XenForoProvider) extractTitleFromURL(rawURL string) string {
	parts := strings.Split(rawURL, "/")
	for i, part := range parts {
		if part == "threads" && i+1 < len(parts) {
			slug := parts[i+1]
			if idx := strings.Index(slug, "."); idx > 0 {
				return strings.ReplaceAll(slug[:idx], "-", " ")
			}
			return strings.ReplaceAll(slug, "-", " ")
		}
	}
	return rawURL
}

func (p *XenForoProvider) buildThreadmarksRSSURL(threadURL string) string {
	clean := strings.TrimSuffix(threadURL, "/")
	return clean + "/threadmarks.rss"
}

func (p *XenForoProvider) PollUpdates(series models.Series) ([]models.Chapter, error) {
	rssURL := p.buildThreadmarksRSSURL(series.SourceURL)

	fp := gofeed.NewParser()
	resp, err := doGet(p.client, rssURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rss feed status %d for %s", resp.StatusCode, rssURL)
	}

	feed, err := fp.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing rss: %w", err)
	}

	var chapters []models.Chapter
	for _, item := range feed.Items {
		pubAt := time.Now()
		if item.PublishedParsed != nil {
			pubAt = *item.PublishedParsed
		}
		chapters = append(chapters, models.Chapter{
			SeriesID:    series.ID,
			Title:       item.Title,
			URL:         item.Link,
			PublishedAt: pubAt,
		})
	}
	return chapters, nil
}
