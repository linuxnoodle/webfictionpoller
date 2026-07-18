package plugin

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func TestCountWords(t *testing.T) {
	cases := map[string]int{
		"":             0,
		"   ":          0,
		"one":          1,
		"one two":      2,
		"  one  two  ": 2,
		"a-b c d":      3,
	}
	for in, want := range cases {
		if got := CountWords(in); got != want {
			t.Errorf("CountWords(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<p>Hello <strong>world</strong>.</p><p>Second para.</p>`
	got := HTMLToText(in)
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "world") {
		t.Errorf("HTMLToText lost content: %q", got)
	}
	if strings.Contains(got, "<") {
		t.Errorf("HTMLToText should strip tags, got %q", got)
	}
}

func TestHTMLToTextEmpty(t *testing.T) {
	if got := HTMLToText(""); got != "" {
		t.Errorf("HTMLToText(\"\") = %q, want \"\"", got)
	}
}

func TestHTMLToTextInvalidHTML(t *testing.T) {
	// Not valid HTML — should pass through trimmed, not panic.
	got := HTMLToText("just plain text")
	if got != "just plain text" {
		t.Errorf("got %q", got)
	}
}

func TestExtractImageURLs(t *testing.T) {
	html := `<div>
		<img src="/rel/a.jpg">
		<img src="https://cdn.example/b.png">
		<img data-src="/lazy/c.webp">
		<img src="/rel/a.jpg">  <!-- duplicate -->
		<img>                    <!-- no src -->
	</div>`
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	got := ExtractImageURLs(doc.Find("div"), "https://site.example/ch/1")
	if len(got) != 3 {
		t.Fatalf("expected 3 unique images, got %d (%v)", len(got), got)
	}
	// First should be resolved against the base.
	if got[0] != "https://site.example/rel/a.jpg" {
		t.Errorf("img[0] = %q", got[0])
	}
	if got[1] != "https://cdn.example/b.png" {
		t.Errorf("img[1] = %q", got[1])
	}
	if got[2] != "https://site.example/lazy/c.webp" {
		t.Errorf("img[2] (lazy) = %q", got[2])
	}
}

func TestExtractImageURLsEmpty(t *testing.T) {
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(`<div></div>`))
	if got := ExtractImageURLs(doc.Find("div"), "https://x.example"); got != nil {
		t.Errorf("expected nil for no images, got %v", got)
	}
}

func TestTextOrEmpty(t *testing.T) {
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(`<p>hi</p>`))
	if got := TextOrEmpty(doc.Find("p")); got != "hi" {
		t.Errorf("got %q", got)
	}
	if got := TextOrEmpty(doc.Find(".missing")); got != "" {
		t.Errorf("expected empty for miss, got %q", got)
	}
}
