package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
	"github.com/mmcdole/gofeed"
)

var errAuthRequired = errors.New("authentication required, cookies may have expired")

const reloginCooldown = 5 * time.Minute

type XenForoProvider struct {
	name     string
	baseURL  string
	domain   string
	client   *http.Client
	requires bool

	credSource func() (username, password string, ok bool)

	loginMu          sync.Mutex
	lastLoginAttempt time.Time
	lastLoginOK      bool
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

func (p *XenForoProvider) Meta() plugin.Meta {
	meta := plugin.Meta{
		Name:        p.name,
		Kind:        plugin.KindText,
		Homepage:    p.baseURL,
		FaviconURL:  p.baseURL + "/favicon.ico",
		Rate:        plugin.RateSpec{RequestsPerSecond: 0.5, Burst: 1, Concurrency: 1},
		PollIntervalDefault: "15m",
	}
	switch p.name {
	case "spacebattles":
		meta.DisplayName = "SpaceBattles"
	case "sufficientvelocity":
		meta.DisplayName = "Sufficient Velocity"
	case "questionablequesting":
		meta.DisplayName = "Questionable Questing"
	default:
		meta.DisplayName = p.name
	}
	if p.requires {
		meta.AuthModes = []plugin.AuthMode{plugin.AuthLogin, plugin.AuthCookies}
	} else {
		meta.AuthModes = []plugin.AuthMode{plugin.AuthNone}
	}
	return meta
}

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

func (p *XenForoProvider) SetCredentialSource(fn func() (username, password string, ok bool)) {
	p.credSource = fn
}

func (p *XenForoProvider) tryRelogin() bool {
	if !p.RequiresAuth() || p.credSource == nil {
		return false
	}
	p.loginMu.Lock()
	defer p.loginMu.Unlock()

	now := time.Now()
	if now.Sub(p.lastLoginAttempt) < reloginCooldown {
		return p.lastLoginOK
	}

	p.lastLoginAttempt = now
	username, password, ok := p.credSource()
	if !ok {
		p.lastLoginOK = false
		return false
	}
	if err := p.Login(username, password); err != nil {
		logging.Error("[%s] automatic re-login failed: %v", p.name, err)
		p.lastLoginOK = false
		return false
	}
	p.lastLoginOK = true
	logging.Info("[%s] automatic re-login succeeded", p.name)
	return true
}

func (p *XenForoProvider) withRelogin(fn func() error) error {
	err := fn()
	if err == nil || !errors.Is(err, errAuthRequired) {
		return err
	}
	if !p.tryRelogin() {
		return err
	}
	return fn()
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
	c, err := p.FetchChapter(chapterURL)
	if err != nil {
		return "", err
	}
	return c.BodyHTML, nil
}

// FetchChapter implements plugin.ContentFetcher. Wraps the existing
// reader-mode / direct-fetch logic and adds title + image extraction.
// Title comes from the threadmark label (XenForo exposes it in the post's
// threadmark element) or the chapter URL's anchor text as a fallback.
func (p *XenForoProvider) FetchChapter(chapterURL string) (plugin.ChapterContent, error) {
	var rawHTML string
	err := p.withRelogin(func() error {
		var innerErr error
		rawHTML, innerErr = p.fetchContentDirect(chapterURL)
		return innerErr
	})
	if err != nil {
		return plugin.ChapterContent{}, err
	}
	bodyText := plugin.HTMLToText(rawHTML)
	logging.Info("[xenforo:%s] fetched chapter from %s (%d chars, %d words)",
		p.name, chapterURL, len(rawHTML), plugin.CountWords(bodyText))
	return plugin.ChapterContent{
		BodyHTML:  rawHTML,
		BodyText:  bodyText,
		WordCount: plugin.CountWords(bodyText),
		Images:    p.extractContentImages(rawHTML, chapterURL),
		SourceURL: chapterURL,
	}, nil
}

// extractContentImages pulls every img src out of an HTML body string.
// The reader-mode / direct-fetch paths return a string rather than a
// goquery selection, so we re-parse to harvest image URLs.
func (p *XenForoProvider) extractContentImages(html, baseURL string) []string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	return plugin.ExtractImageURLs(doc.Find("body"), baseURL)
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
	// Use the post-redirect URL (/posts/POSTID/) which always serves the
	// correct page. The stored chapter URL may have a stale page number
	// (e.g. /page-11) if the thread grew and posts shifted pages. The
	// /posts/POSTID/ endpoint redirects to the right page automatically.
	postID := p.extractPostID(chapterURL)
	fetchURL := chapterURL
	if postID != "" {
		fetchURL = p.baseURL + "/posts/" + postID + "/"
	}

	resp, err := doGet(p.client, fetchURL)
	if err != nil {
		return "", err
	}

	// Cloudflare challenge detection — SpaceBattles/SV/QQ are behind CF
	// managed challenges. When we get a 403 or the body contains the CF
	// interstitial, fall back to FlareSolverr to solve the JS challenge.
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if isCloudflareChallenge(string(body)) {
			logging.Info("[xenforo:%s] Cloudflare challenge detected, using FlareSolverr", p.name)
			html, fsErr := p.solveViaFlareSolverr(fetchURL)
			if fsErr != nil {
				return "", fmt.Errorf("cloudflare blocked and FlareSolverr failed: %w", fsErr)
			}
			return p.parseChapterHTML(html, chapterURL)
		}
		// Non-CF 403: auth issue for QQ.
		if p.RequiresAuth() {
			return "", errAuthRequired
		}
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, chapterURL)
	}

	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden && p.RequiresAuth() {
		return "", errAuthRequired
	}

	// Also detect CF challenge on 200 responses (some CF configs return 200)
	rawHTML, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if isCloudflareChallenge(string(rawHTML)) {
		logging.Info("[xenforo:%s] Cloudflare challenge on 200, using FlareSolverr", p.name)
		html, fsErr := p.solveViaFlareSolverr(fetchURL)
		if fsErr != nil {
			return "", fmt.Errorf("cloudflare blocked and FlareSolverr failed: %w", fsErr)
		}
		rawHTML = []byte(html)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(rawHTML)))
	if err != nil {
		return "", fmt.Errorf("parsing html: %w", err)
	}

	if p.RequiresAuth() {
		if doc.Find("input[name='login']").Length() > 0 || doc.Find("a[href*='/login/']").Length() > 0 {
			if strings.Contains(doc.Find("title").Text(), "Log in") {
				return "", errAuthRequired
			}
		}
	}

	// Find the exact post by data-content (XF2) or id (XF1).
	// Based on fiction-dl's approach: no reader-mode, just a direct fetch
	// and a targeted selector. The old reader-mode pagination was fragile
	// on modern XenForo and caused 'No content available' on SB/SV/QQ.
	var target *goquery.Selection
	if postID != "" {
		target = doc.Find(fmt.Sprintf(`article[data-content="post-%s"]`, postID)).First()
		if target.Length() == 0 {
			target = doc.Find(fmt.Sprintf("#post-%s", postID)).First()
		}
		if target.Length() == 0 {
			target = doc.Find(fmt.Sprintf("#js-post-%s", postID)).First()
		}
	}

	if target.Length() == 0 {
		return "", fmt.Errorf("post %s not found on page %s", postID, chapterURL)
	}

	// Extract body: div.bbWrapper (XF2) or div.messageContent (XF1).
	body := target.Find("div.bbWrapper")
	if body.Length() == 0 {
		body = target.Find("div.messageContent")
	}
	if body.Length() == 0 {
		return "", fmt.Errorf("post body not found for post %s at %s", postID, chapterURL)
	}

	html, err := body.First().Html()
	if err != nil {
		return "", err
	}

	logging.Info("[%s] fetched chapter content from %s (post-%s, %d chars)", p.name, chapterURL, postID, len(html))
	return html, nil
}

