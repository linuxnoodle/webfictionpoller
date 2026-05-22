package comics

import "net/http"

type ComicProvider interface {
	Name() string
	MatchURL(url string) bool

	SearchManga(query string, page int) (*MangasPage, error)
	MangaDetails(sourceID string) (*ComicSeries, error)
	ChapterList(sourceID string) ([]ComicChapter, error)
	PageList(chapterSourceID string) ([]ComicPage, error)
	CoverURL(sourceID string) (string, error)
}

func doGet(client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "WebFictionPoller/1.0")
	return client.Do(req)
}
