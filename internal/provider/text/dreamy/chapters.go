package dreamy

import (
	"fmt"
	"strconv"

	"github.com/PuerkitoBio/goquery"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// PollUpdates implements plugin.Poller. Scrapes the story page's chapter
// list (every <a data-chapter-index="N">) and returns one models.Chapter
// per discovered chapter.
//
// Premium chapters are NOT present in the list view (verified against the
// captured fixtures: count of anchors == "Free" count advertised on the
// stats panel). So we don't need per-row premium filtering here — they're
// already filtered by the site. The premium flag becomes relevant only at
// content-fetch time, where we may hit a Pass interstitial instead of an
// <article>.
func (p *Provider) PollUpdates(series models.Series) ([]models.Chapter, error) {
	slug, ok := slugFromURL(series.SourceURL)
	if !ok {
		return nil, fmt.Errorf("dreamy: cannot parse slug from source URL %q", series.SourceURL)
	}
	doc, err := p.fetchDoc(storyURL(slug))
	if err != nil {
		return nil, err
	}

	var chapters []models.Chapter
	seen := make(map[int]bool) // dedupe by chapter index (anchors can appear twice: TOC + "Start Reading")

	doc.Find(`a[data-chapter-index]`).Each(func(_ int, s *goquery.Selection) {
		idxStr, ok := s.Attr("data-chapter-index")
		if !ok {
			return
		}
		idx, err := strconv.Atoi(idxStr)
		if err != nil || idx < 0 {
			return
		}
		if seen[idx] {
			return
		}
		seen[idx] = true

		href, _ := s.Attr("href")
		if href == "" {
			return
		}
		// Title lives in the <p class="...truncate..."> inside the anchor.
		title := textOrEmpty(s.Find("p.truncate").First())
		if title == "" {
			// Fallback to the <span> text which is "Ch. N".
			title = textOrEmpty(s.Find("span").First())
		}
		title = cleanTitle(title)

		chapters = append(chapters, models.Chapter{
			SeriesID: series.ID,
			Title:    title,
			URL:      absURL(href),
			// PublishedAt is not exposed in the chapter list view; if we
			// want it later we can fetch it per-chapter inside FetchChapter
			// when Supabase REST access is wired. Leave zero for now.
		})
	})

	return chapters, nil
}
