package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"
)

// DefaultMarketplace is the official Visual Studio Marketplace host.
const DefaultMarketplace = "https://marketplace.visualstudio.com"

// ErrNotFound is returned when the marketplace query succeeds but the
// requested extension is absent (empty results/versions) or carries no
// installable .vsix asset. It exists so the pipeline can distinguish
// "store is up, extension is gone" from network/HTTP failures.
var ErrNotFound = errors.New("extract/marketplace: extension not found (empty result or no VSIXPackage asset)")

// marketplaceExtractor resolves Visual Studio Marketplace URLs.
//
//   - Item pages: https://marketplace.visualstudio.com/items?itemName={pub}.{name}
//   - Direct API links: .../_apis/public/gallery/publishers/{pub}/vsextensions/{name}/{ver}/vspackage (passthrough)
//
// Item pages are resolved through the public extensionquery API (POST). The
// latest version is taken from results[0].extensions[0].versions[0]; the
// VSIXPackage file.source is preferred for targetPlatform "universal"/empty.
//
// baseURL is injectable for tests; empty means the official marketplace.
type marketplaceExtractor struct {
	baseURL string
}

// Combined dispatch pattern: item pages OR direct API links. The API form
// captures the whole path in named group "api"; item pages capture publisher
// and name. See the unanchored item form: extra query params are tolerated.
var marketplacePattern = regexp.MustCompile(
	`^https://marketplace\.visualstudio\.com/(?:` +
		`items\?itemName=(?P<pub>[^.]+)\.(?P<name>[^&]+)|` +
		`(?P<api>_apis/public/gallery/publishers/[^/]+/vsextensions/[^/]+/[^/]+/vspackage)` +
		`)`,
)

func (m *marketplaceExtractor) Name() string { return "marketplace" }

func (m *marketplaceExtractor) Pattern() *regexp.Regexp { return marketplacePattern }

func (m *marketplaceExtractor) base() string {
	if m.baseURL != "" {
		return m.baseURL
	}
	return DefaultMarketplace
}

func (m *marketplaceExtractor) Extract(ctx context.Context, rawURL string) (*Artifact, error) {
	sub := marketplacePattern.FindStringSubmatch(rawURL)
	if sub == nil {
		return nil, fmt.Errorf("extract/marketplace: input %q does not match pattern", rawURL)
	}

	// Direct API passthrough: the URL itself is already a direct .vsix link.
	if sub[3] != "" {
		return &Artifact{DirectURL: rawURL, Source: m.Name()}, nil
	}

	pub, name := sub[1], sub[2]
	art := &Artifact{
		Publisher: pub,
		Name:      name,
		Source:    m.Name(),
	}
	if err := m.queryItem(ctx, pub+"."+name, art); err != nil {
		return nil, err
	}
	return art, nil
}

// queryItem posts an extensionquery for fullName and fills art's Version and
// DirectURL from the latest version's VSIXPackage asset.
func (m *marketplaceExtractor) queryItem(ctx context.Context, fullName string, art *Artifact) error {
	body, err := json.Marshal(map[string]any{
		"filters": []map[string]any{{
			"criteria":   []map[string]any{{"filterType": 7, "value": fullName}},
			"pageNumber": 1,
			"pageSize":   1,
			"sortBy":     0,
			"sortOrder":  0,
		}},
		"assetTypes": []string{},
		"flags":      914,
	})
	if err != nil {
		return fmt.Errorf("extract/marketplace: encode query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.base()+"/_apis/public/gallery/extensionquery", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("extract/marketplace: build query: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json;api-version=3.0-preview.1")
	req.Header.Set("User-Agent", "zen-vsix/0.1")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("extract/marketplace: query API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("extract/marketplace: API returned %s", resp.Status)
	}

	var doc struct {
		Results []struct {
			Extensions []struct {
				Publisher struct {
					PublisherName string `json:"publisherName"`
				} `json:"publisher"`
				ExtensionName string `json:"extensionName"`
				Versions      []struct {
					Version string `json:"version"`
					Files   []struct {
						AssetType      string `json:"assetType"`
						Source         string `json:"source"`
						TargetPlatform string `json:"targetPlatform"`
					} `json:"files"`
				} `json:"versions"`
			} `json:"extensions"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("extract/marketplace: decode response: %w", err)
	}

	// Defensive: empty extensions[]/versions[] must never index-panic.
	if len(doc.Results) == 0 || len(doc.Results[0].Extensions) == 0 {
		return fmt.Errorf("%w: %q returned no extensions", ErrNotFound, fullName)
	}
	ext := doc.Results[0].Extensions[0]
	if len(ext.Versions) == 0 {
		return fmt.Errorf("%w: %q has no versions", ErrNotFound, fullName)
	}

	if ext.Publisher.PublisherName != "" {
		art.Publisher = ext.Publisher.PublisherName
	}
	if ext.ExtensionName != "" {
		art.Name = ext.ExtensionName
	}

	latest := ext.Versions[0]
	art.Version = latest.Version

	src, ok := pickVSIXSource(latest.Files)
	if !ok {
		return fmt.Errorf("%w: %q %s has no VSIXPackage asset", ErrNotFound, fullName, latest.Version)
	}
	art.DirectURL = src
	return nil
}

// pickVSIXSource selects the VSIXPackage download, preferring universal/empty
// targetPlatform (skip architecture-specific bundles like darwin-x64 when a
// universal build exists); falls back to the first VSIXPackage present.
func pickVSIXSource(files []struct {
	AssetType      string `json:"assetType"`
	Source         string `json:"source"`
	TargetPlatform string `json:"targetPlatform"`
}) (string, bool) {
	var first string
	for _, f := range files {
		if f.AssetType != "Microsoft.VisualStudio.Services.VSIXPackage" || f.Source == "" {
			continue
		}
		if first == "" {
			first = f.Source
		}
		if f.TargetPlatform == "universal" || f.TargetPlatform == "" {
			return f.Source, true
		}
	}
	return first, first != ""
}
