package plugin

import (
	"errors"
	"strings"
	"testing"
)

// stubHTMLFetcher implements the legacy HTMLFetcher only.
type stubHTMLFetcher struct{ html string }

func (s stubHTMLFetcher) FetchChapterContent(url string) (string, error) {
	return s.html, nil
}

// stubContentFetcher implements the new ContentFetcher directly.
type stubContentFetcher struct{}

func (stubContentFetcher) Meta() Meta                    { return Meta{Name: "x"} }
func (stubContentFetcher) MatchURL(string) bool           { return false }
func (stubContentFetcher) FetchChapter(url string) (ChapterContent, error) {
	return ChapterContent{Title: "T", BodyHTML: "<p>hi</p>", WordCount: 2, SourceURL: url}, nil
}

// stubHTMLFetcherProvider satisfies the base Provider (Meta + MatchURL) AND
// legacy HTMLFetcher, but NOT ContentFetcher. AsContentFetcher should wrap it.
type stubHTMLFetcherProvider struct{ stubHTMLFetcher }

func (stubHTMLFetcherProvider) Meta() Meta          { return Meta{Name: "legacy"} }
func (stubHTMLFetcherProvider) MatchURL(string) bool { return false }

// stubNeitherProvider implements only the base Provider (no content capability).
type stubNeitherProvider struct{}

func (stubNeitherProvider) Meta() Meta          { return Meta{Name: "comic"} }
func (stubNeitherProvider) MatchURL(string) bool { return false }

func TestAdaptHTMLFetcher_WrapsBodyOnly(t *testing.T) {
	cf := AdaptHTMLFetcher(&stubHTMLFetcher{html: "<p>raw</p>"})
	got, err := cf.FetchChapter("https://x.example/ch/1")
	if err != nil {
		t.Fatal(err)
	}
	if got.BodyHTML != "<p>raw</p>" {
		t.Errorf("BodyHTML = %q, want %q", got.BodyHTML, "<p>raw</p>")
	}
	if got.Title != "" {
		t.Errorf("Title should be empty under adapter, got %q", got.Title)
	}
	if got.WordCount != 0 {
		t.Errorf("WordCount should be zero under adapter, got %d", got.WordCount)
	}
	if got.SourceURL != "https://x.example/ch/1" {
		t.Errorf("SourceURL = %q", got.SourceURL)
	}
}

func TestAdaptHTMLFetcher_PropagatesErrors(t *testing.T) {
	cf := AdaptHTMLFetcher(errHTMLFetcher{})
	if _, err := cf.FetchChapter("x"); err == nil {
		t.Fatal("expected error to propagate")
	}
}

type errHTMLFetcher struct{}

func (errHTMLFetcher) FetchChapterContent(string) (string, error) {
	return "", errors.New("boom")
}

func TestAsContentFetcher_PrefersNative(t *testing.T) {
	var p Provider = stubContentFetcher{}
	cf := AsContentFetcher(p)
	if cf == nil {
		t.Fatal("expected ContentFetcher back")
	}
	got, _ := cf.FetchChapter("u")
	if got.Title != "T" {
		t.Errorf("expected native FetchChapter, got Title=%q", got.Title)
	}
}

func TestAsContentFetcher_FallsBackToHTMLAdapter(t *testing.T) {
	var p Provider = stubHTMLFetcherProvider{stubHTMLFetcher{html: "<p>x</p>"}}
	cf := AsContentFetcher(p)
	if cf == nil {
		t.Fatal("expected adapter to be returned")
	}
	got, _ := cf.FetchChapter("u")
	if got.BodyHTML != "<p>x</p>" {
		t.Errorf("adapter BodyHTML = %q", got.BodyHTML)
	}
	if got.Title != "" {
		t.Errorf("adapter should not populate Title, got %q", got.Title)
	}
}

func TestAsContentFetcher_NilForProviderWithoutEither(t *testing.T) {
	var p Provider = stubNeitherProvider{}
	if cf := AsContentFetcher(p); cf != nil {
		t.Errorf("expected nil for provider with neither capability, got %T", cf)
	}
}

func TestChapterContentZeroValue(t *testing.T) {
	// Document zero-value semantics. Premium chapters return this with
	// Premium=true and BodyHTML empty.
	c := ChapterContent{Premium: true, Title: "Locked", SourceURL: "u"}
	if c.Premium != true {
		t.Error("Premium not set")
	}
	if c.BodyHTML != "" {
		t.Error("BodyHTML should be empty for premium")
	}
	if !strings.Contains(c.Title, "Locked") {
		t.Errorf("Title = %q", c.Title)
	}
}
