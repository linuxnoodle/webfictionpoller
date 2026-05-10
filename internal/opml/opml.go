package opml

import (
	"encoding/xml"
	"io"
	"strings"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

type OPML struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Body    Body     `xml:"body"`
}

type Body struct {
	Outlines []Outline `xml:"outline"`
}

type Outline struct {
	Text     string    `xml:"text,attr"`
	Type     string    `xml:"type,attr"`
	XMLURL   string    `xml:"xmlUrl,attr"`
	HTMLURL  string    `xml:"htmlUrl,attr"`
	Outlines []Outline `xml:"outline"`
}

type Feed struct {
	Title    string
	FeedURL  string
	SiteURL  string
	Category string
}

func Parse(r io.Reader) ([]Feed, error) {
	var opml OPML
	if err := xml.NewDecoder(r).Decode(&opml); err != nil {
		return nil, err
	}

	var feeds []Feed
	for _, outline := range opml.Body.Outlines {
		collectFeeds(outline, "", &feeds)
	}
	return feeds, nil
}

func collectFeeds(o Outline, category string, feeds *[]Feed) {
	if o.XMLURL != "" && strings.HasPrefix(o.XMLURL, "http") {
		title := o.Text
		if strings.HasPrefix(title, "http") {
			title = extractTitleFromURL(o.XMLURL)
		}
		*feeds = append(*feeds, Feed{
			Title:    title,
			FeedURL:  o.XMLURL,
			SiteURL:  o.HTMLURL,
			Category: category,
		})
	}

	cat := category
	if o.XMLURL == "" && o.Text != "" {
		cat = o.Text
	}

	for _, child := range o.Outlines {
		collectFeeds(child, cat, feeds)
	}
}

func extractTitleFromURL(feedURL string) string {
	parts := strings.Split(strings.TrimSuffix(feedURL, "/"), "/")
	for i, part := range parts {
		if part == "threads" && i+1 < len(parts) {
			slug := parts[i+1]
			if idx := strings.Index(slug, "."); idx > 0 {
				return strings.ReplaceAll(slug[:idx], "-", " ")
			}
			return strings.ReplaceAll(slug, "-", " ")
		}
	}
	return feedURL
}

func BuildOPML(series []models.Series) ([]byte, error) {
	body := Body{}
	for _, s := range series {
		feedURL := s.SourceURL
		switch s.ProviderName {
		case "spacebattles", "sufficientvelocity", "questionablequesting":
			feedURL = strings.TrimSuffix(s.SourceURL, "/") + "/threadmarks.rss"
		case "royalroad":
			parts := strings.Split(s.SourceURL, "/")
			for i, part := range parts {
				if part == "fiction" && i+1 < len(parts) {
					fictionID := strings.Split(parts[i+1], "-")[0]
					feedURL = "https://www.royalroad.com/syndication/" + fictionID
					break
				}
			}
		}
		body.Outlines = append(body.Outlines, Outline{
			Text:    s.Title,
			Type:    "rss",
			XMLURL:  feedURL,
			HTMLURL: s.SourceURL,
		})
	}

	opmlDoc := OPML{
		Version: "2.0",
		Body:    body,
	}

	output, err := xml.MarshalIndent(opmlDoc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), output...), nil
}
