package providers

import "github.com/linuxnoodle/webfictionpoller/internal/models"

// Comment is an alias for models.Comment. Deprecated: use models.Comment directly.
type Comment = models.Comment

type Provider interface {
	Name() string
	MatchURL(url string) bool
	FetchSeriesMetadata(url string) (models.Series, error)
	PollUpdates(series models.Series) ([]models.Chapter, error)
	FetchChapterContent(url string) (string, error)
	FetchComments(url string) ([]Comment, error)
	RequiresAuth() bool
	SetCookies(cookies string) error
	SupportsLogin() bool
	Login(username, password string) error
}

type LoginRefresher interface {
	SetCredentialSource(fn func() (username, password string, ok bool))
}
