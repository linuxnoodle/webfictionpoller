package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

type FlareSolverrProvider struct {
	client   *http.Client
	proxyURL string
}

func NewFanfictionNetProvider() *FlareSolverrProvider {
	proxyURL := os.Getenv("FLARESOLVERR_URL")
	if proxyURL == "" {
		proxyURL = "http://flaresolverr:8191"
	}
	return &FlareSolverrProvider{
		client: &http.Client{
			Timeout: 90 * time.Second,
		},
		proxyURL: proxyURL,
	}
}

func (p *FlareSolverrProvider) Name() string { return "fanfictionnet" }

func (p *FlareSolverrProvider) MatchURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Host
	return host == "fanfiction.net" || host == "www.fanfiction.net" || strings.HasSuffix(host, ".fanfiction.net")
}

func (p *FlareSolverrProvider) RequiresAuth() bool { return false }

func (p *FlareSolverrProvider) SetCookies(_ string) error { return nil }

func (p *FlareSolverrProvider) SupportsLogin() bool { return false }

func (p *FlareSolverrProvider) Login(_, _ string) error { return fmt.Errorf("not supported") }

func (p *FlareSolverrProvider) solve(url string) (string, error) {
	payload := map[string]interface{}{
		"cmd":        "request.get",
		"url":        url,
		"maxTimeout": 60000,
	}
	body, _ := json.Marshal(payload)

	var lastErr error
	for attempt := 0; attempt <= 2; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*5) * time.Second
			logging.Info("[fanfictionnet] retry %d for %s after %v", attempt, url, backoff)
			time.Sleep(backoff)
		}

		req, err := http.NewRequest("POST", p.proxyURL+"/v1", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("flaresolverr request: %w", err)
			logging.Error("[fanfictionnet] flaresolverr error for %s: %v", url, err)
			continue
		}

		var result struct {
			Status   string `json:"status"`
			Solution struct {
				Response string `json:"response"`
			} `json:"solution"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if decodeErr != nil {
			lastErr = fmt.Errorf("decoding flaresolverr response: %w", decodeErr)
			continue
		}
		if result.Status != "ok" {
			lastErr = fmt.Errorf("flaresolverr status: %s", result.Status)
			continue
		}
		return result.Solution.Response, nil
	}
	return "", fmt.Errorf("all retries exhausted for %s: %w", url, lastErr)
}

func (p *FlareSolverrProvider) FetchSeriesMetadata(url string) (models.Series, error) {
	var series models.Series
	html, err := p.solve(url)
	if err != nil {
		return series, err
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return series, err
	}

	series.Title = doc.Find("#profile_top b").First().Text()
	series.Author = strings.TrimSpace(doc.Find("#profile_top a").First().Text())
	series.SourceURL = url
	series.ProviderName = p.Name()
	series.Status = "active"

	return series, nil
}

func (p *FlareSolverrProvider) FetchChapterContent(url string) (string, error) {
	html, err := p.solve(url)
	if err != nil {
		return "", err
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}

	content := doc.Find("#storytext")
	if content.Length() == 0 {
		return "", fmt.Errorf("no story content found at %s", url)
	}

	htmlContent, err := content.First().Html()
	if err != nil {
		return "", err
	}

	logging.Info("[fanfictionnet] fetched chapter content from %s (%d chars)", url, len(htmlContent))
	return htmlContent, nil
}

func (p *FlareSolverrProvider) PollUpdates(series models.Series) ([]models.Chapter, error) {
	html, err := p.solve(series.SourceURL)
	if err != nil {
		return nil, err
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}

	var chapters []models.Chapter
	doc.Find("#chap_select option").Each(func(i int, s *goquery.Selection) {
		val, exists := s.Attr("value")
		if !exists {
			return
		}
		title := strings.TrimSpace(s.Text())
		chURL := fmt.Sprintf("https://www.fanfiction.net/s/%s/%s",
			p.extractStoryID(series.SourceURL), val)
		chapters = append(chapters, models.Chapter{
			SeriesID:    series.ID,
			Title:       title,
			URL:         chURL,
			PublishedAt: time.Now(),
		})
	})
	return chapters, nil
}

func (p *FlareSolverrProvider) extractStoryID(url string) string {
	parts := strings.Split(strings.TrimSuffix(url, "/"), "/")
	for i, part := range parts {
		if part == "s" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
