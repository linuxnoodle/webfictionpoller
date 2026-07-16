// Package plugin provides the provider registry and capability interfaces
// that consumers (workers, handlers, API) use to discover and invoke providers.
//
// A Provider is the base unit. It exposes Metadata (name, kind, rate, auth modes)
// and a URL matcher. Specialized behaviour is expressed via capability interfaces
// (Poller, HTMLFetcher, Searcher, etc.) — consumers query the registry for
// providers implementing a given capability rather than branching on type.
//
// Built-in providers self-register via init() in their own package; cmd/main.go
// blank-imports them. Declarative TOML providers register at startup from
// data/providers/*.toml.
package plugin

// Kind classifies a provider's content shape.
type Kind string

const (
	KindText  Kind = "text"
	KindComic Kind = "comic"
)

// RateSpec is the provider's default politeness budget. Per-provider overrides
// may live in the settings table.
type RateSpec struct {
	// RequestsPerSecond is the sustained request rate ceiling.
	RequestsPerSecond float64
	// Burst allows short bursts above the sustained rate (token-bucket depth).
	Burst int
	// Concurrency caps in-flight requests for this provider.
	Concurrency int
}

// AuthMode labels how a provider authenticates.
type AuthMode string

const (
	AuthNone         AuthMode = "none"
	AuthCookies      AuthMode = "cookies"
	AuthLogin        AuthMode = "login"
	AuthFlareSolverr AuthMode = "flaresolverr"
)

// Meta describes a provider. It is the single source of provider metadata;
// models.ProviderNames / models.ProviderFavicon etc. are removed once all
// consumers migrate.
type Meta struct {
	// Name is the stable lowercase identifier persisted in series.provider_name
	// and provider_configs.provider_name. Never rename — it is the data key.
	Name string
	// DisplayName is the human label shown in UI.
	DisplayName string
	// Kind is text or comic.
	Kind Kind
	// Homepage is the site base URL; used for favicon resolution + attribution.
	Homepage string
	// FaviconURL is the absolute favicon URL (prefetched at runtime, not startup).
	FaviconURL string
	// AuthModes lists the auth mechanisms this provider supports.
	AuthModes []AuthMode
	// Rate is the default politeness budget.
	Rate RateSpec
	// PollIntervalDefault is the suggested polling interval for this provider.
	// Zero means "use the global POLL_INTERVAL".
	PollIntervalDefault string
}

// Provider is the base capability every plugin implements.
// Specialized behaviour is declared via the capability interfaces below; the
// base contract only carries identity + URL routing so registries can hold
// providers of any capability shape.
type Provider interface {
	Meta() Meta
	// MatchURL reports whether rawURL belongs to this provider.
	// Multiple providers may match; registry.ByURL returns the first registered.
	MatchURL(rawURL string) bool
}
