//go:build !no_integration

package plugin_test

import (
	"testing"

	// Side-effect imports: trigger init() self-registration.
	_ "github.com/linuxnoodle/webfictionpoller/internal/comics"
	_ "github.com/linuxnoodle/webfictionpoller/internal/provider/text/dreamy"
	_ "github.com/linuxnoodle/webfictionpoller/internal/providers"

	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

// expectedRegistered maps provider name to its expected kind + the set of
// capabilities it should expose. Extend this map when adding a built-in.
var expectedRegistered = map[string]struct {
	Kind        plugin.Kind
	Capabilities []interface{}
}{
	"royalroad":          {Kind: plugin.KindText, Capabilities: []interface{}{(*plugin.Poller)(nil), (*plugin.SeriesLister)(nil), (*plugin.HTMLFetcher)(nil), (*plugin.CommentFetcher)(nil)}},
	"spacebattles":       {Kind: plugin.KindText, Capabilities: []interface{}{(*plugin.Poller)(nil), (*plugin.SeriesLister)(nil), (*plugin.HTMLFetcher)(nil), (*plugin.CommentFetcher)(nil)}},
	"sufficientvelocity": {Kind: plugin.KindText, Capabilities: []interface{}{(*plugin.Poller)(nil), (*plugin.SeriesLister)(nil), (*plugin.HTMLFetcher)(nil), (*plugin.CommentFetcher)(nil)}},
	"questionablequesting": {Kind: plugin.KindText, Capabilities: []interface{}{(*plugin.Poller)(nil), (*plugin.SeriesLister)(nil), (*plugin.HTMLFetcher)(nil), (*plugin.CommentFetcher)(nil), (*plugin.LoginAuth)(nil), (*plugin.CookieAuth)(nil), (*plugin.CredentialSource)(nil)}},
	"fanfictionnet": {Kind: plugin.KindText, Capabilities: []interface{}{(*plugin.Poller)(nil), (*plugin.SeriesLister)(nil), (*plugin.HTMLFetcher)(nil)}},
	"ao3":           {Kind: plugin.KindText, Capabilities: []interface{}{(*plugin.Poller)(nil), (*plugin.SeriesLister)(nil), (*plugin.HTMLFetcher)(nil)}},
	"mangadex":      {Kind: plugin.KindComic, Capabilities: []interface{}{(*plugin.Searcher)(nil), (*plugin.ComicDetailsFetcher)(nil), (*plugin.ChapterLister)(nil), (*plugin.PageLister)(nil)}},
	"dreamytranslations": {Kind: plugin.KindText, Capabilities: []interface{}{(*plugin.Poller)(nil), (*plugin.SeriesLister)(nil), (*plugin.ContentFetcher)(nil), (*plugin.CommentFetcher)(nil)}},
}

func TestDefaultRegistryContainsAllBuiltins(t *testing.T) {
	for name, want := range expectedRegistered {
		p, ok := plugin.Default.Get(name)
		if !ok {
			t.Errorf("expected provider %q registered, not found", name)
			continue
		}
		if p.Meta().Kind != want.Kind {
			t.Errorf("provider %q kind = %v, want %v", name, p.Meta().Kind, want.Kind)
		}
		if p.Meta().DisplayName == "" {
			t.Errorf("provider %q has empty DisplayName", name)
		}
		if p.Meta().FaviconURL == "" {
			t.Errorf("provider %q has empty FaviconURL", name)
		}
		for _, cap := range want.Capabilities {
			if !plugin.DefaultImplements(p, cap) {
				t.Errorf("provider %q missing capability %T", name, cap)
			}
		}
	}
}

func TestDefaultRegistryNoUnknownBuiltins(t *testing.T) {
	got := plugin.Default.Names()
	if len(got) != len(expectedRegistered) {
		t.Errorf("registry has %d providers, expected %d (got=%v)", len(got), len(expectedRegistered), got)
	}
	for _, name := range got {
		if _, ok := expectedRegistered[name]; !ok {
			t.Errorf("registry contains unexpected provider %q; update expectedRegistered in this test", name)
		}
	}
}

func TestDefaultRegistryByURL(t *testing.T) {
	cases := map[string]string{
		"https://www.royalroad.com/fiction/123/x":            "royalroad",
		"https://forums.spacebattles.com/threads/x.1/":       "spacebattles",
		"https://forums.sufficientvelocity.com/threads/x.1/": "sufficientvelocity",
		"https://forum.questionablequesting.com/threads/x.1/": "questionablequesting",
		"https://www.fanfiction.net/s/123/1/x":               "fanfictionnet",
		"https://archiveofourown.org/works/123":              "ao3",
		"https://mangadex.org/title/abc/title":               "mangadex",
	}
	for url, wantName := range cases {
		p, ok := plugin.Default.ByURL(url)
		if !ok {
			t.Errorf("ByURL(%q) returned no provider", url)
			continue
		}
		if p.Meta().Name != wantName {
			t.Errorf("ByURL(%q) = %q, want %q", url, p.Meta().Name, wantName)
		}
	}
}
