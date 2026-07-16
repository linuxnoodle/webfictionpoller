package models

// Comic domain types. Live here (leaf package) so plugin, comics, handlers and
// the future api package can all reference them without import cycles.

type ComicSeries struct {
	ID           int64   `json:"id"`
	SourceID     string  `json:"source_id"`
	Title        string  `json:"title"`
	Author       string  `json:"author,omitempty"`
	Artist       string  `json:"artist,omitempty"`
	Description  string  `json:"description,omitempty"`
	CoverURL     string  `json:"cover_url,omitempty"`
	SourceURL    string  `json:"source_url"`
	ProviderName string  `json:"provider_name"`
	Status       string  `json:"status"`
	Genres       string  `json:"genres,omitempty"`
	Rating       float64 `json:"rating"`
	CreatedAt    string  `json:"created_at"`
}

type ComicChapter struct {
	ID          int64  `json:"id"`
	SeriesID    int64  `json:"series_id"`
	SourceID    string  `json:"source_id"`
	Title       string `json:"title"`
	ChapterNum  string `json:"chapter_num,omitempty"`
	VolumeNum   string `json:"volume_num,omitempty"`
	SourceURL   string `json:"source_url"`
	Pages       int    `json:"pages"`
	IsRead      bool   `json:"is_read"`
	Downloaded  bool   `json:"downloaded"`
	PublishedAt string `json:"published_at"`
}

type ComicPage struct {
	Index    int    `json:"index"`
	ImageURL string `json:"image_url"`
}

type MangasPage struct {
	Mangas      []ComicSeries `json:"mangas"`
	HasNextPage bool          `json:"has_next_page"`
}
