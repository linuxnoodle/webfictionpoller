package models

import "time"

const UnratedRating = -1.0

type Series struct {
	ID           int64     `json:"id"`
	Title        string    `json:"title"`
	Author       string    `json:"author"`
	SourceURL    string    `json:"source_url"`
	ProviderName string    `json:"provider_name"`
	Rating       float64   `json:"rating"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

type Chapter struct {
	ID          int64     `json:"id"`
	SeriesID    int64     `json:"series_id"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
	IsRead      bool      `json:"is_read"`
	CreatedAt   time.Time `json:"created_at"`
}

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
}

type ProviderConfig struct {
	ID           int64     `json:"id"`
	ProviderName string    `json:"provider_name"`
	CookieData   string    `json:"cookie_data"`
	LastPolled   time.Time `json:"last_polled"`
}

type SeriesWithChapters struct {
	Series   Series
	Chapters []Chapter
}

type ChapterWithSeries struct {
	Chapter
	SeriesTitle     string
	SeriesAuthor    string
	ProviderName    string
	SeriesRating    float64
	SeriesSourceURL string
	PreviewHTML     string
}

type DayGroup struct {
	Date     string
	Chapters []ChapterWithSeries
}

type RatingBucket struct {
	Rating float64
	Count  int
}

func ProviderFavicon(name string) string {
	switch name {
	case "royalroad":
		return "https://www.royalroad.com/favicon.ico"
	case "spacebattles":
		return "https://forums.spacebattles.com/favicon.ico"
	case "sufficientvelocity":
		return "https://forums.sufficientvelocity.com/favicon.ico"
	case "questionablequesting":
		return "https://forum.questionablequesting.com/favicon.ico"
	case "fanfictionnet":
		return "https://www.fanfiction.net/favicon.ico"
	default:
		return ""
	}
}