// solveViaFlareSolverr uses the FlareSolverr sidecar to bypass Cloudflare
// challenges. Returns the raw HTML body of the target URL.
func (p *XenForoProvider) solveViaFlareSolverr(targetURL string) (string, error) {
	fsURL := os.Getenv("FLARESOLVERR_URL")
	if fsURL == "" {
		fsURL = "http://flaresolverr:8191"
	}
	client := &http.Client{Timeout: 90 * time.Second}
	payload := map[string]interface{}{
		"cmd":        "request.get",
		"url":        targetURL,
		"maxTimeout": 60000,
	}
	body, _ := json.Marshal(payload)
	resp, err := client.Post(fsURL+"/v1", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("flaresolverr request: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		Status   string 
		Solution struct {
			Response string 
		} 
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding flaresolverr response: %w", err)
	}
	if result.Status != "ok" {
		return "", fmt.Errorf("flaresolverr status: %s", result.Status)
	}
	return result.Solution.Response, nil
}

// parseChapterHTML parses raw HTML into chapter content using the same
// post-targeting logic as fetchContentDirect. Extracted so the
// FlareSolverr path can reuse it.
func (p *XenForoProvider) parseChapterHTML(rawHTML, chapterURL string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
	if err != nil {
		return "", fmt.Errorf("parsing html: %w", err)
	}
	if p.RequiresAuth() {
		if doc.Find("input[name='login']").Length() > 0 || doc.Find("a[href*='/login/']").Length() > 0 {
			if strings.Contains(doc.Find("title").Text(), "Log in") {
				return "", errAuthRequired
			}
		}
	}
	postID := p.extractPostID(chapterURL)
	var target *goquery.Selection
	if postID != "" {
		target = doc.Find(fmt.Sprintf(`article[data-content="post-%s"]`, postID)).First()
		if target.Length() == 0 {
			target = doc.Find(fmt.Sprintf("#post-%s", postID)).First()
		}
		if target.Length() == 0 {
			target = doc.Find(fmt.Sprintf("#js-post-%s", postID)).First()
		}
	}
	if target.Length() == 0 {
		return "", fmt.Errorf("post %s not found on page %s", postID, chapterURL)
	}
	body := target.Find("div.bbWrapper")
	if body.Length() == 0 {
		body = target.Find("div.messageContent")
	}
	if body.Length() == 0 {
		return "", fmt.Errorf("post body not found for post %s at %s", postID, chapterURL)
	}
	html, err := body.First().Html()
	if err != nil {
		return "", err
	}
	logging.Info("[%s] fetched chapter content from %s (post-%s, %d chars)", p.name, chapterURL, postID, len(html))
	return html, nil
}

func isCloudflareChallenge(body string) bool {
	return strings.Contains(body, "Just a moment...") ||
		strings.Contains(body, "cf-challenge") ||
		strings.Contains(body, "cdn-cgi/challenge")
}

// hasChapterNumber reports whether the title already contains a chapter number,
// either at the start ("Chapter 5", "5. Title", "Ch. 5") or embedded
// ("Part 5: Something"). Used to avoid prepending a redundant number.
func hasChapterNumber(title string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	if title == "" {
		return false
	}
	// Starts with a digit.
	if title[0] >= '0' && title[0] <= '9' {
		return true
	}
	// Contains 'chapter N', 'ch. N', 'part N', 'epilogue', 'prologue'.
	numberWords := []string{"chapter ", "ch. ", "ch ", "part ", "epilogue", "prologue", "interlude"}
	for _, w := range numberWords {
		if strings.Contains(title, w) {
			return true
		}
	}
	// Contains a number followed by ':' or '.'.
	for i := 0; i < len(title); i++ {
		if title[i] >= '0' && title[i] <= '9' {
			if i+1 < len(title) && (title[i+1] == ':' || title[i+1] == '.') {
				return true
			}
		}
	}
	return false
}

func (p *XenForoProvider) extractPostID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	// Fragment-based: #post-12345
	fragment := strings.TrimPrefix(u.Fragment, "post-")
	if fragment != "" {
		return fragment
	}
	// Canonical: /posts/12345/
	parts := strings.Split(strings.TrimSuffix(u.Path, "/"), "/")
	if len(parts) >= 2 && parts[len(parts)-2] == "posts" {
		return parts[len(parts)-1]
	}
	// Path-based: /post-12345
	for _, part := range parts {
		if strings.HasPrefix(part, "post-") {
			return strings.TrimPrefix(part, "post-")
		}
	}
	return ""
}

