package noveldex

import (
	"fmt"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

// FetchChapter implements plugin.ContentFetcher. Downloads a chapter page,
// extracts title + body + images into the canonical ChapterContent shape,
// and returns it for storage.
//
// Extraction order: __NEXT_DATA__ first (stable Next.js shape), then DOM
// selectors as fallback. NovelDex is heavily client-side rendered, so when
// neither path yields a body we surface a clear "client-rendered" error
// rather than an empty success.
func (p *Provider) FetchChapter(chapterURL string) (plugin.ChapterContent, error) {
	doc, err := p.fetchDoc(chapterURL)
	if err != nil {
		return plugin.ChapterContent{}, err
	}

	content := plugin.ChapterContent{SourceURL: chapterURL}

	if nd := extractNextData(doc); nd != nil {
		applyChapterFromNextData(nd, &content)
	}

	// Title fallbacks.
	if content.Title == "" {
		content.Title = strings.TrimSpace(doc.Find("h1").First().Text())
	}
	if content.Title == "" {
		content.Title = metaContent(doc, "og:title")
	}
	if content.Title == "" {
		if t := strings.TrimSpace(doc.Find("title").First().Text()); t != "" {
			content.Title = strings.TrimSuffix(t, " - NovelDex")
		}
	}

	// Body: locate the content selection once so we can reuse it for images.
	var bodySel *goquery.Selection
	if content.BodyHTML == "" {
		bodySel = firstSelection(doc, ".chapter-content", `[class*='chapter-content']`, ".prose", "article", "main")
		if bodySel != nil {
			if html, err := bodySel.Html(); err == nil {
				content.BodyHTML = strings.TrimSpace(html)
			}
		}
	}

	if content.BodyHTML == "" {
		return content, fmt.Errorf("noveldex: no chapter content found at %s (page may be client-side rendered)", chapterURL)
	}

	if content.BodyText == "" {
		content.BodyText = plugin.HTMLToText(content.BodyHTML)
	}
	if content.WordCount == 0 {
		content.WordCount = plugin.CountWords(content.BodyText)
	}
	if len(content.Images) == 0 {
		if bodySel == nil {
			bodySel = firstSelection(doc, ".chapter-content", `[class*='chapter-content']`, ".prose", "article", "main")
		}
		if bodySel != nil {
			content.Images = plugin.ExtractImageURLs(bodySel, chapterURL)
		}
	}

	logging.Info("[noveldex] fetched chapter %s (%d chars, %d words)",
		chapterURL, len(content.BodyHTML), content.WordCount)

	return content, nil
}

// applyChapterFromNextData reads chapter props from __NEXT_DATA__. The
// chapter object key varies by build (chapter vs data); probe both. Each
// field is type-asserted defensively.
func applyChapterFromNextData(nd map[string]interface{}, c *plugin.ChapterContent) {
	pp := nextMap(nd, "props", "pageProps")
	if pp == nil {
		return
	}
	ch := nextMap(pp, "chapter")
	if ch == nil {
		ch = nextMap(pp, "data")
	}
	if ch == nil {
		return
	}
	if c.Title == "" {
		c.Title = nonEmpty(nextString(ch, "title"), nextString(ch, "name"))
	}
	if c.BodyHTML == "" {
		c.BodyHTML = nonEmpty(
			nextString(ch, "content"), nextString(ch, "body"),
			nextString(ch, "bodyHTML"), nextString(ch, "html"),
		)
	}
	if c.PublishedAt.IsZero() {
		ts := nonEmpty(nextString(ch, "publishedAt"), nextString(ch, "created_at"), nextString(ch, "date"))
		if ts != "" {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				c.PublishedAt = t
			}
		}
	}
}
