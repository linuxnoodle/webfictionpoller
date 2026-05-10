package providers

import (
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
)

const maxRetries = 3

func doGet(client *http.Client, url string) (*http.Response, error) {
	return doWithRetry(client, "GET", url, nil)
}

func doWithRetry(client *http.Client, method, url string, body io.Reader) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(500+rand.IntN(1000)) * time.Millisecond * time.Duration(1<<attempt)
			logging.Info("[http] retry %d/%d for %s after %v", attempt, maxRetries, url, backoff)
			time.Sleep(backoff)
		}

		req, err := http.NewRequest(method, url, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			logging.Error("[http] error fetching %s: %v", url, err)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			retryAfter := time.Duration(5) * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if d, err := time.ParseDuration(ra + "s"); err == nil {
					retryAfter = d
				}
			}
			logging.Info("[http] 429 rate limited for %s, retry after %v", url, retryAfter)
			time.Sleep(retryAfter)
			lastErr = fmt.Errorf("rate limited (429)")
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("server error: %d", resp.StatusCode)
			logging.Error("[http] server error %d for %s", resp.StatusCode, url)
			continue
		}

		return resp, nil
	}
	return nil, fmt.Errorf("all %d retries exhausted for %s: %w", maxRetries, url, lastErr)
}
