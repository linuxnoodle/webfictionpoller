package noveldex

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

// Search implements plugin.SeriesSearcher against NovelDex's search page.
// Results come from __NEXT_DATA__ when present, supplemented by DOM anchors.
func (p *Provider) Search(query string, page int) ([]plugin.SeriesSearchResult, error) {
	if page < 1 {
		page = 1
	}
	searchURL := fmt.Sprintf("%s/search?q=%s&page=%d", homepage, url.QueryEscape(query), page)
	doc, err := p.fetchDoc(searchURL)
	if err != nil {
		return nil, err
	}

	results := searchFromNextData(doc)

	seen := make(map[string]bool)
	for _, r := range results {
		seen[r.SourceURL] = true
	}

	doc.Find(`a[href*='/series/']`).Each(func(_ int, s *goquery.Selection) {
		href, ok := s.Attr("href")
		if !ok || !strings.Contains(href, "/series/") {
			return
		}
		u := absURL(href)
		if seen[u] {
			return
		}
		title := strings.TrimSpace(s.Find("h2, h3, h4, .title, [class*='title']").First().Text())
		if title == "" {
			return
		}
		img, _ := s.Find("img").First().Attr("src")
		seen[u] = true
		results = append(results, plugin.SeriesSearchResult{
			Title:     title,
			SourceURL: u,
			ImageURL:  absURL(img),
		})
	})

	return results, nil
}

// searchFromNextData reads the results array from __NEXT_DATA__, probing the
// common pageProps field names NovelDex uses for search payloads.
func searchFromNextData(doc *goquery.Document) []plugin.SeriesSearchResult {
	nd := extractNextData(doc)
	if nd == nil {
		return nil
	}
	pp := nextMap(nd, "props", "pageProps")
	if pp == nil {
		return nil
	}
	for _, key := range []string{"results", "series", "novels", "data"} {
		raw, ok := pp[key]
		if !ok {
			continue
		}
		arr, ok := raw.([]interface{})
		if !ok {
			continue
		}
		out := make([]plugin.SeriesSearchResult, 0, len(arr))
		for _, el := range arr {
			m, ok := el.(map[string]interface{})
			if !ok {
				continue
			}
			title := nonEmpty(nextString(m, "title"), nextString(m, "name"))
			slug := nextString(m, "slug")
			src := nonEmpty(nextString(m, "url"), nextString(m, "href"))
			if src == "" && slug != "" {
				src = homepage + "/series/novel/" + slug
			}
			if title == "" || src == "" {
				continue
			}
			out = append(out, plugin.SeriesSearchResult{
				Title:     title,
				SourceURL: absURL(src),
				Author:    nonEmpty(nextString(m, "author"), nextString(m, "authorName")),
				Summary:   nonEmpty(nextString(m, "description"), nextString(m, "summary")),
				ImageURL:  absURL(nonEmpty(nextString(m, "cover_image"), nextString(m, "coverImage"), nextString(m, "cover"))),
				Status:    nonEmpty(nextString(m, "status"), nextString(m, "state")),
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