// normalizeChapterURL canonicalizes a XenForo chapter URL to
// {baseURL}/posts/{postID}/ so that RSS-discovered and threadmarks-discovered
// chapters for the same post share the same URL. This prevents duplicates
// in the chapters table (UNIQUE(series_id, url)).
//
// Input examples that all map to the same canonical URL:
//   .../threads/title.123/page-5#post-67890  (threadmarks)
//   .../threads/title.123/post-67890         (RSS)
//   .../posts/67890/                          (canonical redirect)
// → https://forums.spacebattles.com/posts/67890/
func (p *XenForoProvider) normalizeChapterURL(rawURL string) string {
	postID := p.extractPostID(rawURL)
	if postID != "" {
		return p.baseURL + "/posts/" + postID + "/"
	}
	// No fragment — try path-based /post-N pattern (RSS uses this).
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parts := strings.Split(strings.TrimSuffix(u.Path, "/"), "/")
	for _, part := range parts {
		if strings.HasPrefix(part, "post-") {
			pid := strings.TrimPrefix(part, "post-")
			if pid != "" {
				return p.baseURL + "/posts/" + pid + "/"
			}
		}
	}
	// Can't extract post ID — return the URL with page/fragment stripped.
	return p.normalizeThreadURL(rawURL)
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
	var chapters []models.Chapter
	err := p.withRelogin(func() error {
		var innerErr error
		chapters, innerErr = p.pollUpdates(series)
		return innerErr
	})
	return chapters, err
}

