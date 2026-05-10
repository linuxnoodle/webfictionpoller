package opml

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head><title>Test</title></head>
  <body>
    <outline text="RoyalRoad">
      <outline text="Story One" type="rss" xmlUrl="https://www.royalroad.com/syndication/12345" htmlUrl="https://www.royalroad.com/fiction/12345/story-one"/>
      <outline text="Story Two" type="rss" xmlUrl="https://www.royalroad.com/syndication/67890" htmlUrl="https://www.royalroad.com/fiction/67890/story-two"/>
    </outline>
    <outline text="SpaceBattles">
      <outline text="Thread One" type="rss" xmlUrl="https://forums.spacebattles.com/threads/thread-one.111/threadmarks.rss?threadmark_category=1" htmlUrl="https://forums.spacebattles.com/"/>
    </outline>
  </body>
</opml>`

	feeds, err := Parse(strings.NewReader(xml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(feeds) != 3 {
		t.Fatalf("expected 3 feeds, got %d", len(feeds))
	}

	if feeds[0].Title != "Story One" {
		t.Errorf("feeds[0].Title = %q, want %q", feeds[0].Title, "Story One")
	}
	if feeds[0].Category != "RoyalRoad" {
		t.Errorf("feeds[0].Category = %q, want %q", feeds[0].Category, "RoyalRoad")
	}
	if feeds[0].FeedURL != "https://www.royalroad.com/syndication/12345" {
		t.Errorf("feeds[0].FeedURL = %q", feeds[0].FeedURL)
	}
	if feeds[2].Title != "Thread One" {
		t.Errorf("feeds[2].Title = %q, want %q", feeds[2].Title, "Thread One")
	}
	if feeds[2].Category != "SpaceBattles" {
		t.Errorf("feeds[2].Category = %q, want %q", feeds[2].Category, "SpaceBattles")
	}
}

func TestParse_URLAsTitle(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <body>
    <outline text="https://forums.spacebattles.com/threads/my-story.12345/threadmarks.rss?threadmark_category=1" type="rss" xmlUrl="https://forums.spacebattles.com/threads/my-story.12345/threadmarks.rss?threadmark_category=1" htmlUrl="https://forums.spacebattles.com/"/>
  </body>
</opml>`

	feeds, err := Parse(strings.NewReader(xml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(feeds) != 1 {
		t.Fatalf("expected 1 feed, got %d", len(feeds))
	}

	if feeds[0].Title != "my story" {
		t.Errorf("Title = %q, want %q (extracted from URL)", feeds[0].Title, "my story")
	}
}

func TestParse_EmptyOutlines(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <body>
    <outline text="fanfiction.net"/>
  </body>
</opml>`

	feeds, err := Parse(strings.NewReader(xml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(feeds) != 0 {
		t.Errorf("expected 0 feeds for empty outline, got %d", len(feeds))
	}
}

func TestParse_SufficientVelocity(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <body>
    <outline text="SufficientVelocity">
      <outline text="SV Thread" type="rss" xmlUrl="https://forums.sufficientvelocity.com/threads/sv-thread.99999/threadmarks.rss?threadmark_category=1" htmlUrl="https://forums.sufficientvelocity.com/"/>
    </outline>
  </body>
</opml>`

	feeds, err := Parse(strings.NewReader(xml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(feeds) != 1 {
		t.Fatalf("expected 1 feed, got %d", len(feeds))
	}
	if feeds[0].Title != "SV Thread" {
		t.Errorf("Title = %q, want %q", feeds[0].Title, "SV Thread")
	}
	if feeds[0].FeedURL != "https://forums.sufficientvelocity.com/threads/sv-thread.99999/threadmarks.rss?threadmark_category=1" {
		t.Errorf("FeedURL = %q", feeds[0].FeedURL)
	}
}
