package providers

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
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

	u, _ := url.Parse(p.baseURL)
	hasUserCookie := false
	for _, cookie := range p.client.Jar.Cookies(u) {
		if cookie.Name == "xf_user" {
			hasUserCookie = true
			break
		}
	}

	if !hasUserCookie {
		return fmt.Errorf("login failed silently: xf_user cookie not set (blocked by CAPTCHA or Cloudflare?)")
	}

	logging.Info("[%s] successfully logged in as %s", p.name, username)
	return nil
}

func (p *XenForoProvider) FetchSeriesMetadata(rawURL string) (models.Series, error) {
	var series models.Series
	threadURL := p.normalizeThreadURL(rawURL)

	if strings.Contains(rawURL, "threadmarks.rss") {
		return p.fetchMetadataFromRSS(rawURL, threadURL)
	}

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

	doc.Find(".threadmarkListing .threadmarkListing-header, .structItem-threadmark .structItem-title").Each(func(i int, s *goquery.Selection) {
		if series.Summary == "" {
			series.Summary = strings.TrimSpace(s.Text())
		}
	})

	if metaDesc, ok := doc.Find("meta[property='og:description']").Attr("content"); ok && metaDesc != "" {
		series.Summary = strings.TrimSpace(metaDesc)
	}

	if metaImg, ok := doc.Find("meta[property='og:image']").Attr("content"); ok && metaImg != "" {
		series.ImageURL = metaImg
	}

	return series, nil
}

func (p *XenForoProvider) fetchMetadataFromRSS(rssURL, threadURL string) (models.Series, error) {
	var series models.Series

	fp := gofeed.NewParser()
	resp, err := doGet(p.client, rssURL)
	if err != nil {
		return series, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return series, fmt.Errorf("rss feed status %d for %s", resp.StatusCode, rssURL)
	}

	feed, err := fp.Parse(resp.Body)
	if err != nil {
		return series, fmt.Errorf("parsing rss: %w", err)
	}

	title := feed.Title
	if title == "" {
		title = p.extractTitleFromURL(threadURL)
	}

	series = models.Series{
		Title:        title,
		SourceURL:    threadURL,
		ProviderName: p.Name(),
		Status:       "active",
	}

	if feed.Description != "" {
		series.Summary = feed.Description
	}

	if feed.Image != nil && feed.Image.URL != "" {
		series.ImageURL = feed.Image.URL
	}

	return series, nil
}

func (p *XenForoProvider) FetchChapterContent(chapterURL string) (string, error) {
	return p.fetchContentFromReader(chapterURL)
}

func (p *XenForoProvider) FetchComments(chapterURL string) ([]Comment, error) {
	postID := p.extractPostID(chapterURL)
	threadURL := p.normalizeThreadURL(chapterURL)

	targetURL := chapterURL
	if postID != "" {
		targetURL = threadURL + "#post-" + postID
	}

	resp, err := doGet(p.client, targetURL)
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
	doc.Find("article.message").Each(func(i int, s *goquery.Selection) {
		dataContent, _ := s.Attr("data-content")
		messageID, _ := s.Attr("id")
		isChapterPost := false
		if postID != "" {
			isChapterPost = dataContent == "post-"+postID || messageID == "js-post-"+postID || messageID == "post-"+postID
		}
		if isChapterPost {
			return
		}

		bb := s.Find(".message-body .bbWrapper")
		if bb.Length() == 0 {
			return
		}
		html, err := bb.Html()
		if err != nil {
			return
		}
		html = strings.TrimSpace(html)
		if html == "" {
			return
		}

		author := strings.TrimSpace(s.Find(".message-userArrow").Closest(".message-cell").Find(".message-name a").Text())
		if author == "" {
			author = strings.TrimSpace(s.Find(".message-name").Text())
		}

		date := strings.TrimSpace(s.Find("time").First().AttrOr("title", ""))
		if date == "" {
			date = strings.TrimSpace(s.Find(".message-date time").Text())
		}

		avatarSel := s.Find(".message-avatar img")
		avatarURL, _ := avatarSel.Attr("src")
		if avatarURL != "" && !strings.HasPrefix(avatarURL, "http") {
			avatarURL = p.baseURL + "/" + strings.TrimPrefix(avatarURL, "/")
		}

		comments = append(comments, Comment{
			Author:    author,
			Content:   html,
			Date:      date,
			AvatarURL: avatarURL,
		})
	})

	return comments, nil
}

func (p *XenForoProvider) fetchContentFromReader(chapterURL string) (string, error) {
	postID := p.extractPostID(chapterURL)
	threadURL := p.normalizeThreadURL(chapterURL)
	readerURL := strings.TrimSuffix(threadURL, "/") + "/reader"

	if postID == "" {
		return p.fetchContentDirect(chapterURL)
	}

	pageNum := 1
	for {
		pageURL := readerURL
		if pageNum > 1 {
			pageURL = readerURL + "/page-" + strconv.Itoa(pageNum)
		}

		content, found, hasMore, isNonOK, err := p.scanReaderPage(pageURL, postID, pageNum)
		if err != nil {
			return "", err
		}

		if isNonOK {
			logging.Info("[%s] reader page %d returned non-OK for post %s, falling back to direct fetch", p.name, pageNum, postID)
			return p.fetchContentDirect(chapterURL)
		}

		if found {
			logging.Info("[%s] fetched chapter content from reader page %d for post %s (%d chars)", p.name, pageNum, postID, len(content))
			return content, nil
		}

		if !hasMore {
			break
		}
		pageNum++
	}

	logging.Info("[%s] post %s not found in reader mode, falling back to direct fetch", p.name, postID)
	return p.fetchContentDirect(chapterURL)
}

