package noveldex

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// parseURL wraps url.Parse to keep the call sites tidy.
func parseURL(rawURL string) (*url.URL, error) { return url.Parse(rawURL) }

// readSnippet reads up to maxBytes from r and returns it as a string. Used
// for Cloudflare-challenge sniffing without buffering the whole body.
func readSnippet(r io.Reader, maxBytes int) (string, error) {
	buf := make([]byte, maxBytes)
	n, err := r.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	return string(buf[:n]), nil
}

// absURL resolves a possibly-relative href against the NovelDex site root.
func absURL(href string) string {
	if href == "" {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if u.IsAbs() {
		return href
	}
	base, _ := url.Parse(homepage)
	return base.ResolveReference(u).String()
}

// isCloudflareChallenge sniffs the markers Cloudflare injects on its
// "Just a moment..." interstitial.
func isCloudflareChallenge(body string) bool {
	return strings.Contains(body, "Just a moment...") ||
		strings.Contains(body, "cf-challenge") ||
		strings.Contains(body, "cdn-cgi/challenge") ||
		strings.Contains(body, "challenges.cloudflare.com")
}

// errCloudflare is returned when a fetch hits a Cloudflare interstitial.
// Operators should set FLARESOLVERR_URL when this fires repeatedly.
var errCloudflare = fmt.Errorf("noveldex: cloudflare challenge detected — set FLARESOLVERR_URL if this persists")

// extractNextData pulls the JSON object out of the Next.js #​__NEXT_DATA__
// script tag. Returns nil when absent or unparseable.
func extractNextData(doc *goquery.Document) map[string]interface{} {
	script := doc.Find("#__NEXT_DATA__").First().Text()
	if strings.TrimSpace(script) == "" {
		return nil
	}
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(script), &data); err != nil {
		return nil
	}
	return data
}

// nextString walks a nested map-of-maps path and returns the leaf as a
// string. Returns "" when any step is the wrong type or missing, so callers
// can chain probes without per-level nil checks.
func nextString(data map[string]interface{}, path ...string) string {
	cur := nextMap(data, path[:len(path)-1]...)
	if cur == nil {
		return ""
	}
	s, _ := cur[path[len(path)-1]].(string)
	return s
}

// nextMap returns the nested map at path, or nil when any step misses.
func nextMap(data map[string]interface{}, path ...string) map[string]interface{} {
	var cur interface{} = data
	for _, key := range path {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = m[key]
	}
	if m, ok := cur.(map[string]interface{}); ok {
		return m
	}
	return nil
}

// nonEmpty returns the first trimmed non-empty value in vals, or "".
func nonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// firstText returns the trimmed text of the first selector that matches,
// or "" when none match.
func firstText(doc *goquery.Document, selectors ...string) string {
	for _, sel := range selectors {
		if t := strings.TrimSpace(doc.Find(sel).First().Text()); t != "" {
			return t
		}
	}
	return ""
}

// firstSelection returns the first matching selection, or nil when none of
// the selectors match anything.
func firstSelection(doc *goquery.Document, selectors ...string) *goquery.Selection {
	for _, sel := range selectors {
		if s := doc.Find(sel).First(); s.Length() > 0 {
			return s
		}
	}
	return nil
}

// metaContent reads the content attribute of <meta property="{prop}"> or
// <meta name="{prop}">, trimmed. "" when absent.
func metaContent(doc *goquery.Document, prop string) string {
	if v, ok := doc.Find(`meta[property="` + prop + `"]`).First().Attr("content"); ok {
		return strings.TrimSpace(v)
	}
	if v, ok := doc.Find(`meta[name="` + prop + `"]`).First().Attr("content"); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
