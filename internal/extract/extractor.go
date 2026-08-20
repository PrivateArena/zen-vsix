// Package extract turns a supported store URL (or local .vsix path) into a
// downloadable Artifact via a yt-dlp-style modular registry of extractors.
//
// The interface is the sole extension point: adding a store means writing one
// new file with an Extractor implementation and appending it to the ordered
// `all` slice in registry.go. The pipeline and CLI are source-agnostic.
package extract

import (
	"context"
	"errors"
	"regexp"
)

// ErrNoExtractor is returned by ForURL when no registered extractor's pattern
// matches the raw input. The CLI lists supported URL forms on this error.
var ErrNoExtractor = errors.New("extract: no extractor matches input (supported: marketplace.visualstudio.com item/API URLs, open-vsx.org extension/API URLs, direct .vsix URLs, file:// and local .vsix paths)")

// Artifact is the normalized result of extraction. Publisher/Name/Version may
// be empty for direct/local inputs — the pipeline backfills them from the
// extension manifest once it is downloaded.
type Artifact struct {
	Publisher string // dot-separated facing name, e.g. "cdervis"
	Name      string // extension id, e.g. "vscode-pi"
	Version   string // exact version when known from the URL/API, else ""
	DirectURL string // final downloadable .vsix URL (remote artifacts)
	LocalPath string // local .vsix path (direct extractor, file:// inputs)
	Source    string // extractor name for diagnostics ("marketplace", ...)
}

// Extractor recognizes one class of store URL and resolves it to an Artifact.
// Pattern is the yt-dlp _VALID_URL equivalent: a raw input is dispatched to
// the FIRST extractor whose pattern matches (registry order, no priorities).
type Extractor interface {
	// Name is a short diagnostic identifier, e.g. "marketplace".
	Name() string
	// Pattern matches URLs this extractor owns. Must compile at init.
	Pattern() *regexp.Regexp
	// Extract performs whatever network lookups are needed and returns the
	// Artifact. It must return an error carrying reasonable context; callers
	// should wrap with the extractor name.
	Extract(ctx context.Context, rawURL string) (*Artifact, error)
}
