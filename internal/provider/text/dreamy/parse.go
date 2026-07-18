package dreamy

import (
	"io"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// parseURL wraps url.Parse to keep dreamy.go's import list small.
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

// absURL resolves a possibly-relative href against the site root.
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

// textOrEmpty returns the trimmed text of the first match of selector, or
// "" when the selector matches nothing.
func textOrEmpty(s *goquery.Selection) string {
	return strings.TrimSpace(s.First().Text())
}

// cleanTitle strips common decorations from a chapter or series title.
// Removes leading "Ch. N" / "Chapter N" prefixes, trailing word counts.
func cleanTitle(raw string) string {
	raw = strings.TrimSpace(raw)
	// Leading "Ch. N" or "Chapter N"
	raw = strings.TrimPrefix(raw, "Ch. ")
	// "N. Title" pattern — keep the number+title as-is, it's meaningful
	// (e.g. "0. Prologue", "245. Abyssal Holy War – Final Resistance (4)")
	return raw
}

// countWords is a simple whitespace-split word counter, ASCII-aware. Used
// for the WordCount field on ChapterContent when the source doesn't
// expose a per-chapter count.
func countWords(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return len(strings.Fields(s))
}

// htmlToText strips all HTML tags from s, returning the visible text.
// Used to derive BodyText from BodyHTML when the provider didn't populate it.
func htmlToText(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return html
	}
	return strings.TrimSpace(doc.Text())
}
