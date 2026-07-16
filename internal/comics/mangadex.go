package comics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

type MangaDexProvider struct {
	client *http.Client
}

func NewMangaDexProvider() *MangaDexProvider {
	return &MangaDexProvider{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (m *MangaDexProvider) Name() string { return "mangadex" }

func (m *MangaDexProvider) Meta() plugin.Meta {
	return plugin.Meta{
		Name:              "mangadex",
		DisplayName:       "MangaDex",
		Kind:              plugin.KindComic,
		Homepage:          "https://mangadex.org",
		FaviconURL:        "https://mangadex.org/img/avatar.png",
		AuthModes:         []plugin.AuthMode{plugin.AuthNone},
		Rate:              plugin.RateSpec{RequestsPerSecond: 5.0, Burst: 10, Concurrency: 2},
		PollIntervalDefault: "1h",
	}
}

// MatchURL lets MangaDex participate in plugin.ByURL routing for pasted
// mangadex.org/title/... URLs (future add-by-URL flow). Currently add-by-URL
// for comics is search-driven; this is forward compatibility.
func (m *MangaDexProvider) MatchURL(rawURL string) bool {
	return plugin.HostMatch(rawURL, "mangadex.org")
}

// Search is the plugin.Searcher capability; existing SearchManga is kept
// as the concrete implementation for backward compatibility with
// handlers.comicProviders map.
func (m *MangaDexProvider) Search(query string, page int) (*MangasPage, error) {
	return m.SearchManga(query, page)
}

func (m *MangaDexProvider) SearchManga(query string, page int) (*MangasPage, error) {
	limit := 20
	offset := (page - 1) * limit
	u := fmt.Sprintf("https://api.mangadex.org/manga?limit=%d&offset=%d&contentRating[]=safe&contentRating[]=suggestive&includes[]=cover_art&title=%s",
		limit, offset, url.QueryEscape(query))

	resp, err := doGet(m.client, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Result string `json:"result"`
		Data   []struct {
			ID         string `json:"id"`
			Attributes struct {
				Title       map[string]string   `json:"title"`
				AltTitles   []map[string]string `json:"altTitles"`
				Description map[string]string   `json:"description"`
				Status      string              `json:"status"`
				Tags        []struct {
					Attributes struct {
						Name map[string]string `json:"name"`
					} `json:"attributes"`
				} `json:"tags"`
			} `json:"attributes"`
			Relationships []struct {
				ID         string `json:"id"`
				Type       string `json:"type"`
				Attributes *struct {
					FileName string `json:"fileName"`
				} `json:"attributes,omitempty"`
			} `json:"relationships"`
		} `json:"data"`
		Total int `json:"total"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("mangadex: decoding search: %w", err)
	}

	var series []ComicSeries
	for _, d := range result.Data {
		title := firstVal(d.Attributes.Title)
		if title == "" {
			continue
		}
		desc := firstVal(d.Attributes.Description)

		var coverFileName string
		for _, rel := range d.Relationships {
			if rel.Type == "cover_art" && rel.Attributes != nil {
				coverFileName = rel.Attributes.FileName
				break
			}
		}

		var coverURL string
		if coverFileName != "" {
			coverURL = fmt.Sprintf("https://uploads.mangadex.org/covers/%s/%s.512.jpg", d.ID, coverFileName)
		}

		var genres []string
		for _, tag := range d.Attributes.Tags {
			if n := firstVal(tag.Attributes.Name); n != "" {
				genres = append(genres, n)
			}
		}

		series = append(series, ComicSeries{
			SourceID:     d.ID,
			Title:        title,
			Description:  desc,
			CoverURL:     coverURL,
			SourceURL:    "https://mangadex.org/title/" + d.ID,
			ProviderName: "mangadex",
			Status:       mdStatus(d.Attributes.Status),
			Genres:       joinComma(genres),
		})
	}

	hasNext := (offset + limit) < result.Total
	return &MangasPage{Mangas: series, HasNextPage: hasNext}, nil
}

func (m *MangaDexProvider) MangaDetails(sourceID string) (*ComicSeries, error) {
	u := fmt.Sprintf("https://api.mangadex.org/manga/%s?includes[]=cover_art&includes[]=author&includes[]=artist", sourceID)

	resp, err := doGet(m.client, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			ID         string `json:"id"`
			Attributes struct {
				Title       map[string]string   `json:"title"`
				AltTitles   []map[string]string `json:"altTitles"`
				Description map[string]string   `json:"description"`
				Status      string              `json:"status"`
				Author      string              `json:"author"`
				Artist      string              `json:"artist"`
				Tags        []struct {
					Attributes struct {
						Name map[string]string `json:"name"`
					} `json:"attributes"`
				} `json:"tags"`
			} `json:"attributes"`
			Relationships []struct {
				ID         string `json:"id"`
				Type       string `json:"type"`
				Attributes *struct {
					FileName string `json:"fileName"`
					Name     string `json:"name"`
				} `json:"attributes,omitempty"`
			} `json:"relationships"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("mangadex: decoding manga: %w", err)
	}

	d := result.Data
	title := firstVal(d.Attributes.Title)
	desc := firstVal(d.Attributes.Description)

	var coverFileName, author, artist string
	for _, rel := range d.Relationships {
		if rel.Type == "cover_art" && rel.Attributes != nil {
			coverFileName = rel.Attributes.FileName
		}
		if rel.Type == "author" && rel.Attributes != nil {
			author = rel.Attributes.Name
		}
		if rel.Type == "artist" && rel.Attributes != nil {
			artist = rel.Attributes.Name
		}
	}

	var coverURL string
	if coverFileName != "" {
		coverURL = fmt.Sprintf("https://uploads.mangadex.org/covers/%s/%s.512.jpg", d.ID, coverFileName)
	}

	var genres []string
	for _, tag := range d.Attributes.Tags {
		if n := firstVal(tag.Attributes.Name); n != "" {
			genres = append(genres, n)
		}
	}

	return &ComicSeries{
		SourceID:     d.ID,
		Title:        title,
		Author:       author,
		Artist:       artist,
		Description:  desc,
		CoverURL:     coverURL,
		SourceURL:    "https://mangadex.org/title/" + d.ID,
		ProviderName: "mangadex",
		Status:       mdStatus(d.Attributes.Status),
		Genres:       joinComma(genres),
	}, nil
}

func (m *MangaDexProvider) ChapterList(sourceID string) ([]ComicChapter, error) {
	var all []ComicChapter
	offset := 0
	limit := 100

	for {
		u := fmt.Sprintf("https://api.mangadex.org/manga/%s/feed?limit=%d&offset=%d&translatedLanguage[]=en&order[chapter]=asc&contentRating[]=safe&contentRating[]=suggestive",
			sourceID, limit, offset)

		resp, err := doGet(m.client, u)
		if err != nil {
			return nil, err
		}

		var result struct {
			Data []struct {
				ID         string `json:"id"`
				Attributes struct {
					Volume             string  `json:"volume"`
					Chapter            string  `json:"chapter"`
					Title              string  `json:"title"`
					PublishAt          string  `json:"publishAt"`
					Pages              int     `json:"pages"`
					ExternalURL        *string `json:"externalUrl"`
					TranslatedLanguage string  `json:"translatedLanguage"`
				} `json:"attributes"`
			} `json:"data"`
			Total int `json:"total"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("mangadex: decoding chapters: %w", err)
		}
		resp.Body.Close()

		for _, ch := range result.Data {
			if ch.Attributes.ExternalURL != nil {
				continue
			}

			title := ch.Attributes.Title
			if title == "" {
				title = "Ch. " + ch.Attributes.Chapter
			}

			all = append(all, ComicChapter{
				SourceID:    ch.ID,
				Title:       title,
				ChapterNum:  ch.Attributes.Chapter,
				VolumeNum:   ch.Attributes.Volume,
				SourceURL:   "https://mangadex.org/chapter/" + ch.ID,
				Pages:       ch.Attributes.Pages,
				PublishedAt: ch.Attributes.PublishAt,
			})
		}

		offset += limit
		if offset >= result.Total {
			break
		}
	}

	return all, nil
}

func (m *MangaDexProvider) PageList(chapterSourceID string) ([]ComicPage, error) {
	u := fmt.Sprintf("https://api.mangadex.org/at-home/server/%s", chapterSourceID)

	resp, err := doGet(m.client, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		BaseURL string `json:"baseUrl"`
		Chapter struct {
			Hash string   `json:"hash"`
			Data []string `json:"data"`
		} `json:"chapter"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("mangadex: decoding pages: %w", err)
	}

	pages := make([]ComicPage, len(result.Chapter.Data))
	for i, img := range result.Chapter.Data {
		pages[i] = ComicPage{
			Index:    i,
			ImageURL: result.BaseURL + "/data/" + result.Chapter.Hash + "/" + img,
		}
	}
	return pages, nil
}

func firstVal(m map[string]string) string {
	for k, v := range m {
		_ = k
		return v
	}
	return ""
}

func mdStatus(s string) string {
	switch s {
	case "ongoing":
		return "active"
	case "completed":
		return "completed"
	case "hiatus":
		return "hiatus"
	case "cancelled":
		return "dropped"
	default:
		return "active"
	}
}

func joinComma(ss []string) string {
	var out string
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
