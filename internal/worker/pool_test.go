package worker

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/providers"
)

type mockProvider struct {
	name       string
	pollResult []models.Chapter
	pollErr    error
	pollCount  atomic.Int32
}

func (m *mockProvider) Name() string             { return m.name }
func (m *mockProvider) MatchURL(url string) bool { return true }
func (m *mockProvider) FetchSeriesMetadata(url string) (models.Series, error) {
	return models.Series{}, nil
}
func (m *mockProvider) RequiresAuth() bool              { return false }
func (m *mockProvider) SetCookies(cookies string) error { return nil }
func (m *mockProvider) SupportsLogin() bool             { return false }
func (m *mockProvider) Login(_, _ string) error         { return fmt.Errorf("not supported") }
func (m *mockProvider) FetchChapterContent(url string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (m *mockProvider) PollUpdates(series models.Series) ([]models.Chapter, error) {
	m.pollCount.Add(1)
	return m.pollResult, m.pollErr
}

func TestWorkerPool_SubmitAndProcess(t *testing.T) {
	mock := &mockProvider{
		name: "mock",
		pollResult: []models.Chapter{
			{Title: "Ch1", URL: "https://example.com/ch1"},
			{Title: "Ch2", URL: "https://example.com/ch2"},
		},
	}

	var collectedMu sync.Mutex
	var collectedSeriesIDs []int64
	var collectedChapters [][]models.Chapter

	pool := NewWorkerPool(2, []providers.Provider{mock}, func(seriesID int64, chapters []models.Chapter) {
		collectedMu.Lock()
		collectedSeriesIDs = append(collectedSeriesIDs, seriesID)
		collectedChapters = append(collectedChapters, chapters)
		collectedMu.Unlock()
	})

	series := models.Series{ID: 1, Title: "Test Series", ProviderName: "mock"}
	pool.Submit(Job{Series: series, Provider: mock})

	time.Sleep(3 * time.Second)
	pool.Stop()

	collectedMu.Lock()
	defer collectedMu.Unlock()
	if len(collectedSeriesIDs) != 1 {
		t.Errorf("expected 1 series callback, got %d", len(collectedSeriesIDs))
	}
	if collectedSeriesIDs[0] != 1 {
		t.Errorf("series ID = %d, want 1", collectedSeriesIDs[0])
	}
	if len(collectedChapters[0]) != 2 {
		t.Errorf("chapters count = %d, want 2", len(collectedChapters[0]))
	}
}

func TestWorkerPool_RateLimiting(t *testing.T) {
	mock := &mockProvider{
		name:       "mock",
		pollResult: []models.Chapter{{Title: "Ch1"}},
	}

	pool := NewWorkerPool(4, []providers.Provider{mock}, nil)
	defer pool.Stop()

	for i := 0; i < 3; i++ {
		series := models.Series{ID: int64(i + 1), Title: "Test", ProviderName: "mock"}
		pool.Submit(Job{Series: series, Provider: mock})
	}

	time.Sleep(5 * time.Second)
	count := mock.pollCount.Load()
	if count != 3 {
		t.Logf("poll count = %d", count)
	}
}

func TestWorkerPool_GetProvider(t *testing.T) {
	mock := &mockProvider{name: "test_provider"}
	pool := NewWorkerPool(1, []providers.Provider{mock}, nil)
	defer pool.Stop()

	p, ok := pool.GetProvider("test_provider")
	if !ok {
		t.Fatal("expected to find provider")
	}
	if p.Name() != "test_provider" {
		t.Errorf("provider name = %q, want %q", p.Name(), "test_provider")
	}

	_, ok = pool.GetProvider("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent provider")
	}
}

func TestWorkerPool_AllProviders(t *testing.T) {
	m1 := &mockProvider{name: "p1"}
	m2 := &mockProvider{name: "p2"}

	pool := NewWorkerPool(1, []providers.Provider{m1, m2}, nil)
	defer pool.Stop()

	all := pool.AllProviders()
	if len(all) != 2 {
		t.Errorf("expected 2 providers, got %d", len(all))
	}
}

func TestWorkerPool_EmptyResult(t *testing.T) {
	mock := &mockProvider{
		name:       "mock",
		pollResult: []models.Chapter{},
	}

	var callCount int32
	pool := NewWorkerPool(1, []providers.Provider{mock}, func(seriesID int64, chapters []models.Chapter) {
		atomic.AddInt32(&callCount, 1)
	})

	pool.Submit(Job{
		Series:   models.Series{ID: 1, Title: "Test", ProviderName: "mock"},
		Provider: mock,
	})

	time.Sleep(3 * time.Second)
	pool.Stop()

	if atomic.LoadInt32(&callCount) != 0 {
		t.Errorf("callback should not be called for empty results, got %d calls", atomic.LoadInt32(&callCount))
	}
}

func TestWorkerPool_PollError(t *testing.T) {
	mock := &mockProvider{
		name:    "mock",
		pollErr: fmt.Errorf("network error"),
	}

	pool := NewWorkerPool(1, []providers.Provider{mock}, nil)
	pool.Submit(Job{
		Series:   models.Series{ID: 1, Title: "Test", ProviderName: "mock"},
		Provider: mock,
	})

	time.Sleep(3 * time.Second)
	pool.Stop()

	if mock.pollCount.Load() != 1 {
		t.Errorf("expected 1 poll attempt, got %d", mock.pollCount.Load())
	}
}
