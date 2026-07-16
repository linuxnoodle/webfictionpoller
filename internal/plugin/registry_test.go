package plugin

import (
	"errors"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// stubProvider implements only the base Provider.
type stubProvider struct{ meta Meta }

func (s *stubProvider) Meta() Meta                       { return s.meta }
func (s *stubProvider) MatchURL(rawURL string) bool      { return rawURL == "stub://"+s.meta.Name }

// textStub implements Poller + SeriesLister + HTMLFetcher.
type textStub struct{ stubProvider }

func (t *textStub) FetchSeriesMetadata(string) (models.Series, error) { return models.Series{}, nil }
func (t *textStub) PollUpdates(models.Series) ([]models.Chapter, error) {
	return nil, nil
}
func (t *textStub) FetchChapterContent(string) (string, error) { return "", nil }

// comicStub implements Searcher + ChapterLister + PageLister.
type comicStub struct{ stubProvider }

func (c *comicStub) Search(string, int) (*models.MangasPage, error) { return nil, nil }
func (c *comicStub) MangaDetails(string) (*models.ComicSeries, error) {
	return nil, nil
}
func (c *comicStub) ChapterList(string) ([]models.ComicChapter, error) { return nil, nil }
func (c *comicStub) PageList(string) ([]models.ComicPage, error)       { return nil, nil }

func newStub(name string, kind Kind) *stubProvider {
	return &stubProvider{meta: Meta{Name: name, DisplayName: name, Kind: kind}}
}

func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{meta: Meta{Name: "rr"}})
	p, ok := r.Get("rr")
	if !ok || p == nil {
		t.Fatal("expected to find registered provider")
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("expected missing provider to return false")
	}
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{meta: Meta{Name: "x"}})
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r.Register(&stubProvider{meta: Meta{Name: "x"}})
}

func TestRegisterPanicsOnEmptyName(t *testing.T) {
	r := NewRegistry()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on empty name")
		}
	}()
	r.Register(&stubProvider{meta: Meta{Name: ""}})
}

func TestAllPreservesOrder(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"a", "b", "c"} {
		r.Register(&stubProvider{meta: Meta{Name: n}})
	}
	got := r.Names()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestByURL(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{meta: Meta{Name: "a"}})
	r.Register(&stubProvider{meta: Meta{Name: "b"}})
	p, ok := r.ByURL("stub://b")
	if !ok || p.Meta().Name != "b" {
		t.Fatalf("expected b, got %+v ok=%v", p, ok)
	}
	if _, ok := r.ByURL("unknown://x"); ok {
		t.Fatal("expected miss for unknown URL")
	}
}

func TestByKind(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{meta: Meta{Name: "rr", Kind: KindText}})
	r.Register(&stubProvider{meta: Meta{Name: "md", Kind: KindComic}})
	if got := r.ByKind(KindText); len(got) != 1 || got[0].Meta().Name != "rr" {
		t.Fatalf("ByKind text wrong: %+v", got)
	}
	if got := r.ByKind(KindComic); len(got) != 1 || got[0].Meta().Name != "md" {
		t.Fatalf("ByKind comic wrong: %+v", got)
	}
}

func TestWithCapabilityPoller(t *testing.T) {
	r := NewRegistry()
	r.Register(&textStub{stubProvider{meta: Meta{Name: "rr", Kind: KindText}}})
	r.Register(&comicStub{stubProvider{meta: Meta{Name: "md", Kind: KindComic}}})

	pollers := r.WithCapability((*Poller)(nil))
	if len(pollers) != 1 || pollers[0].Meta().Name != "rr" {
		t.Fatalf("expected only rr as Poller, got %+v", names(pollers))
	}
	searchers := r.WithCapability((*Searcher)(nil))
	if len(searchers) != 1 || searchers[0].Meta().Name != "md" {
		t.Fatalf("expected only md as Searcher, got %+v", names(searchers))
	}
	listers := r.WithCapability((*SeriesLister)(nil))
	if len(listers) != 1 || listers[0].Meta().Name != "rr" {
		t.Fatalf("expected only rr as SeriesLister, got %+v", names(listers))
	}
}

func TestWithCapabilityRejectsNonInterfacePointer(t *testing.T) {
	r := NewRegistry()
	r.Register(&textStub{stubProvider{meta: Meta{Name: "rr"}}})
	// Passing a non-nil concrete value should return nothing rather than panic.
	got := r.WithCapability("not an interface pointer")
	if len(got) != 0 {
		t.Fatalf("expected empty result for bogus capability, got %+v", got)
	}
}

func TestHostMatch(t *testing.T) {
	cases := []struct {
		url  string
		host string
		want bool
	}{
		{"https://www.fanfiction.net/s/123", "fanfiction.net", true},
		{"https://fanfiction.net/s/123", "fanfiction.net", true},
		{"https://forums.spacebattles.com/threads/x.1/", "forums.spacebattles.com", true},
		{"https://example.com/", "fanfiction.net", false},
		{"://broken", "fanfiction.net", false},
	}
	for _, c := range cases {
		if got := HostMatch(c.url, c.host); got != c.want {
			t.Errorf("HostMatch(%q,%q)=%v want %v", c.url, c.host, got, c.want)
		}
	}
}

// guard against accidental interface contract drift in the comics package.
var _ = errors.New

func names(ps []Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Meta().Name
	}
	return out
}
