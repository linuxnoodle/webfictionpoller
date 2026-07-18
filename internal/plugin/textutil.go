// Package plugin — textutil.go
//
// Shared text-extraction helpers used by every book provider's
// ContentFetcher implementation. Promoted here so providers don't each
// roll their own word-count / html-to-text / image-list logic.

package plugin

import (
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// CountWords returns the whitespace-split word count of s. Returns 0 for
// empty/whitespace-only input. Used to populate ChapterContent.WordCount
// when the source doesn't expose a per-chapter count.
func CountWords(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return len(strings.Fields(s))
}

// HTMLToText strips every HTML tag from html and returns the visible text.
// Used to derive ChapterContent.BodyText from BodyHTML when the provider
// doesn't populate it directly.
//
// Implementation note: goquery's .Text() recurses through the parsed tree
// and concatenates text nodes, which is what we want. We don't try to be
// clever about <script>/<style> — by the time a provider calls this, the
// body HTML has already been narrowed to the chapter's prose container.
func HTMLToText(html string) string {
	if html == "" {
		return ""
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		// Not valid HTML; return as-is rather than lose the data.
		return strings.TrimSpace(html)
	}
	return strings.TrimSpace(doc.Text())
}

// ExtractImageURLs collects every unique absolute image URL found under
// the given selection. Relative URLs are resolved against base (the site
// root or chapter URL). Used to populate ChapterContent.Images so the
// archiver knows what to cache locally.
//
// Order is preserved (first occurrence wins on dedup).
func ExtractImageURLs(root *goquery.Selection, base string) []string {
	if root.Length() == 0 {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	baseURL, _ := url.Parse(base)

	root.Find("img").Each(func(_ int, s *goquery.Selection) {
		// Prefer src, fall back to data-src (lazy-loaded images).
		src, exists := s.Attr("src")
		if !exists || src == "" {
			src, exists = s.Attr("data-src")
		}
		if !exists || src == "" {
			return
		}
		// Resolve relative URLs.
		if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
			if u, err := url.Parse(src); err == nil && baseURL != nil {
				src = baseURL.ResolveReference(u).String()
			}
		}
		if seen[src] {
			return
		}
		seen[src] = true
		out = append(out, src)
	})
	return out
}

// TextOrEmpty returns the trimmed text of s.First(), or "" when the
// selection is empty. Convenient for selector-based title extraction
// where a miss should silently fall through to the next attempt.
func TextOrEmpty(s *goquery.Selection) string {
	return strings.TrimSpace(s.First().Text())
}
