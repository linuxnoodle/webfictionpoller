package worker

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/providers"
	"github.com/linuxnoodle/webfictionpoller/internal/safefetch"
	"github.com/microcosm-cc/bluemonday"
)

var imgSrcRegex = regexp.MustCompile(`(?i)src=["']([^"']+)["']`)

type ArchiverStore interface {
	GetArchivedSeries() ([]models.Series, error)
	GetAllActiveSeries() ([]models.Series, error)
	GetChaptersNeedingContent(seriesID int64) ([]models.Chapter, error)
	SaveChapterContent(id int64, content string) error
	SaveChapterImage(chapterID int64, url string, data []byte, contentType string) error
	GetSetting(key string) string
}

type ArchiverStatus struct {
	Active  bool   `json:"active"`
	Current string `json:"current,omitempty"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	LastRun string `json:"last_run,omitempty"`
}

type Archiver struct {
	store            ArchiverStore
	providers        map[string]providers.Provider
	policy           *bluemonday.Policy
	archiveAllDefault bool

	mu         chan struct{}
	status     ArchiverStatus
	lastRequest map[string]time.Time
}

func NewArchiver(store ArchiverStore, providerList []providers.Provider, archiveAllDefault bool) *Archiver {
	a := &Archiver{
		store:            store,
		providers:        make(map[string]providers.Provider),
		policy:           bluemonday.UGCPolicy(),
		archiveAllDefault: archiveAllDefault,
		mu:               make(chan struct{}, 1),
		lastRequest:      make(map[string]time.Time),
	}
	a.policy.AllowImages()
	for _, p := range providerList {
		a.providers[p.Name()] = p
	}
	return a
}

func (a *Archiver) isArchiveAll() bool {
	val := a.store.GetSetting("archive_all")
	if val != "" {
		return val == "true"
	}
	return a.archiveAllDefault
}

func (a *Archiver) Run(ctx context.Context, interval time.Duration) {
	a.runCycle(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runCycle(ctx)
		}
	}
}

func (a *Archiver) waitForSiteRateLimit(providerName string) {
	minDelay := 10 * time.Second
	jitter := time.Duration(rand.IntN(5000)) * time.Millisecond

	if last, ok := a.lastRequest[providerName]; ok {
		elapsed := time.Since(last)
		required := minDelay + jitter
		if elapsed < required {
			time.Sleep(required - elapsed)
		}
	} else {
		time.Sleep(jitter)
	}

	a.lastRequest[providerName] = time.Now()
}

func (a *Archiver) runCycle(ctx context.Context) {
	select {
	case a.mu <- struct{}{}:
	default:
		logging.Info("[archiver] already running, skipping")
		return
	}
	defer func() { <-a.mu }()

	var series []models.Series
	var err error

	if a.isArchiveAll() {
		series, err = a.store.GetAllActiveSeries()
	} else {
		series, err = a.store.GetArchivedSeries()
	}
	if err != nil {
		logging.Error("[archiver] error fetching series: %v", err)
		return
	}
	if len(series) == 0 {
		return
	}

	a.status.Active = true
	a.status.Total = len(series)
	a.status.Done = 0
	defer func() {
		a.status.Active = false
		a.status.LastRun = time.Now().Format(time.RFC3339)
	}()

	for _, s := range series {
		select {
		case <-ctx.Done():
			return
		default:
		}

		a.status.Current = s.Title
		p, ok := a.providers[s.ProviderName]
		if !ok {
			a.status.Done++
			continue
		}

		chapters, err := a.store.GetChaptersNeedingContent(s.ID)
		if err != nil {
			logging.Error("[archiver] error fetching chapters for series %d: %v", s.ID, err)
			a.status.Done++
			continue
		}
		if len(chapters) == 0 {
			a.status.Done++
			continue
		}

		logging.Info("[archiver] archiving %d chapters for %q", len(chapters), s.Title)

		for _, ch := range chapters {
			select {
			case <-ctx.Done():
				return
			default:
			}

			a.waitForSiteRateLimit(s.ProviderName)

			content, err := p.FetchChapterContent(ch.URL)
			if err != nil {
				logging.Error("[archiver] error fetching chapter %d (%s): %v", ch.ID, ch.URL, err)
				continue
			}

			a.downloadImages(ch.ID, content)

			content = a.policy.Sanitize(content)

			if err := a.store.SaveChapterContent(ch.ID, content); err != nil {
				logging.Error("[archiver] error saving content for chapter %d: %v", ch.ID, err)
			} else {
				logging.Info("[archiver] saved content for chapter %d (%s)", ch.ID, ch.Title)
			}
		}

		a.status.Done++
	}
}

func (a *Archiver) downloadImages(chapterID int64, htmlContent string) {
	matches := imgSrcRegex.FindAllStringSubmatch(htmlContent, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		imgURL := m[1]
		if strings.HasPrefix(imgURL, "data:") {
			continue
		}

		data, contentType, err := a.fetchImage(imgURL)
		if err != nil {
			logging.Error("[archiver] error fetching image %s: %v", imgURL, err)
			continue
		}

		if err := a.store.SaveChapterImage(chapterID, imgURL, data, contentType); err != nil {
			logging.Error("[archiver] error saving image for chapter %d: %v", chapterID, err)
		}
	}
}

func (a *Archiver) fetchImage(url string) ([]byte, string, error) {
	resp, err := safefetch.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, "", err
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return data, contentType, nil
}

func (a *Archiver) RunOnce() {
	a.runCycle(context.Background())
}

func (a *Archiver) GetStatus() ArchiverStatus {
	return a.status
}
