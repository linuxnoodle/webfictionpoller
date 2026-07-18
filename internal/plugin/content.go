// Package plugin — content.go
//
// Canonical chapter-content shape that every book plugin returns from a
// content fetch. Plugins convert whatever source format their site uses
// (HTML, JSON API, markdown, etc.) into this struct. The storage layer
// (archiver) and reader pipeline consume only this shape — they never see
// raw HTML and never branch on provider type.
//
// Two related concepts:
//
//   - ChapterContent is the data shape (what gets persisted).
//   - ContentFetcher  is the capability interface (how the provider exposes it).
//
// Every book plugin (KindText) must implement ContentFetcher. Comic plugins
// (KindComic) use a different flow: PageLister returns image URLs which the
// download tracker pulls into the blob store directly — no ChapterContent
// equivalent because there's no prose to extract.

package plugin

import "time"

// ChapterContent is the canonical parsed-chapter shape returned by every
// book provider's content fetch. The provider is the only code that knows
// the source format; downstream code works exclusively against this struct.
type ChapterContent struct {
	// Title is the human-readable chapter title, stripped of decorations
	// like "Ch. N" prefixes or word counts. Required (non-empty) for a
	// successful free-chapter fetch.
	Title string

	// BodyHTML is the chapter prose as sanitized HTML — paragraphs,
	// inline images, basic formatting. Site chrome (nav, ads, related
	// links, sidebars) must be stripped by the provider before returning.
	// Sanitization policy is applied downstream by the archiver; providers
	// return extracted-but-policy-unfiltered HTML.
	BodyHTML string

	// BodyText is a plain-text rendering of BodyHTML, used for full-text
	// search and quick previews. Optional: if empty, storage derives it
	// lazily by stripping tags from BodyHTML.
	BodyText string

	// PublishedAt is when the chapter was published on the source site.
	// Zero value if unknown (some sites don't expose per-chapter dates).
	PublishedAt time.Time

	// WordCount is the prose word count, excluding markup and site chrome.
	// 0 if the provider didn't compute it; storage falls back to counting
	// words in BodyText when this is zero.
	WordCount int

	// Premium indicates the chapter is paywalled and the content was not
	// retrievable without authentication. When true, BodyHTML must be
	// empty (the provider couldn't fetch it). Storage marks the chapter
	// row so the UI can display a "locked" badge and the scheduler
	// doesn't retry the fetch indefinitely.
	Premium bool

	// AuthorNote is an optional author's-note section, when the site
	// separates it from the main body. Empty if the site doesn't split
	// notes from prose.
	AuthorNote string

	// Images lists every image URL referenced in BodyHTML that should be
	// cached locally by the archiver. Empty if the chapter has no images.
	// The archiver fetches each via safefetch and stores bytes in the
	// blob store / chapter_images table.
	Images []string

	// SourceURL is the canonical URL of the chapter on the source site.
	// Echoed here so consumers (archiver, OPDS) don't need to thread the
	// original URL through separately.
	SourceURL string
}

// ContentFetcher is the chapter-sync capability for book plugins.
// Implementations download a single chapter from the source site, parse
// it into the canonical ChapterContent shape, and return it for storage.
//
// This is the "sync to storage" half of the two-interface plugin contract.
// The other half is Poller (chapter-update detection). Every book plugin
// must implement both.
//
// Providers that still implement the legacy HTMLFetcher interface (returns
// a raw HTML string) are auto-wrapped by AdaptHTMLFetcher below so they
// satisfy ContentFetcher without code changes during the migration.
type ContentFetcher interface {
	FetchChapter(url string) (ChapterContent, error)
}

// AdaptHTMLFetcher wraps a legacy HTMLFetcher so it satisfies ContentFetcher.
// The returned ChapterContent has only BodyHTML populated; Title, WordCount,
// PublishedAt, etc. are left zero. This is the migration bridge: existing
// providers keep working unchanged while the storage layer moves to the
// unified ContentFetcher contract.
//
// Providers should implement ContentFetcher directly when they want to
// surface richer data (parsed title, word count, premium flag, image list).
// The adapter is a fallback, not a target.
func AdaptHTMLFetcher(h HTMLFetcher) ContentFetcher {
	return &htmlFetcherAdapter{h: h}
}

type htmlFetcherAdapter struct{ h HTMLFetcher }

func (a *htmlFetcherAdapter) FetchChapter(url string) (ChapterContent, error) {
	html, err := a.h.FetchChapterContent(url)
	if err != nil {
		return ChapterContent{}, err
	}
	return ChapterContent{
		BodyHTML:  html,
		SourceURL: url,
	}, nil
}

// AsContentFetcher returns p's native ContentFetcher implementation when it
// has one, else falls back to AdaptHTMLFetcher(p) when p implements the
// legacy HTMLFetcher. Returns nil when p implements neither (e.g. a
// comic provider, which has no prose to fetch).
//
// Usage in the archiver / reader pipeline:
//
//	if cf := plugin.AsContentFetcher(p); cf != nil {
//	    content, err := cf.FetchChapter(url)
//	    ...
//	}
func AsContentFetcher(p Provider) ContentFetcher {
	if cf, ok := p.(ContentFetcher); ok {
		return cf
	}
	if h, ok := p.(HTMLFetcher); ok {
		return AdaptHTMLFetcher(h)
	}
	return nil
}
