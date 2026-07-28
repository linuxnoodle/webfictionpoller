package noveldex

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// PollUpdates implements plugin.Poller. Walks the series page's chapter list
// — preferring __NEXT_DATA__ (stable) and supplementing/falling back to
// chapter-link anchors — and returns one models.Chapter per unique URL.
func (p *Provider) PollUpdates(series models.Series) ([]models.Chapter, error) {
	doc, err := p.fetchDoc(series.SourceURL)
	if err != nil {
		return nil, err
	}

	chapters := pollFromNextData(doc, series.ID)

	seen := make(map[string]bool)
	for _, c := range chapters {
		seen[c.URL] = true
	}

	// DOM fallback / supplement: any anchor whose href matches the chapter shape.
	doc.Find(`a[href*='/chapter/']`).Each(func(_ int, s *goquery.Selection) {
		href, ok := s.Attr("href")
		if !ok || href == "" {
			return
		}
		u := absURL(href)
		if seen[u] {
			return
		}
		num, ok := parseChapterNumber(u)
		if !ok {
			return
		}
		title := strings.TrimSpace(s.Text())
		if title == "" {
			title = strings.TrimSpace(s.Find("h2, h3, h4, .title, [class*='title']").First().Text())
		}
		if title == "" {
			title = "Chapter " + strconv.Itoa(num)
		}
		seen[u] = true
		chapters = append(chapters, models.Chapter{
			SeriesID: series.ID,
			Title:    title,
			URL:      u,
		})
	})

	if len(chapters) == 0 {
		return nil, fmt.Errorf("noveldex: no chapters found at %s (page may be client-side rendered)", series.SourceURL)
	}
	return chapters, nil
}

// parseChapterNumber extracts the chapter number from a NovelDex chapter URL.
func parseChapterNumber(rawURL string) (int, bool) {
	u, err := parseURL(rawURL)
	if err != nil {
		return 0, false
	}
	m := chapterPathRe.FindStringSubmatch(u.Path)
	if m == nil {
		return 0, false
	}
	num, err := strconv.Atoi(m[3])
	if err != nil {
		return 0, false
	}
	return num, true
}

// pollFromNextData reads the chapter array from __NEXT_DATA__. The chapter
// list key/shape varies by build, so probe common field names and return the
// first one that yields a non-empty list.
func pollFromNextData(doc *goquery.Document, seriesID int64) []models.Chapter {
	nd := extractNextData(doc)
	if nd == nil {
		return nil
	}
	pp := nextMap(nd, "props", "pageProps")
	if pp == nil {
		return nil
	}
	for _, key := range []string{"chapters", "chapterList", "chapter_list", "data"} {
		raw, ok := pp[key]
		if !ok {
			continue
		}
		arr, ok := raw.([]interface{})
		if !ok {
			continue
		}
		out := make([]models.Chapter, 0, len(arr))
		for _, el := range arr {
			m, ok := el.(map[string]interface{})
			if !ok {
				continue
			}
			title := nonEmpty(nextString(m, "title"), nextString(m, "name"))
			num := nonEmpty(nextString(m, "number"), nextString(m, "chapter_number"), nextString(m, "chapterNumber"), nextString(m, "index"))
			slug := nextString(m, "slug")
			href := nonEmpty(nextString(m, "url"), nextString(m, "href"))
			if href == "" && slug != "" && num != "" {
				href = homepage + "/series/novel/" + slug + "/chapter/" + num
			}
			if href == "" {
				continue
			}
			if title == "" && num != "" {
				title = "Chapter " + num
			}
			if title == "" {
				continue
			}
			out = append(out, models.Chapter{
				SeriesID: seriesID,
				Title:    title,
				URL:      absURL(href),
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
