package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// DefaultOpenVSX is the official open-vsx.org registry host.
const DefaultOpenVSX = "https://open-vsx.org"

// openVSXExtractor resolves open-vsx.org extension/item and api URLs.
//
//   - https://open-vsx.org/extension/{publisher}/{name}[/{version}]
//   - https://open-vsx.org/api/{publisher}/{name}[/{version}]
//
// The item/API URL simply declares which extension is wanted; the actual
// download link always comes from the registry API
// (GET /api/{publisher}/{name}[/{version}], JSON {version, files.download}).
//
// baseURL is injectable for tests; empty means the official registry.
type openVSXExtractor struct {
	baseURL string
}

var openVSXPattern = regexp.MustCompile(
	`^https://open-vsx\.org/(?:extension|api)/([^/]+)/([^/]+)(?:/([^/]+))?`,
)

func (o *openVSXExtractor) Name() string { return "openvsx" }

func (o *openVSXExtractor) Pattern() *regexp.Regexp { return openVSXPattern }

type openVSXFile struct {
	Download string `json:"download"`
}
type openVSXVersion struct {
	Version string `json:"version"`
}
type openVSXResponse struct {
	Version string      `json:"version"`
	Files   openVSXFile `json:"files"`
}

func (o *openVSXExtractor) Extract(ctx context.Context, rawURL string) (*Artifact, error) {
	m := openVSXPattern.FindStringSubmatch(rawURL)
	if len(m) < 3 {
		return nil, fmt.Errorf("extract/openvsx: input %q does not match pattern", rawURL)
	}
	pub, name := m[1], m[2]
	ver := ""
	if len(m) > 3 {
		ver = m[3]
	}

	base := o.baseURL
	if base == "" {
		base = DefaultOpenVSX
	}

	apiURL := base + "/api/" + url.PathEscape(pub) + "/" + url.PathEscape(name)
	if ver != "" {
		apiURL += "/" + url.PathEscape(ver)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("extract/openvsx: build request: %w", err)
	}
	req.Header.Set("User-Agent", "zen-vsix/0.1")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("extract/openvsx: query registry: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("extract/openvsx: registry returned %s for %s (404 = unknown extension/version)", resp.Status, apiURL)
	}

	var doc openVSXResponse
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("extract/openvsx: decode registry response: %w", err)
	}
	if doc.Files.Download == "" {
		return nil, fmt.Errorf("extract/openvsx: registry response for %s lacks files.download", apiURL)
	}

	return &Artifact{
		Publisher: pub,
		Name:      name,
		Version:   doc.Version,
		DirectURL: doc.Files.Download,
		Source:    o.Name(),
	}, nil
}
