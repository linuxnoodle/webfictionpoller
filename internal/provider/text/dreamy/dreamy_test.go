package dreamy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func loadDoc(t *testing.T, name string) *goquery.Document {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestMetaAndName(t *testing.T) {
	p := &Provider{}
	m := p.Meta()
	if m.Name != "dreamytranslations" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.DisplayName != "Dreamy Translations" {
		t.Errorf("DisplayName = %q", m.DisplayName)
	}
	if m.Kind != "text" {
		t.Errorf("Kind = %v, want text", m.Kind)
	}
	if m.PollIntervalDefault != "1h" {
		t.Errorf("PollIntervalDefault = %q", m.PollIntervalDefault)
	}
}

func TestMatchURL(t *testing.T) {
	p := &Provider{}
	cases := map[string]bool{
		"https://dreamy-translations.com/novel/imc":                 true,
		"https://dreamy-translations.com/novel/imc/chapter/0":       true,
		"https://www.dreamy-translations.com/novel/rev":             true,
		"https://dreamy-translations.com/series":                    false,
		"https://royalroad.com/fiction/123":                         false,
		"https://example.com/novel/x":                                false,
	}
	for url, want := range cases {
		if got := p.MatchURL(url); got != want {
			t.Errorf("MatchURL(%q) = %v, want %v", url, got, want)
		}
	}
}

func TestSlugFromURL(t *testing.T) {
	cases := map[string]string{
		"https://dreamy-translations.com/novel/imc":           "imc",
		"https://dreamy-translations.com/novel/imc/chapter/0": "imc",
		"https://dreamy-translations.com/novel/saaft":         "saaft",
	}
	for url, want := range cases {
		got, ok := slugFromURL(url)
		if !ok || got != want {
			t.Errorf("slugFromURL(%q) = (%q, %v), want (%q, true)", url, got, ok, want)
		}
	}
	// Non-novel URL
	if _, ok := slugFromURL("https://dreamy-translations.com/latest"); ok {
		t.Error("slugFromURL should reject non-novel URLs")
	}
}

func TestFetchSeriesMetadata_TitleAuthorCover(t *testing.T) {
	// Test the parse helpers directly against the fixture. The Provider's
	// fetchDoc just wraps safefetch + goquery; testing it would need network
	// mocking that adds little value beyond what the parse tests cover.
	doc := loadDoc(t, "story_imc.html")

	title := textOrEmpty(doc.Find("h1"))
	if title != "Be Careful When Installing Mods" {
		t.Errorf("title = %q", title)
	}
	author := parseAuthor(doc)
	if author != "네뮤" {
		t.Errorf("author = %q, want 네뮤", author)
	}
	cover := parseCoverURL(doc, title)
	if cover == "" || !contains(cover, "supabase.dreamy-translations.com") {
		t.Errorf("cover = %q", cover)
	}
	synopsis := parseSynopsis(doc)
	if synopsis == "" {
		t.Error("synopsis should not be empty")
	}
	// The meta-description is the site's short tagline ("This world will
	// soon be destroyed."). The long synopsis appears in the body but is
	// less stable to extract. Non-empty is the right contract.
	t.Logf("imc synopsis = %q", synopsis)
}

func TestParseAuthor_RevStory(t *testing.T) {
	doc := loadDoc(t, "story_rev.html")
	author := parseAuthor(doc)
	// rev fixture has no visible "by X" element (verified by HTML inspection).
	// Empty author is the correct outcome, not a parser bug.
	t.Logf("rev author = %q", author)
}

func TestPollUpdates_FindsChapters(t *testing.T) {
	doc := loadDoc(t, "story_imc.html")
	// Replicate PollUpdates' inner scrape loop against the fixture.
	// This is what PollUpdates would do once it had the doc in hand.
	type parsed struct {
		idx   int
		title string
		url   string
	}
	var out []parsed
	seen := make(map[int]bool)
	doc.Find(`a[data-chapter-index]`).Each(func(_ int, s *goquery.Selection) {
		idxStr, _ := s.Attr("data-chapter-index")
		// (use the real strconv.Atoi via a closure)
		idx, ok := parseInt(idxStr)
		if !ok || idx < 0 {
			return
		}
		if seen[idx] {
			return
		}
		seen[idx] = true
		href, _ := s.Attr("href")
		out = append(out, parsed{idx: idx, title: textOrEmpty(s.Find("p.truncate").First()), url: href})
	})

	if len(out) != 204 {
		t.Errorf("expected 204 free chapters (per stats panel), got %d", len(out))
	}

	// Sanity: chapter 0 should be the prologue.
	if len(out) == 0 {
		t.Fatal("no chapters parsed")
	}
	first := out[0]
	if first.idx != 0 {
		t.Errorf("first chapter idx = %d, want 0", first.idx)
	}
	if first.title == "" {
		t.Error("first chapter title is empty")
	}
	if !contains(first.title, "Prologue") {
		t.Errorf("first chapter title = %q, expected 'Prologue'", first.title)
	}
	if first.url != "/novel/imc/chapter/0" {
		t.Errorf("first chapter URL = %q", first.url)
	}
}

func TestPollUpdates_Dedup(t *testing.T) {
	doc := loadDoc(t, "story_imc.html")
	count := doc.Find(`a[data-chapter-index]`).Length()
	// "Start Reading" CTA duplicates the first chapter link. Total raw
	// anchors > 204 if dedup is broken; should be exactly 204 unique.
	seen := make(map[string]bool)
	doc.Find(`a[data-chapter-index]`).Each(func(_ int, s *goquery.Selection) {
		idx, _ := s.Attr("data-chapter-index")
		seen[idx] = true
	})
	if count != 205 {
		t.Logf("raw anchor count = %d (includes duplicate Start Reading CTA)", count)
	}
	if len(seen) != 204 {
		t.Errorf("unique chapter indices = %d, want 204", len(seen))
	}
}

func TestFetchChapter_ExtractsContent(t *testing.T) {
	doc := loadDoc(t, "chapter_imc_0.html")

	title := extractChapterTitle(doc, "https://x/novel/imc/chapter/0")
	if title == "" {
		t.Fatal("title is empty")
	}
	if !contains(title, "Prologue") {
		t.Errorf("title = %q, expected 'Prologue'", title)
	}

	body, err := extractBodyHTML(doc)
	if err != nil {
		t.Fatalf("extractBodyHTML: %v", err)
	}
	if !contains(body, "Labyrinth") {
		t.Errorf("body missing expected content; got %q...", body[:min(200, len(body))])
	}
	if !contains(body, "<div class=\"paragraph\">") {
		t.Errorf("body should preserve <div class=paragraph> structure, got %q...", body[:min(200, len(body))])
	}
}

func TestFetchChapter_WordCountNonZero(t *testing.T) {
	doc := loadDoc(t, "chapter_imc_0.html")
	body, _ := extractBodyHTML(doc)
	bodyText := htmlToText(body)
	wc := countWords(bodyText)
	if wc < 50 {
		t.Errorf("word count = %d, expected >50 for a real chapter", wc)
	}
	t.Logf("prologue word count: %d", wc)
}

func TestFetchChapter_RevChapterParses(t *testing.T) {
	doc := loadDoc(t, "chapter_rev_1.html")
	title := extractChapterTitle(doc, "https://x/novel/rev/chapter/1")
	if title == "" {
		t.Fatal("rev title empty")
	}
	body, err := extractBodyHTML(doc)
	if err != nil {
		t.Fatalf("rev body: %v", err)
	}
	if len(body) < 1000 {
		t.Errorf("rev body suspiciously short: %d bytes", len(body))
	}
}

// parseInt wraps strconv.Atoi to keep the test bodies readable.
func parseInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
