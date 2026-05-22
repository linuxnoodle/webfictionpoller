package providers

import "github.com/linuxnoodle/webfictionpoller/internal/models"

type Comment struct {
	Author    string `json:"author"`
	Content   string `json:"content"`
	Date      string `json:"date"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

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
