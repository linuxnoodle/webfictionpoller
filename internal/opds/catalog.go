package opds

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/safefetch"
)

var brRegex = regexp.MustCompile(`(?i)<br\s*/?>`)

type Store interface {
	GetArchivedSeries() ([]models.Series, error)
	GetAllActiveSeries() ([]models.Series, error)
	GetChaptersForArchive(seriesID int64) ([]models.Chapter, error)
	GetChapterImage(chapterID int64, url string) ([]byte, string, error)
	GetSeriesByID(id int64) (*models.Series, error)
	GetSetting(key string) string
}

type Catalog struct {
	store Store
}

func NewCatalog(store Store) *Catalog {
	return &Catalog{store: store}
}

func (c *Catalog) ServeRoot(w http.ResponseWriter, r *http.Request) {
	var series []models.Series
	var err error
	if c.store.GetSetting("archive_all") == "true" {
		series, err = c.store.GetAllActiveSeries()
	} else {
		series, err = c.store.GetArchivedSeries()
	}
	if err != nil {
		logging.Error("[opds] error fetching archived series: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:opds="http://opds-spec.org/2010/catalog">
  <id>urn:uuid:webfictionpoller-catalog</id>
  <title>WebFiction Poller</title>
  <updated>` + time.Now().UTC().Format(time.RFC3339) + `</updated>
  <author><name>WebFiction Poller</name></author>
  <link rel="self" href="/opds" type="application/atom+xml;profile=opds-catalog"/>
`)

	for _, s := range series {
		updated := s.CreatedAt.UTC().Format(time.RFC3339)
		content := ""
		if s.Summary != "" {
			content = html.EscapeString(s.Summary)
		}
		coverAttr := ""
		if s.ImageURL != "" {
			coverAttr = ` opds:image="/opds/cover/` + strconv.FormatInt(s.ID, 10) + `"`
		}
		buf.WriteString(fmt.Sprintf(`  <entry>
    <id>urn:uuid:series-%d</id>
    <title>%s</title>
    <updated>%s</updated>
    <author><name>%s</name></author>
    <content type="text">%s</content>
    <link rel="http://opds-spec.org/acquisition" href="/opds/epub/%d" type="application/epub+zip" title="Download EPUB"%s/>
  </entry>
`, s.ID, html.EscapeString(s.Title), updated, html.EscapeString(s.Author), content, s.ID, coverAttr))
	}

	buf.WriteString(`</feed>`)

	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Write(buf.Bytes())
}

func (c *Catalog) ServeCover(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/opds/cover/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	s, err := c.store.GetSeriesByID(id)
	if err != nil || s == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if s.ImageURL == "" {
		http.Error(w, "no cover", http.StatusNotFound)
		return
	}

	resp, err := safefetch.Get(s.ImageURL)
	if err != nil || resp.StatusCode != 200 {
		http.Error(w, "fetch failed", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		http.Error(w, "invalid content type", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	io.Copy(w, io.LimitReader(resp.Body, 5<<20))
}

func (c *Catalog) ServeEPUB(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/opds/epub/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	series, err := c.store.GetSeriesByID(id)
	if err != nil || series == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	chapters, err := c.store.GetChaptersForArchive(id)
	if err != nil {
		logging.Error("[opds] error fetching chapters for series %d: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if len(chapters) == 0 {
		http.Error(w, "no content", http.StatusNoContent)
		return
	}

	epubData, err := c.generateEPUB(series, chapters)
	if err != nil {
		logging.Error("[opds] error generating epub for series %d: %v", id, err)
		http.Error(w, "generation failed", http.StatusInternalServerError)
		return
	}

	filename := sanitizeFilename(series.Title) + ".epub"
	w.Header().Set("Content-Type", "application/epub+zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(epubData)))
	w.Write(epubData)
}

func (c *Catalog) ServeImage(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/opds/images/"), "/", 2)
	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	chapterID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid chapter id", http.StatusBadRequest)
		return
	}
	imgURL := parts[1]

	data, contentType, err := c.store.GetChapterImage(chapterID, imgURL)
	if err != nil {
		http.Error(w, "error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(data)
}

func (c *Catalog) generateEPUB(series *models.Series, chapters []models.Chapter) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	mimetypeFile, _ := zw.CreateRaw(&zip.FileHeader{
		Name:   "mimetype",
		Method: zip.Store,
	})
	mimetypeFile.Write([]byte("application/epub+zip"))

	containerFile, _ := zw.Create("META-INF/container.xml")
	containerFile.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`))

	writeFile(zw, "OEBPS/toc.ncx", c.generateNCX(series, chapters))

	writeFile(zw, "OEBPS/stylesheet.css", `body{font-family:serif;margin:1em;line-height:1.6}
h1{font-size:1.4em;margin-bottom:0.3em}
h2{font-size:1.2em;margin-bottom:0.2em}
img{max-width:100%;height:auto}
p{margin:0.5em 0}`)

	writeFile(zw, "OEBPS/title.xhtml", generateTitlePage(series))

	coverFile := ""
	if series.ImageURL != "" {
		resp, err := safefetch.Get(series.ImageURL)
		if err == nil && resp.StatusCode == 200 {
			coverData, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
			resp.Body.Close()
			if len(coverData) > 0 {
				ext := ".jpg"
				ct := resp.Header.Get("Content-Type")
				switch ct {
				case "image/png":
					ext = ".png"
				case "image/gif":
					ext = ".gif"
				case "image/webp":
					ext = ".webp"
				}
				coverFile = "OEBPS/cover" + ext
				writeFile(zw, coverFile, string(coverData))
			}
		}
	}

	var manifestItems []string
	var spineItems []string

	manifestItems = append(manifestItems, `<item id="title" href="title.xhtml" media-type="application/xhtml+xml"/>`)
	manifestItems = append(manifestItems, `<item id="css" href="stylesheet.css" media-type="text/css"/>`)
	spineItems = append(spineItems, `<itemref idref="title"/>`)

	if coverFile != "" {
		coverID := "cover-image"
		ct := "image/jpeg"
		if strings.HasSuffix(coverFile, ".png") {
			ct = "image/png"
		}
		manifestItems = append(manifestItems, fmt.Sprintf(`<item id="%s" href="%s" media-type="%s" properties="cover-image"/>`, coverID, strings.TrimPrefix(coverFile, "OEBPS/"), ct))
	}

	for i, ch := range chapters {
		filename := fmt.Sprintf("chapter%04d.xhtml", i)
		chapterHTML := generateChapterHTML(ch)
		writeFile(zw, "OEBPS/"+filename, chapterHTML)

		itemID := fmt.Sprintf("ch%d", i)
		manifestItems = append(manifestItems, fmt.Sprintf(`<item id="%s" href="%s" media-type="application/xhtml+xml"/>`, itemID, filename))
		spineItems = append(spineItems, fmt.Sprintf(`<itemref idref="%s"/>`, itemID))
	}

	manifest := strings.Join(manifestItems, "\n    ")
	spine := strings.Join(spineItems, "\n    ")

	uid := fmt.Sprintf("urn:uuid:wfp-series-%d", series.ID)
	metaCover := ""
	if coverFile != "" {
		metaCover = `<meta name="cover" content="cover-image"/>`
	}

	writeFile(zw, "OEBPS/content.opf", fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="uid">%s</dc:identifier>
    <dc:title>%s</dc:title>
    <dc:creator>%s</dc:creator>
    <dc:language>en</dc:language>
    <dc:date>%s</dc:date>
    <meta property="dcterms:modified">%s</meta>
    %s
  </metadata>
  <manifest>
    <item id="nav" href="toc.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    %s
  </manifest>
  <spine>
    %s
  </spine>
</package>`, uid, html.EscapeString(series.Title), html.EscapeString(series.Author),
		series.CreatedAt.Format("2006-01-02"),
		time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		metaCover,
		manifest, spine))

	navXHTML := generateNavXHTML(series, chapters)
	writeFile(zw, "OEBPS/toc.xhtml", navXHTML)

	zw.Close()
	return buf.Bytes(), nil
}

func (c *Catalog) generateNCX(series *models.Series, chapters []models.Chapter) string {
	var navPoints []string
	for i, ch := range chapters {
		navPoints = append(navPoints, fmt.Sprintf(`    <navPoint id="navpoint-%d" playOrder="%d">
      <navLabel><text>%s</text></navLabel>
      <content src="chapter%04d.xhtml"/>
    </navPoint>`, i, i+2, html.EscapeString(ch.Title), i))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head>
    <meta name="dtb:uid" content="urn:uuid:wfp-series-%d"/>
    <meta name="dtb:depth" content="1"/>
    <meta name="dtb:totalPageCount" content="0"/>
    <meta name="dtb:maxPageNumber" content="0"/>
  </head>
  <docTitle><text>%s</text></docTitle>
  <navMap>
    <navPoint id="navpoint-title" playOrder="1">
      <navLabel><text>Title Page</text></navLabel>
      <content src="title.xhtml"/>
    </navPoint>
%s
  </navMap>
</ncx>`, series.ID, html.EscapeString(series.Title), strings.Join(navPoints, "\n"))
}

