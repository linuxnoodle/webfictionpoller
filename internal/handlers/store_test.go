package handlers

import (
	"os"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/database"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	tmp, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })

	db, err := database.InitDB(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestAddAndGetSeries(t *testing.T) {
	s := newTestStore(t)

	series := models.Series{
		Title:        "Test Fiction",
		Author:       "Test Author",
		SourceURL:    "https://www.royalroad.com/fiction/12345/test",
		ProviderName: "royalroad",
		Rating:       5,
		Status:       "active",
	}

	id, err := s.AddSeries(series)
	if err != nil {
		t.Fatalf("AddSeries failed: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	got, err := s.GetSeriesByID(id)
	if err != nil {
		t.Fatalf("GetSeriesByID failed: %v", err)
	}
	if got.Title != series.Title {
		t.Errorf("Title = %q, want %q", got.Title, series.Title)
	}
	if got.Author != series.Author {
		t.Errorf("Author = %q, want %q", got.Author, series.Author)
	}
	if got.ProviderName != series.ProviderName {
		t.Errorf("ProviderName = %q, want %q", got.ProviderName, series.ProviderName)
	}
}

func TestListSeries(t *testing.T) {
	s := newTestStore(t)

	for i := 0; i < 3; i++ {
		_, err := s.AddSeries(models.Series{
			Title:        "Series " + string(rune('A'+i)),
			SourceURL:    "https://example.com/" + string(rune('A'+i)),
			ProviderName: "test",
			Status:       "active",
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	list, err := s.ListSeries()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Errorf("ListSeries returned %d, want 3", len(list))
	}
}

func TestUpdateSeriesStatus(t *testing.T) {
	s := newTestStore(t)

	id, _ := s.AddSeries(models.Series{
		Title: "Test", SourceURL: "https://example.com", ProviderName: "test", Status: "active",
	})

	err := s.UpdateSeriesStatus(id, "dropped")
	if err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetSeriesByID(id)
	if got.Status != "dropped" {
		t.Errorf("Status = %q, want %q", got.Status, "dropped")
	}
}

func TestUpdateSeriesRating(t *testing.T) {
	s := newTestStore(t)

	id, err := s.AddSeries(models.Series{
		Title: "Test", SourceURL: "https://example.com", ProviderName: "test", Rating: 0, Status: "active",
	})
	if err != nil {
		t.Fatalf("AddSeries failed: %v", err)
	}

	err = s.UpdateSeriesRating(id, 8.5)
	if err != nil {
		t.Fatalf("UpdateSeriesRating failed: %v", err)
	}

	got, err := s.GetSeriesByID(id)
	if err != nil {
		t.Fatalf("GetSeriesByID failed: %v", err)
	}
	if got.Rating != 8.5 {
		t.Errorf("Rating = %f, want %f", got.Rating, 8.5)
	}
}

func TestInsertChapters(t *testing.T) {
	s := newTestStore(t)

	seriesID, _ := s.AddSeries(models.Series{
		Title: "Test", SourceURL: "https://example.com", ProviderName: "test", Status: "active",
	})

	chapters := []models.Chapter{
		{SeriesID: seriesID, Title: "Ch 1", URL: "https://example.com/ch1"},
		{SeriesID: seriesID, Title: "Ch 2", URL: "https://example.com/ch2"},
		{SeriesID: seriesID, Title: "Ch 3", URL: "https://example.com/ch3"},
	}

	inserted, err := s.InsertChapters(seriesID, chapters)
	if err != nil {
		t.Fatalf("InsertChapters failed: %v", err)
	}
	if inserted != 3 {
		t.Errorf("inserted = %d, want 3", inserted)
	}

	inserted2, err := s.InsertChapters(seriesID, chapters)
	if err != nil {
		t.Fatal(err)
	}
	if inserted2 != 0 {
		t.Errorf("duplicate insert: inserted = %d, want 0", inserted2)
	}
}

func TestMarkChapterRead(t *testing.T) {
	s := newTestStore(t)

	seriesID, _ := s.AddSeries(models.Series{
		Title: "Test", SourceURL: "https://example.com", ProviderName: "test", Status: "active",
	})

	s.InsertChapters(seriesID, []models.Chapter{
		{SeriesID: seriesID, Title: "Ch 1", URL: "https://example.com/ch1"},
	})

	dashboard, _ := s.GetSeriesView()
	if len(dashboard) != 1 {
		t.Fatalf("dashboard should have 1 group, got %d", len(dashboard))
	}
	if len(dashboard[0].Chapters) != 1 {
		t.Fatalf("should have 1 chapter, got %d", len(dashboard[0].Chapters))
	}

	chapterID := dashboard[0].Chapters[0].ID
	redirectURL, err := s.MarkChapterRead(chapterID)
	if err != nil {
		t.Fatal(err)
	}
	if redirectURL != "https://example.com/ch1" {
		t.Errorf("redirect URL = %q, want chapter URL", redirectURL)
	}

	dashboard2, _ := s.GetSeriesView()
	if len(dashboard2) != 1 {
		t.Fatalf("series view should still show the series, got %d groups", len(dashboard2))
	}
	if dashboard2[0].Chapters[0].IsRead != true {
		t.Errorf("chapter should be marked as read")
	}
}

func TestMarkAllSeriesRead(t *testing.T) {
	s := newTestStore(t)

	seriesID, _ := s.AddSeries(models.Series{
		Title: "Test", SourceURL: "https://example.com", ProviderName: "test", Status: "active",
	})

	s.InsertChapters(seriesID, []models.Chapter{
		{SeriesID: seriesID, Title: "Ch 1", URL: "https://example.com/ch1"},
		{SeriesID: seriesID, Title: "Ch 2", URL: "https://example.com/ch2"},
	})

	err := s.MarkAllSeriesRead(seriesID)
	if err != nil {
		t.Fatal(err)
	}

	dashboard, _ := s.GetSeriesView()
	if len(dashboard) != 1 {
		t.Fatalf("expected 1 group, got %d", len(dashboard))
	}
	for _, ch := range dashboard[0].Chapters {
		if !ch.IsRead {
			t.Error("expected all chapters to be read")
		}
	}
}

func TestDeleteSeries(t *testing.T) {
	s := newTestStore(t)

	seriesID, _ := s.AddSeries(models.Series{
		Title: "Test", SourceURL: "https://example.com", ProviderName: "test", Status: "active",
	})

	s.InsertChapters(seriesID, []models.Chapter{
		{SeriesID: seriesID, Title: "Ch 1", URL: "https://example.com/ch1"},
	})

	err := s.DeleteSeries(seriesID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.GetSeriesByID(seriesID)
	if err == nil {
		t.Error("expected error for deleted series")
	}
}

func TestGetAllActiveSeries(t *testing.T) {
	s := newTestStore(t)

	s.AddSeries(models.Series{Title: "Active", SourceURL: "https://example.com/1", ProviderName: "test", Status: "active"})
	s.AddSeries(models.Series{Title: "Binge", SourceURL: "https://example.com/2", ProviderName: "test", Status: "binge"})
	s.AddSeries(models.Series{Title: "Dropped", SourceURL: "https://example.com/3", ProviderName: "test", Status: "dropped"})
	s.AddSeries(models.Series{Title: "Hiatus", SourceURL: "https://example.com/4", ProviderName: "test", Status: "hiatus"})

	active, err := s.GetAllActiveSeries()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Errorf("expected 2 active series, got %d", len(active))
	}
}

func TestProviderConfig(t *testing.T) {
	s := newTestStore(t)

	got, err := s.GetProviderConfig("test")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("expected nil for non-existent config")
	}

	err = s.UpsertProviderConfig("test", "cookie1=val1", "", "")
	if err != nil {
		t.Fatal(err)
	}

	got, err = s.GetProviderConfig("test")
	if err != nil {
		t.Fatal(err)
	}
	if got.CookieData != "cookie1=val1" {
		t.Errorf("CookieData = %q, want %q", got.CookieData, "cookie1=val1")
	}

	err = s.UpsertProviderConfig("test", "cookie2=val2", "", "")
	if err != nil {
		t.Fatal(err)
	}

	got, _ = s.GetProviderConfig("test")
	if got.CookieData != "cookie2=val2" {
		t.Errorf("updated CookieData = %q, want %q", got.CookieData, "cookie2=val2")
	}
}

func TestSearchSeries(t *testing.T) {
	s := newTestStore(t)

	s.AddSeries(models.Series{Title: "Harry Potter", Author: "J.K. Rowling", SourceURL: "https://example.com/1", ProviderName: "test", Status: "active"})
	s.AddSeries(models.Series{Title: "Lord of the Rings", Author: "Tolkien", SourceURL: "https://example.com/2", ProviderName: "test", Status: "active"})

	results, err := s.SearchSeries("harry")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("SearchSeries('harry') = %d results, want 1", len(results))
	}

	results, err = s.SearchSeries("tolkien")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("SearchSeries('tolkien') = %d results, want 1", len(results))
	}

	results, err = s.SearchSeries("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("SearchSeries('nonexistent') = %d results, want 0", len(results))
	}
}

func TestDashboardStats(t *testing.T) {
	s := newTestStore(t)

	sid1, _ := s.AddSeries(models.Series{Title: "Active", SourceURL: "https://example.com/1", ProviderName: "test", Status: "active"})
	s.AddSeries(models.Series{Title: "Dropped", SourceURL: "https://example.com/2", ProviderName: "test", Status: "dropped"})
	s.InsertChapters(sid1, []models.Chapter{
		{SeriesID: sid1, Title: "Ch 1", URL: "https://example.com/ch1"},
		{SeriesID: sid1, Title: "Ch 2", URL: "https://example.com/ch2"},
	})

	stats, err := s.GetDashboardStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalSeries != 2 {
		t.Errorf("TotalSeries = %d, want 2", stats.TotalSeries)
	}
	if stats.ActiveSeries != 1 {
		t.Errorf("ActiveSeries = %d, want 1", stats.ActiveSeries)
	}
	if stats.UnreadChapter != 2 {
		t.Errorf("UnreadChapter = %d, want 2", stats.UnreadChapter)
	}
}

func TestDashboardSorting(t *testing.T) {
	s := newTestStore(t)

	sid1, _ := s.AddSeries(models.Series{Title: "Low Rated", SourceURL: "https://example.com/1", ProviderName: "test", Status: "active", Rating: 2})
	sid2, _ := s.AddSeries(models.Series{Title: "High Rated", SourceURL: "https://example.com/2", ProviderName: "test", Status: "active", Rating: 9})

	s.InsertChapters(sid1, []models.Chapter{{SeriesID: sid1, Title: "Ch", URL: "https://example.com/lch"}})
	s.InsertChapters(sid2, []models.Chapter{{SeriesID: sid2, Title: "Ch", URL: "https://example.com/hch"}})

	dashboard, _ := s.GetSeriesView()
	if len(dashboard) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(dashboard))
	}
	if dashboard[0].Series.Rating != 2 {
		t.Errorf("first group rating = %f, want 2 (sorted ASC)", dashboard[0].Series.Rating)
	}
	if dashboard[1].Series.Rating != 9 {
		t.Errorf("second group rating = %f, want 9", dashboard[1].Series.Rating)
	}
}
