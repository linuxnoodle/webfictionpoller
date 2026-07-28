package noveldex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/safefetch"
)

// fetchHTML retrieves the HTML at target. NovelDex sits behind Cloudflare,
// so when FLARESOLVERR_URL is configured we solve via FlareSolverr first;
// otherwise we fall back to a plain browser-UA GET via safefetch and surface
// a clear error if the response is a Cloudflare challenge.
func (p *Provider) fetchHTML(target string) (string, error) {
	if p.flareURL != "" {
		html, err := p.solve(target)
		if err == nil {
			return html, nil
		}
		logging.Error("[noveldex] flaresolverr failed for %s: %v (falling back to direct fetch)", target, err)
	}
	return p.fetchDirect(target)
}

// fetchDirect does a plain browser-UA GET via safefetch (SSRF-guarded) and
// returns the body. Detects Cloudflare interstitials and converts them into
// errCloudflare so callers can distinguish CF from genuine parse failures.
func (p *Provider) fetchDirect(target string) (string, error) {
	resp, err := safefetch.Get(target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
		snip, _ := readSnippet(resp.Body, 4096)
		if isCloudflareChallenge(snip) {
			return "", errCloudflare
		}
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("noveldex: %s returned status %d", target, resp.StatusCode)
	}

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	body := sb.String()
	if isCloudflareChallenge(body) {
		return "", errCloudflare
	}
	return body, nil
}

// solve posts request.get to FlareSolverr and returns the solved HTML.
// Retries up to twice with linear backoff (FlareSolverr solves can transiently
// time out under CF rule churn).
func (p *Provider) solve(target string) (string, error) {
	payload := map[string]interface{}{
		"cmd":        "request.get",
		"url":        target,
		"maxTimeout": 60000,
	}
	body, _ := json.Marshal(payload)

	var lastErr error
	for attempt := 0; attempt <= 2; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*5) * time.Second
			logging.Info("[noveldex] flaresolverr retry %d for %s after %v", attempt, target, backoff)
			time.Sleep(backoff)
		}

		req, err := http.NewRequest("POST", p.flareURL+"/v1", bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := p.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("flaresolverr request: %w", err)
			continue
		}

		var result struct {
			Status   string `json:"status"`
			Message  string `json:"message"`
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
			lastErr = fmt.Errorf("flaresolverr status: %s (%s)", result.Status, result.Message)
			continue
		}
		return result.Solution.Response, nil
	}
	return "", fmt.Errorf("noveldex: flaresolverr retries exhausted for %s: %w", target, lastErr)
}

// flareSolverrURL reads FLARESOLVERR_URL; empty when unset (direct fetch only).
func flareSolverrURL() string {
	return strings.TrimSpace(os.Getenv("FLARESOLVERR_URL"))
}