func generateTitlePage(series *models.Series) string {
	coverHTML := ""
	if series.ImageURL != "" {
		coverHTML = fmt.Sprintf(`<div style="text-align:center;margin:1em 0"><img src="cover.jpg" alt="cover" style="max-height:400px"/></div>`)
	}
	summaryHTML := ""
	if series.Summary != "" {
		summaryHTML = fmt.Sprintf(`<p style="font-style:italic;color:#555">%s</p>`, html.EscapeString(series.Summary))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>%s</title><link rel="stylesheet" type="text/css" href="stylesheet.css"/></head>
<body>
%s
<h1>%s</h1>
<p>by %s</p>
%s
<p style="font-size:0.8em;color:#888">Source: %s</p>
</body>
</html>`, html.EscapeString(series.Title), coverHTML, html.EscapeString(series.Title), html.EscapeString(series.Author), summaryHTML, html.EscapeString(series.SourceURL))
}

func generateChapterHTML(ch models.Chapter) string {
	content := ch.ContentHTML
	if content == "" {
		content = "<p>Content not available.</p>"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml">
<head><title>%s</title><link rel="stylesheet" type="text/css" href="stylesheet.css"/></head>
<body>
<h2>%s</h2>
%s
</body>
</html>`, html.EscapeString(ch.Title), html.EscapeString(ch.Title), content)
}

func generateNavXHTML(series *models.Series, chapters []models.Chapter) string {
	var items []string
	for i, ch := range chapters {
		items = append(items, fmt.Sprintf(`    <li><a href="chapter%04d.xhtml">%s</a></li>`, i, html.EscapeString(ch.Title)))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>Table of Contents</title><link rel="stylesheet" type="text/css" href="stylesheet.css"/></head>
<body>
<nav epub:type="toc">
  <h1>Table of Contents</h1>
  <ol>
    <li><a href="title.xhtml">Title Page</a></li>
%s
  </ol>
</nav>
</body>
</html>`, strings.Join(items, "\n"))
}

func writeFile(zw *zip.Writer, name string, content string) {
	f, err := zw.Create(name)
	if err != nil {
		return
	}
	f.Write([]byte(content))
}

func sanitizeFilename(name string) string {
	r := strings.NewReplacer(
		" ", "_",
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	return r.Replace(path.Base(name))
}
