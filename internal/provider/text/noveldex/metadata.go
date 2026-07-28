package noveldex

import (
	"fmt"
	"strings"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// FetchSeriesMetadata implements plugin.SeriesLister. Scrapes the series
// page for title, author, synopsis, cover, status — preferring the embedded
// __NEXT_DATA__ blob (stable Next.js shape) and falling back to DOM/meta
// selectors.
func (p *Provider) FetchSeriesMetadata(rawURL string) (models.Series, error) {
	doc, err := p.fetchDoc(rawURL)
	if err != nil {
		return models.Series{}, err
	}

	series := models.Series{
		SourceURL:    rawURL,
		ProviderName: providerName,
		Status:       "active",
		Rating:       models.UnratedRating,
	}

	// __NEXT_DATA__ is the most reliable source; DOM/meta selectors fill any gaps.
	if nd := extractNextData(doc); nd != nil {
		applySeriesFromNextData(nd, &series)
	}

	if series.Title == "" {
		series.Title = firstText(doc, "h1")
	}
	if series.Title == "" {
		series.Title = metaContent(doc, "og:title")
	}
	if series.Title == "" {
		// <title> often renders as "Series Name - NovelDex".
		if t := strings.TrimSpace(doc.Find("title").First().Text()); t != "" {
			series.Title = strings.TrimSuffix(t, " - NovelDex")
		}
	}
	if series.Title == "" {
		return series, fmt.Errorf("noveldex: no title found at %s", rawURL)
	}

	if series.Author == "" {
		series.Author = firstText(doc, `a[href*="/community"]`, ".author", `[class*='author']`)
	}

	if series.Summary == "" {
		series.Summary = metaContent(doc, "description")
	}
	if series.Summary == "" {
		series.Summary = metaContent(doc, "og:description")
	}
	if series.Summary == "" {
		series.Summary = firstText(doc, `[class*='description']`, `[class*='summary']`, ".prose")
	}

	if series.ImageURL == "" {
		series.ImageURL = metaContent(doc, "og:image")
	}
	if series.ImageURL == "" {
		if src, _ := doc.Find(`img[class*='cover'], img[alt*='cover']`).First().Attr("src"); src != "" {
			series.ImageURL = absURL(src)
		}
	}

	return series, nil
}

// applySeriesFromNextData reads series props from __NEXT_DATA__. NovelDex's
// exact pageProps key shape varies by route/build (series, novel, data), so
// probe a few known locations and only fill fields that are still empty.
func applySeriesFromNextData(nd map[string]interface{}, series *models.Series) {
	for _, path := range [][]string{
		{"props", "pageProps", "series"},
		{"props", "pageProps", "novel"},
		{"props", "pageProps", "data"},
	} {
		sd := nextMap(nd, path...)
		if sd == nil {
			continue
		}
		if series.Title == "" {
			series.Title = nonEmpty(nextString(sd, "title"), nextString(sd, "name"))
		}
		if series.Author == "" {
			series.Author = nonEmpty(nextString(sd, "author"), nextString(sd, "authorName"))
		}
		if series.Summary == "" {
			series.Summary = nonEmpty(nextString(sd, "description"), nextString(sd, "summary"), nextString(sd, "synopsis"))
		}
		if series.ImageURL == "" {
			series.ImageURL = absURL(nonEmpty(
				nextString(sd, "cover_image"), nextString(sd, "coverImage"),
				nextString(sd, "cover"), nextString(sd, "thumbnail"),
			))
		}
		// Only override the default "active" when the source exposes a real status.
		if st := nonEmpty(nextString(sd, "status"), nextString(sd, "state")); st != "" {
			series.Status = st
		}
	}
}
