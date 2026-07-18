package dreamy

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

// FetchChapter implements plugin.ContentFetcher. Downloads a chapter page,
// extracts the title + body + image list into the canonical ChapterContent
// shape, and returns it for storage.
//
// Selectors (verified against captured fixtures):
//
//   - Title: <main> <div class="group/title"> <button> <span> <span>
//     The button has classes "text-2xl md:text-4xl font-bold ...".
//     Fallback: <title> tag parses "... - Chapter N: TITLE - Dreamy ...".
//   - Body:  <main> <article class="chapter-content"> (inner HTML)
//     Body paragraphs are <div class="paragraph"><p class="line">...</p></div>.
//
// Premium detection: if the article element is absent and the page contains
// a "Sign in for Pass" CTA, return ChapterContent{Premium: true} with empty
// body so the storage layer can mark the row locked.
func (p *Provider) FetchChapter(chapterURL string) (plugin.ChapterContent, error) {
	doc, err := p.fetchDoc(chapterURL)
	if err != nil {
		return plugin.ChapterContent{}, err
	}

	title := extractChapterTitle(doc, chapterURL)

	bodyHTML, err := extractBodyHTML(doc)
	if err != nil {
		// Article missing — check whether this is a premium interstitial.
		if isPremiumInterstitial(doc) {
			return plugin.ChapterContent{
				Title:     title,
				Premium:   true,
				SourceURL: chapterURL,
			}, nil
		}
		return plugin.ChapterContent{}, fmt.Errorf("dreamy: %v (url=%s)", err, chapterURL)
	}

	images := extractImages(doc)
	bodyText := htmlToText(bodyHTML)

	return plugin.ChapterContent{
		Title:     title,
		BodyHTML:  bodyHTML,
		BodyText:  bodyText,
		WordCount: countWords(bodyText),
		Images:    images,
		SourceURL: chapterURL,
	}, nil
}

// extractChapterTitle pulls the chapter title from the page.
//
// Primary path: the button under <main> with class "text-2xl" (or any
// descendant button of <main>'s title container). The button's nested
// <span><span>TEXT</span></span> is the visible title.
//
// Fallback: parse the <title> tag which renders as
// "Series Name - Chapter N: CHAPTER TITLE - Dreamy Translations".
func extractChapterTitle(doc *goquery.Document, chapterURL string) string {
	// Primary: main button span span
	if btn := doc.Find("main button.text-2xl span span").First(); btn.Length() > 0 {
		if t := textOrEmpty(btn); t != "" {
			return cleanTitle(t)
		}
	}
	// Wider fallback: any span inside the chapter-title button.
	if btn := doc.Find("main .group\\/title button span").First(); btn.Length() > 0 {
		if t := textOrEmpty(btn); t != "" {
			return cleanTitle(t)
		}
	}
	// Last resort: parse <title>.
	if t := textOrEmpty(doc.Find("title")); t != "" {
		// "Series - Chapter N: TITLE - Dreamy Translations"
		if i := strings.LastIndex(t, " - Dreamy Translations"); i >= 0 {
			t = t[:i]
		}
		if i := strings.Index(t, ": "); i >= 0 {
			return cleanTitle(t[i+2:])
		}
		return cleanTitle(t)
	}
	return ""
}

// extractBodyHTML returns the inner HTML of the chapter-content article.
// Returns an error when the article is missing so the caller can decide
// whether it's a premium interstitial or a real parse failure.
func extractBodyHTML(doc *goquery.Document) (string, error) {
	article := doc.Find("article.chapter-content").First()
	if article.Length() == 0 {
		// Older fallback: any <article> under main.
		article = doc.Find("main article").First()
	}
	if article.Length() == 0 {
		return "", fmt.Errorf("no <article class=chapter-content> found")
	}
	html, err := article.Html()
	if err != nil {
		return "", fmt.Errorf("reading article HTML: %w", err)
	}
	html = strings.TrimSpace(html)
	if html == "" {
		return "", fmt.Errorf("article body is empty")
	}
	return html, nil
}

// extractImages collects every <img> src inside the chapter body for the
// archiver to cache. Absolute URLs only.
func extractImages(doc *goquery.Document) []string {
	var imgs []string
	seen := make(map[string]bool)
	doc.Find("article.chapter-content img, main article img").Each(func(_ int, s *goquery.Selection) {
		src, _ := s.Attr("src")
		if src == "" || seen[src] {
			return
		}
		seen[src] = true
		if abs := absURL(src); abs != "" {
			imgs = append(imgs, abs)
		}
	})
	return imgs
}

// isPremiumInterstitial returns true when the page lacks an article and
// contains the "Sign in for Pass" CTA — i.e. the chapter is paywalled.
func isPremiumInterstitial(doc *goquery.Document) bool {
	return doc.Find(`body:contains("Sign in for Pass")`).Length() > 0 ||
		doc.Find(`a:contains("Sign in for Pass")`).Length() > 0 ||
		doc.Find(`button:contains("Pass")`).Length() > 0
}
