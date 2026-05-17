package providers

import "github.com/linuxnoodle/webfictionpoller/internal/models"

type Provider interface {
	Name() string
	MatchURL(url string) bool
	FetchSeriesMetadata(url string) (models.Series, error)
	PollUpdates(series models.Series) ([]models.Chapter, error)
	FetchChapterContent(url string) (string, error)
	RequiresAuth() bool
	SetCookies(cookies string) error
	SupportsLogin() bool
	Login(username, password string) error
}
