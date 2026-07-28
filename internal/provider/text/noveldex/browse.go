package noveldex

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

// BrowseCategories implements plugin.SeriesBrowser. NovelDex's homepage
// exposes popular/latest/trending rails.
func (p *Provider) BrowseCategories() []plugin.BrowseCategory {
	return []plugin.BrowseCategory{
		{Key: "popular", Label: "Popular Today"},
		{Key: "latest", Label: "Latest Updates"},
		{Key: "trending", Label: "Trending"},
	}
}

// Browse implements plugin.SeriesBrowser. NovelDex does not expose paginated
// listing URLs, so only page 1 returns results (the homepage sections).
// Category filtering is best-effort: the homepage markup varies between
// builds, so we scope DOM scraping to a section that looks like the
// requested category and fall back to the whole document.
func (p *Provider) Browse(category string, page int) ([]plugin.SeriesBrowseResult, error) {
	if page < 1 {
		page = 1
	}
	if page > 1 {
		// No paginated listing endpoint known; signal an empty page.
		return nil, nil
	}
	doc, err := p.fetchDoc(homepage)
	if err != nil {
		return nil, err
	}

	results := browseFromNextData(doc, category)

	seen := make(map[string]bool)
	for _, r := range results {
		seen[r.SourceURL] = true
	}

	// Best-effort DOM section match.
	sectionSel := fmt.Sprintf(`[class*='%s'], #%s`, category, category)
	section := doc.Find(sectionSel)
	if section.Length() == 0 {
		section = doc.Find("body")
	}

	section.Find(`a[href*='/series/']`).Each(func(_ int, s *goquery.Selection) {
		href, ok := s.Attr("href")
		if !ok || strings.Contains(href, "/chapter/") {
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
		results = append(results, plugin.SeriesBrowseResult{
			Title:     title,
			SourceURL: u,
			ImageURL:  absURL(img),
		})
	})

	return results, nil
}

// browseFromNextData reads a homepage section's series array from
// __NEXT_DATA__. The homepage pageProps usually nest sections by key
// (popular / latest / trending); when the requested category isn't a key we
// scan top-level arrays.
func browseFromNextData(doc *goquery.Document, category string) []plugin.SeriesBrowseResult {
	nd := extractNextData(doc)
	if nd == nil {
		return nil
	}
	pp := nextMap(nd, "props", "pageProps")
	if pp == nil {
		return nil
	}
	root := nextMap(pp, category)
	if root == nil {
		root = pp
	}
	for _, key := range []string{category, "popular", "latest", "trending", "series", "data"} {
		raw, ok := root[key]
		if !ok {
			continue
		}
		arr, ok := raw.([]interface{})
		if !ok {
			continue
		}
		out := make([]plugin.SeriesBrowseResult, 0, len(arr))
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
			out = append(out, plugin.SeriesBrowseResult{
				Title:     title,
				SourceURL: absURL(src),
				ImageURL:  absURL(nonEmpty(nextString(m, "cover_image"), nextString(m, "coverImage"), nextString(m, "cover"))),
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
