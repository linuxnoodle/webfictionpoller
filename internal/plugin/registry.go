package plugin

import (
	"fmt"
	"net/url"
	"sync"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// ---------------------------------------------------------------------------
// Capability interfaces
//
// Consumers query the registry via WithCapability to obtain providers that
// implement a given capability, then type-assert. Each capability references
// only leaf types (models.*, comics.*) to avoid import cycles.
// ---------------------------------------------------------------------------

// SeriesLister fetches series metadata for a URL (text providers).
type SeriesLister interface {
	FetchSeriesMetadata(rawURL string) (models.Series, error)
}

// Poller lists new chapters for an already-tracked series.
// Both text and comic providers may implement this (comic polling refreshes
// the chapter list rather than fetching HTML).
type Poller interface {
	PollUpdates(series models.Series) ([]models.Chapter, error)
}

// HTMLFetcher returns the sanitized chapter body for a single URL.
type HTMLFetcher interface {
	FetchChapterContent(rawURL string) (string, error)
}

// CommentFetcher pulls reader comments for a chapter URL.
type CommentFetcher interface {
	FetchComments(rawURL string) ([]models.Comment, error)
}

// Searcher runs a free-text query (comic providers, discovery flow).
type Searcher interface {
	Search(query string, page int) (*models.MangasPage, error)
}

// ComicDetailsFetcher fetches full metadata for a single comic by source ID.
type ComicDetailsFetcher interface {
	MangaDetails(sourceID string) (*models.ComicSeries, error)
}

// ChapterLister enumerates chapters for a comic by source ID.
type ChapterLister interface {
	ChapterList(sourceID string) ([]models.ComicChapter, error)
}

// PageLister enumerates image pages for a comic chapter by source ID.
type PageLister interface {
	PageList(chapterSourceID string) ([]models.ComicPage, error)
}

// CookieAuth accepts a cookie header string (CookieAuth capability).
type CookieAuth interface {
	SetCookies(cookieStr string) error
}

// FullSyncer does a complete chapter discovery (as opposed to PollUpdates
// which returns only recent changes). Used when adding a new series to get
// the full historical chapter list in one shot. XenForo providers implement
// this by parsing the threadmarks listing page; other providers fall back to
// PollUpdates which is sufficient for providers with single-page chapter lists.
type FullSyncer interface {
	FullSync(series models.Series) ([]models.Chapter, error)
}

// LoginAuth performs form-based username/password login.
type LoginAuth interface {
	Login(username, password string) error
}

// CredentialSource lets a provider pull fresh credentials on demand (relogin).
type CredentialSource interface {
	SetCredentialSource(fn func() (username, password string, ok bool))
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// Registry holds providers keyed by Meta().Name. The zero value is unusable;
// use NewRegistry.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	order     []string // preserves registration order for deterministic listing
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds p to the registry. Duplicate names panic at boot — a typo in
// a compiled-in provider's Meta().Name is a programmer error, not a runtime one.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := p.Meta()
	if m.Name == "" {
		panic(fmt.Sprintf("plugin: provider %T registered with empty Meta().Name", p))
	}
	if _, exists := r.providers[m.Name]; exists {
		panic(fmt.Sprintf("plugin: duplicate provider registration for %q", m.Name))
	}
	r.providers[m.Name] = p
	r.order = append(r.order, m.Name)
}

// Get returns the provider registered under name.
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// All returns every registered provider in registration order.
func (r *Registry) All() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.providers[name])
	}
	return out
}

// Names returns every registered provider name in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]string(nil), r.order...)
	return out
}

// ByURL returns the first registered provider whose MatchURL accepts rawURL.
// Registration order is the precedence order for overlapping matchers; in
// practice providers have disjoint domains so this is unambiguous.
func (r *Registry) ByURL(rawURL string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, name := range r.order {
		p := r.providers[name]
		if p.MatchURL(rawURL) {
			return p, true
		}
	}
	return nil, false
}

// ByKind returns providers filtered to kind.
func (r *Registry) ByKind(kind Kind) []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Provider
	for _, name := range r.order {
		p := r.providers[name]
		if p.Meta().Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

// WithCapability returns every provider that implements the capability
// pointed to by ifacePtr. ifacePtr must be a pointer to an interface type,
// e.g. (*Poller)(nil). Use of reflect is avoided so the call stays cheap.
//
// This works via two-interface type assertion; see capability_test.go for
// exact semantics.
func (r *Registry) WithCapability(ifacePtr interface{}) []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Provider
	for _, name := range r.order {
		p := r.providers[name]
		if implements(ifacePtr, p) {
			out = append(out, p)
		}
	}
	return out
}

// implements reports whether v satisfies the interface whose zero value
// (a typed nil pointer to an interface) is ifacePtr. We resolve the iface
// type once via reflect, then assert each provider.
func implements(ifacePtr, v interface{}) bool {
	return implementsReflect(ifacePtr, v)
}

// HostMatch is a small helper used by compiled-in providers' MatchURL methods.
// It reports whether rawURL's host equals host or is a subdomain of it.
// Errors parse rawURL as non-matching.
func HostMatch(rawURL, host string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	h := u.Host
	if h == host {
		return true
	}
	// allow "www." or any subdomain prefix
	return len(h) > len(host)+1 && h[len(h)-len(host)-1] == '.' && h[len(h)-len(host):] == host
}

// Default is the process-global registry built-ins self-register into.
// Tests that want isolation should construct their own NewRegistry.
var Default = NewRegistry()

// DefaultImplements reports whether p satisfies the capability described by
// ifacePtr (a typed nil pointer to an interface, e.g. (*Poller)(nil)).
// Exported so external test packages can assert capabilities.
func DefaultImplements(p Provider, ifacePtr interface{}) bool {
	return implements(ifacePtr, p)
}

// FaviconServedPath returns the URL path under /static/favicons/ that serves
// the cached favicon for name. Returns "" if name is unknown to the registry.
// The actual bytes are prefetched lazily by handlers.FaviconCache from
// the provider's Meta.FaviconURL.
func (r *Registry) FaviconServedPath(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.providers[name]; !ok {
		return ""
	}
	return "/static/favicons/" + name + ".ico"
}

// FaviconSourceURL returns the upstream favicon URL for name (Meta.FaviconURL).
// Returns "" if the provider is unknown or has no favicon configured.
func (r *Registry) FaviconSourceURL(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return ""
	}
	return p.Meta().FaviconURL
}