func (p *XenForoProvider) pollUpdates(series models.Series) ([]models.Chapter, error) {
	// RSS FIRST for polling: RSS feeds are served from a CDN and are NOT
	// behind Cloudflare. They return in ~1s. They show the 20-50 most recent
	// threadmarks — sufficient for polling because ON CONFLICT DO NOTHING
	// in the store means already-known chapters are skipped silently.
	//
	// Threadmarks page parsing (pollThreadmarksFull) is the FALLBACK for when
	// RSS fails entirely. It goes through FlareSolverr (~15s per series)
	// because the threadmarks HTML page IS behind Cloudflare. Using it for
	// every poll made the 15-minute poll cycle impossible to complete.

	rssURL := p.buildThreadmarksRSSURL(series.SourceURL)
	fp := gofeed.NewParser()

	resp, err := doGet(p.client, rssURL)
	if err == nil && resp.StatusCode == http.StatusOK {
		if feed, parseErr := fp.Parse(resp.Body); parseErr == nil && len(feed.Items) > 0 {
			resp.Body.Close()
			var chapters []models.Chapter
			for _, item := range feed.Items {
				pubAt := time.Now()
				if item.PublishedParsed != nil {
					pubAt = *item.PublishedParsed
				}
				chapters = append(chapters, models.Chapter{
					SeriesID:    series.ID,
					Title:       item.Title,
					URL:         p.normalizeChapterURL(item.Link),
					PublishedAt: pubAt,
				})
			}
			logging.Info("[%s] RSS yielded %d chapters", p.name, len(chapters))
			return chapters, nil
		}
		resp.Body.Close()
	} else if resp != nil {
		resp.Body.Close()
	}

	// RSS failed — fall back to threadmarks page.
	logging.Info("[%s] RSS failed, falling back to threadmarks page", p.name)
	htmlChapters, htmlErr := p.pollThreadmarksFull(series)
	if htmlErr == nil && len(htmlChapters) > 0 {
		logging.Info("[%s] threadmarks page yielded %d chapters (fallback)", p.name, len(htmlChapters))
		return htmlChapters, nil
	}
	if htmlErr != nil {
		logging.Info("[%s] threadmarks page also failed: %v", p.name, htmlErr)
		if errors.Is(htmlErr, errAuthRequired) {
			return nil, htmlErr
		}
	}

	return nil, fmt.Errorf("both RSS and threadmarks failed for %s", series.SourceURL)
}

