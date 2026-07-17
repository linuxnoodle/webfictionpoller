package handlers

import (
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func addSeriesForSourceTest(t *testing.T, s *Store) *models.Series {
	t.Helper()
	id, err := s.AddSeries(models.Series{
		Title:        "Multi-Source Test",
		Author:       "Author",
		SourceURL:    "https://www.royalroad.com/fiction/999/test",
		ProviderName: "royalroad",
		Rating:       -1,
		Status:       "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	ser, err := s.GetSeriesByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return ser
}

func TestAddSourceSeedsPrimaryOnLegacySeries(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)

	// The series_sources_seed migration should have populated one row
	// mirroring series.source_url + provider_name.
	sources, err := s.ListSources(ser.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected seed row, got %d sources: %+v", len(sources), sources)
	}
	if !sources[0].IsPrimary {
		t.Error("seeded source should be primary")
	}
	if sources[0].ProviderName != "royalroad" {
		t.Errorf("seeded provider = %q, want royalroad", sources[0].ProviderName)
	}
}

func TestAddSourceFirstSourceIsPrimary(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)
	// Drop the seed row to test "no sources" path.
	if _, err := s.db.Exec("DELETE FROM series_sources WHERE series_id = ?", ser.ID); err != nil {
		t.Fatal(err)
	}

	src, err := s.AddSource(ser.ID, "royalroad", "https://www.royalroad.com/fiction/999/test", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !src.IsPrimary {
		t.Error("first added source should auto-become primary")
	}
	// Series' denormalized columns should mirror.
	got, _ := s.GetSeriesByID(ser.ID)
	if got.ProviderName != "royalroad" || got.SourceURL != src.SourceURL {
		t.Errorf("series denorm not updated: %+v", got)
	}
}

func TestAddSourceAlternateIsNotPrimary(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)

	alt, err := s.AddSource(ser.ID, "ao3", "https://archiveofourown.org/works/12345", 100)
	if err != nil {
		t.Fatal(err)
	}
	if alt.IsPrimary {
		t.Error("alternate source should not be primary")
	}
	sources, _ := s.ListSources(ser.ID)
	if len(sources) != 2 {
		t.Errorf("expected 2 sources, got %d", len(sources))
	}
}

func TestAddSourceDuplicateURLIsNoop(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)

	url := "https://archiveofourown.org/works/12345"
	if _, err := s.AddSource(ser.ID, "ao3", url, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSource(ser.ID, "ao3", url, 50); err != nil {
		t.Fatal(err)
	}
	sources, _ := s.ListSources(ser.ID)
	if len(sources) != 2 { // seed + one alternate (not 3)
		t.Errorf("duplicate add should be a no-op; got %d sources", len(sources))
	}
}

func TestPromoteSource(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)

	alt, err := s.AddSource(ser.ID, "ao3", "https://archiveofourown.org/works/12345", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteSource(alt.ID); err != nil {
		t.Fatal(err)
	}

	// Alt is now primary; original primary is demoted.
	sources, _ := s.ListSources(ser.ID)
	var primary, altReloaded *models.SeriesSource
	for i := range sources {
		if sources[i].ID == alt.ID {
			altReloaded = &sources[i]
		}
		if sources[i].IsPrimary {
			primary = &sources[i]
		}
	}
	if primary == nil || primary.ID != alt.ID {
		t.Errorf("expected alt to be primary, got %+v", primary)
	}
	if altReloaded.Priority != 0 {
		t.Errorf("promoted source should have priority 0, got %d", altReloaded.Priority)
	}

	// Series' denormalized columns should reflect new primary.
	got, _ := s.GetSeriesByID(ser.ID)
	if got.ProviderName != "ao3" {
		t.Errorf("series provider_name = %q, want ao3", got.ProviderName)
	}
}

func TestDeleteSourcePromotesReplacementWhenPrimaryRemoved(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)

	// Add an alternate, then promote it. The original seed becomes an alternate.
	alt, _ := s.AddSource(ser.ID, "ao3", "https://archiveofourown.org/works/12345", 100)
	if err := s.PromoteSource(alt.ID); err != nil {
		t.Fatal(err)
	}

	// Find the original seed (now an alternate).
	sources, _ := s.ListSources(ser.ID)
	var seedID int64
	for _, src := range sources {
		if src.ProviderName == "royalroad" {
			seedID = src.ID
		}
	}
	if seedID == 0 {
		t.Fatal("couldn't find seed source")
	}

	// Delete the alt (current primary). The seed should auto-promote.
	if err := s.DeleteSource(alt.ID); err != nil {
		t.Fatal(err)
	}
	sources, _ = s.ListSources(ser.ID)
	if len(sources) != 1 {
		t.Fatalf("expected 1 remaining source, got %d", len(sources))
	}
	if !sources[0].IsPrimary {
		t.Error("remaining source should be promoted to primary")
	}
}

