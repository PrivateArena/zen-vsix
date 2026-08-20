package extract

// all is the explicit, ordered extractor registry: first match wins, no
// priorities, no init()-time registration. Adding a store = append one
// implementation to this slice (and create its internal/extract/<source>.go).
// The order here is load-bearing: marketplace and open-vsx must be tried
// before direct so their API/item URLs never fall through to `.vsix`-path
// handling, and the direct extractor must come last as the catch-all.
var all = []Extractor{
	&marketplaceExtractor{},
	&openVSXExtractor{},
	&directExtractor{},
}

// ForURL dispatches rawURL to the first extractor whose pattern matches.
// Returns (nil, false) when nothing matches (callers map that to
// ErrNoExtractor from the package docs).
func ForURL(rawURL string) (Extractor, bool) {
	set := all
	if registryOverride != nil {
		set = registryOverride
	}
	for _, e := range set {
		if e.Pattern().MatchString(rawURL) {
			return e, true
		}
	}
	return nil, false
}

// registryOverride is a test seam so integration tests can point the store
// extractors at httptest servers. nil in production.
var registryOverride []Extractor

// SetRegistryForTesting swaps the extractor set used by ForURL and returns a
// restore func. Intended for pipeline integration tests only.
func SetRegistryForTesting(rs []Extractor) (restore func()) {
	old := registryOverride
	registryOverride = rs
	return func() { registryOverride = old }
}

// NewMarketplaceExtractor constructs a marketplace extractor with an explicit
// API base URL (empty → official marketplace). Exported so pipelines/tests can
// point it at a mirror or httptest server.
func NewMarketplaceExtractor(baseURL string) Extractor {
	return &marketplaceExtractor{baseURL: baseURL}
}

// NewOpenVSXExtractor constructs an open-vsx extractor with an explicit base
// URL (empty → open-vsx.org).
func NewOpenVSXExtractor(baseURL string) Extractor {
	return &openVSXExtractor{baseURL: baseURL}
}

// NewDirectExtractor constructs the direct/raw-copy extractor.
func NewDirectExtractor() Extractor {
	return &directExtractor{}
}