func (p *XenForoProvider) scanReaderPage(pageURL, postID string, currentPage int) (string, bool, bool, bool, error) {
	resp, err := doGet(p.client, pageURL)
	if err != nil {
		return "", false, false, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", false, false, true, nil
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", false, false, false, fmt.Errorf("parsing reader html: %w", err)
	}

	var content string
	var found bool

	doc.Find("article.message").Each(func(i int, s *goquery.Selection) {
		if found {
			return
		}
		dataContent, _ := s.Attr("data-content")
		messageID, _ := s.Attr("id")
		if dataContent == "post-"+postID || messageID == "js-post-"+postID || messageID == "post-"+postID {
			bb := s.Find(".message-body .bbWrapper")
			if bb.Length() > 0 {
				html, err := bb.Html()
				if err == nil {
					content = html
					found = true
				}
			}
		}
	})

	if found {
		return content, true, false, false, nil
	}

	lastPageNum := 0
	doc.Find(".pageNav-page a").Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if n, err := strconv.Atoi(text); err == nil && n > lastPageNum {
			lastPageNum = n
		}
	})

	if lastPageNum > 0 {
		return "", false, currentPage < lastPageNum, false, nil
	}

	if doc.Find("article.message").Length() == 0 {
		return "", false, false, false, nil
	}
	return "", false, false, false, nil
}

func (p *XenForoProvider) fetchContentDirect(chapterURL string) (string, error) {
	resp, err := doGet(p.client, chapterURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("parsing html: %w", err)
	}

	postID := p.extractPostID(chapterURL)
	var target *goquery.Selection

	if postID != "" {
		doc.Find("article.message").EachWithBreak(func(i int, s *goquery.Selection) bool {
			dataContent, _ := s.Attr("data-content")
			messageID, _ := s.Attr("id")
			if dataContent == "post-"+postID || messageID == "js-post-"+postID || messageID == "post-"+postID {
				bb := s.Find(".message-body .bbWrapper")
				if bb.Length() > 0 {
					target = bb
					return false
				}
			}
			return true
		})
	}

	if target == nil || target.Length() == 0 {
		target = doc.Find(".message-body .bbWrapper")
	}
	if target.Length() == 0 {
		target = doc.Find(".message-body")
	}
	if target.Length() == 0 {
		return "", fmt.Errorf("no chapter content found at %s", chapterURL)
	}

	html, err := target.First().Html()
	if err != nil {
		return "", err
	}

	logging.Info("[%s] fetched chapter content from direct HTML %s (%d chars)", p.name, chapterURL, len(html))
	return html, nil
}

func (p *XenForoProvider) extractPostID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	fragment := strings.TrimPrefix(u.Fragment, "post-")
	if fragment != "" {
		return fragment
	}
	return ""
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

	q := u.Query()
	newQ := url.Values{}
	if uid := q.Get("uid"); uid != "" {
		newQ.Set("uid", uid)
	}
	if auth := q.Get("auth"); auth != "" {
		newQ.Set("auth", auth)
	}
	if rss := q.Get("rss"); rss != "" {
		newQ.Set("rss", rss)
	}
	u.RawQuery = newQ.Encode()
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
	u, err := url.Parse(threadURL)
	if err != nil {
		clean := strings.TrimSuffix(threadURL, "/")
		return clean + "/threadmarks.rss"
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/threadmarks.rss"
	return u.String()
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
		if p.RequiresAuth() {
			logging.Info("[%s] RSS feed status %d for %s, falling back to HTML", p.name, resp.StatusCode, rssURL)
			return p.pollUpdatesHTML(series)
		}
		return nil, fmt.Errorf("rss feed status %d for %s", resp.StatusCode, rssURL)
	}

	feed, err := fp.Parse(resp.Body)
	if err != nil {
		if p.RequiresAuth() {
			logging.Info("[%s] RSS parsing failed (%v), falling back to HTML", p.name, err)
			return p.pollUpdatesHTML(series)
		}
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

func (p *XenForoProvider) pollUpdatesHTML(series models.Series) ([]models.Chapter, error) {
	threadmarksURL := p.normalizeThreadURL(series.SourceURL)
	threadmarksURL = strings.TrimSuffix(threadmarksURL, "/") + "/threadmarks"

	resp, err := doGet(p.client, threadmarksURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden && p.RequiresAuth() {
			return nil, fmt.Errorf("authentication required, cookies may have expired")
		}
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	if doc.Find("input[name='login']").Length() > 0 || doc.Find("a[href*='/login/']").Length() > 0 {
		if strings.Contains(doc.Find("title").Text(), "Log in") {
			return nil, fmt.Errorf("authentication required, cookies may have expired")
		}
	}

	var chapters []models.Chapter
	doc.Find(".structItem--threadmark").Each(func(i int, s *goquery.Selection) {
		a := s.Find(".structItem-title a").First()
		title := strings.TrimSpace(a.Text())
		link, _ := a.Attr("href")
		if title == "" || link == "" {
			return
		}
		if !strings.HasPrefix(link, "http") {
			link = p.baseURL + "/" + strings.TrimPrefix(link, "/")
		}

		timeStr, _ := s.Find("time").Attr("datetime")
		pubAt := time.Now()
		if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
			pubAt = t
		} else if timeData, _ := s.Find("time").Attr("data-time"); timeData != "" {
			if unix, err := strconv.ParseInt(timeData, 10, 64); err == nil {
				pubAt = time.Unix(unix, 0)
			}
		}

		chapters = append(chapters, models.Chapter{
			SeriesID:    series.ID,
			Title:       title,
			URL:         link,
			PublishedAt: pubAt,
		})
	})

	if len(chapters) == 0 {
		return nil, fmt.Errorf("no chapters found in HTML fallback")
	}

	return chapters, nil
}