func TestDeleteLastSourceRejected(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)

	sources, _ := s.ListSources(ser.ID)
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	err := s.DeleteSource(sources[0].ID)
	if err != ErrLastSource {
		t.Errorf("expected ErrLastSource, got %v", err)
	}
}

func TestRecordSourceOKResetsFails(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)
	src, _ := s.AddSource(ser.ID, "ao3", "https://archiveofourown.org/works/12345", 100)

	_ = s.RecordSourceFail(src.ID, "boom")
	_ = s.RecordSourceFail(src.ID, "boom2")
	reload, _ := s.GetSourceByID(src.ID)
	if reload.ConsecutiveFails != 2 {
		t.Errorf("expected 2 fails, got %d", reload.ConsecutiveFails)
	}
	if reload.LastError != "boom2" {
		t.Errorf("last error = %q, want boom2", reload.LastError)
	}

	_ = s.RecordSourceOK(src.ID)
	reload, _ = s.GetSourceByID(src.ID)
	if reload.ConsecutiveFails != 0 {
		t.Errorf("expected 0 fails after OK, got %d", reload.ConsecutiveFails)
	}
	if reload.LastError != "" {
		t.Errorf("last error should be cleared, got %q", reload.LastError)
	}
	if reload.LastOK == nil {
		t.Error("last_ok should be set after OK")
	}
}

func TestAutoPromoteIfFailingBelowThreshold(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)
	alt, _ := s.AddSource(ser.ID, "ao3", "https://archiveofourown.org/works/12345", 100)

	// Primary fails 5 times; threshold is 10. No promotion.
	for i := 0; i < 5; i++ {
		_ = s.RecordSourceFail(ser.ID, "x")
	}
	promoted, err := s.AutoPromoteIfFailing(ser.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if promoted != 0 {
		t.Errorf("expected no promotion, got %d", promoted)
	}
	_ = alt
}

func TestAutoPromoteIfFailingAboveThreshold(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)
	alt, _ := s.AddSource(ser.ID, "ao3", "https://archiveofourown.org/works/12345", 100)

	// Primary fails 10 times; threshold is 5. Should promote the alt.
	sources, _ := s.ListSources(ser.ID)
	var primaryID int64
	for _, src := range sources {
		if src.IsPrimary {
			primaryID = src.ID
		}
	}
	for i := 0; i < 10; i++ {
		_ = s.RecordSourceFail(primaryID, "x")
	}

	promoted, err := s.AutoPromoteIfFailing(ser.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if promoted != alt.ID {
		t.Errorf("expected promotion of alt id %d, got %d", alt.ID, promoted)
	}

	// Verify primary actually swapped.
	sources, _ = s.ListSources(ser.ID)
	for _, src := range sources {
		if src.ID == alt.ID && !src.IsPrimary {
			t.Error("alt should be primary after auto-promotion")
		}
	}
}

func TestUpdateSourcePriorityAndDisabled(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)
	src, _ := s.AddSource(ser.ID, "ao3", "https://archiveofourown.org/works/12345", 100)

	if err := s.UpdateSource(src.ID, 50, true); err != nil {
		t.Fatal(err)
	}
	reload, _ := s.GetSourceByID(src.ID)
	if reload.Priority != 50 {
		t.Errorf("priority = %d, want 50", reload.Priority)
	}
	if !reload.Disabled {
		t.Error("should be disabled")
	}
}

func TestActiveSourcesForPollExcludesDisabled(t *testing.T) {
	s := newTestStore(t)
	ser := addSeriesForSourceTest(t, s)
	src, _ := s.AddSource(ser.ID, "ao3", "https://archiveofourown.org/works/12345", 100)
	_ = s.UpdateSource(src.ID, -1, true)

	active, err := s.ActiveSourcesForPoll(ser.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range active {
		if a.ID == src.ID {
			t.Error("disabled source should not appear in ActiveSourcesForPoll")
		}
	}
}