// pollThreadmarksFull fetches the threadmarks listing page with parameters
// that request ALL threadmarks on a single page (min=-1&max=5000). This
// gives the complete chapter list, not just recent RSS changes.
// Based on fiction-dl's _GetThreadmarksURL approach.
func (p *XenForoProvider) pollThreadmarksFull(series models.Series) ([]models.Chapter, error) {
	threadURL := p.normalizeThreadURL(series.SourceURL)
	// The load-range endpoint with min=-1&max=5000 returns all threadmarks
	// in one page. Try this first; some XenForo versions use /threadmarks
	// without the query params.
	urls := []string{
		strings.TrimSuffix(threadURL, "/") + "/threadmarks-load-range?category_id=1&min=-1&max=5000",
		strings.TrimSuffix(threadURL, "/") + "/threadmarks?category_id=1&min=-1&max=5000",
		strings.TrimSuffix(threadURL, "/") + "/threadmarks",
	}

	var doc *goquery.Document
	var lastErr error
	for _, tmURL := range urls {
		d, err := p.fetchDocWithFlareSolverr(tmURL)
		if err != nil {
			lastErr = err
			continue
		}
		doc = d
		break
	}
	if doc == nil {
		return nil, fmt.Errorf("all threadmarks URL attempts failed: %w", lastErr)
	}

	// Auth check for QQ.
	if doc.Find("input[name='login']").Length() > 0 || doc.Find("a[href*='/login/']").Length() > 0 {
		if strings.Contains(doc.Find("title").Text(), "Log in") {
			return nil, errAuthRequired
		}
	}

	var chapters []models.Chapter
	chapNum := 0

	// XF2: .structItem--threadmark .structItem-title a
	doc.Find(".structItem--threadmark").Each(func(_ int, s *goquery.Selection) {
		a := s.Find(".structItem-title a").First()
		title := strings.TrimSpace(a.Text())
		link, _ := a.Attr("href")
		if title == "" || link == "" {
			return
		}
		if !strings.HasPrefix(link, "http") {
			link = p.baseURL + "/" + strings.TrimPrefix(link, "/")
		}
		link = p.normalizeChapterURL(link)
		link = p.normalizeChapterURL(link)

		timeStr, _ := s.Find("time").Attr("datetime")
		pubAt := time.Time{}
		if timeStr != "" {
			for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-0700", "2006-01-02T15:04:05Z"} {
				if t, err := time.Parse(layout, timeStr); err == nil {
					pubAt = t
					break
				}
			}
		}
		if pubAt.IsZero() {
			if timeData, _ := s.Find("time").Attr("data-time"); timeData != "" {
				if unix, err := strconv.ParseInt(timeData, 10, 64); err == nil {
					pubAt = time.Unix(unix, 0)
				}
			}
		}

		chapNum++
		// Prepend chapter number only if the title doesn't already contain one.
		// Avoids '46. Chapter 46:' doubling.
		displayTitle := title
		if !hasChapterNumber(title) {
			displayTitle = fmt.Sprintf("%d. %s", chapNum, title)
		}

		chapters = append(chapters, models.Chapter{
			SeriesID:    series.ID,
			Title:       displayTitle,
			URL:         link,
			PublishedAt: pubAt,
		})
	})

	// XF1 fallback: .threadmarkList > ol > li > a
	if len(chapters) == 0 {
		doc.Find(".threadmarkList li a, .threadmarkListing li a").Each(func(_ int, s *goquery.Selection) {
			title := strings.TrimSpace(s.Text())
			link, _ := s.Attr("href")
			if title == "" || link == "" {
				return
			}
			if !strings.HasPrefix(link, "http") {
				link = p.baseURL + "/" + strings.TrimPrefix(link, "/")
			}
			link = p.normalizeChapterURL(link)
			chapNum++
			displayTitle := title
			if !hasChapterNumber(title) {
				displayTitle = fmt.Sprintf("%d. %s", chapNum, title)
			}
			chapters = append(chapters, models.Chapter{
				SeriesID: series.ID,
				Title:    displayTitle,
				URL:      link,
			})
		})
	}

	if len(chapters) == 0 {
		return nil, fmt.Errorf("no chapters found on threadmarks page")
	}
	return chapters, nil
}

// fetchDocWithFlareSolverr fetches a URL and returns a goquery document.
// If Cloudflare challenges the request, it falls back to FlareSolverr.
func (p *XenForoProvider) fetchDocWithFlareSolverr(targetURL string) (*goquery.Document, error) {
	resp, err := doGet(p.client, targetURL)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if isCloudflareChallenge(string(body)) {
			logging.Info("[%s] Cloudflare challenge on threadmarks, using FlareSolverr", p.name)
			html, fsErr := p.solveViaFlareSolverr(targetURL)
			if fsErr != nil {
				return nil, fmt.Errorf("flaresolverr: %w", fsErr)
			}
			return goquery.NewDocumentFromReader(strings.NewReader(html))
		}
		if p.RequiresAuth() {
			return nil, errAuthRequired
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	rawHTML, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if isCloudflareChallenge(string(rawHTML)) {
		logging.Info("[%s] Cloudflare challenge (200), using FlareSolverr", p.name)
		html, fsErr := p.solveViaFlareSolverr(targetURL)
		if fsErr != nil {
			return nil, fmt.Errorf("flaresolverr: %w", fsErr)
		}
		return goquery.NewDocumentFromReader(strings.NewReader(html))
	}
	return goquery.NewDocumentFromReader(strings.NewReader(string(rawHTML)))
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
			return nil, errAuthRequired
		}
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	if doc.Find("input[name='login']").Length() > 0 || doc.Find("a[href*='/login/']").Length() > 0 {
		if strings.Contains(doc.Find("title").Text(), "Log in") {
			return nil, errAuthRequired
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
		link = p.normalizeChapterURL(link)
		link = p.normalizeChapterURL(link)

		timeStr, _ := s.Find("time").Attr("datetime")
		pubAt := time.Time{}
		if timeStr != "" {
			for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-0700", "2006-01-02T15:04:05Z"} {
				if t, err := time.Parse(layout, timeStr); err == nil {
					pubAt = t
					break
				}
			}
		}
		if pubAt.IsZero() {
			if timeData, _ := s.Find("time").Attr("data-time"); timeData != "" {
				if unix, err := strconv.ParseInt(timeData, 10, 64); err == nil {
					pubAt = time.Unix(unix, 0)
				}
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
