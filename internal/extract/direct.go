package extract

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// directExtractor handles three input classes with one explicit pattern:
//   - https?://...vsix URLs            → Artifact{DirectURL}
//   - file:///path/to/x.vsix           → Artifact{LocalPath}
//   - bare fs path ending in .vsix     → Artifact{LocalPath} (existence-verified)
//
// Version/Name/Publisher are intentionally empty; the pipeline backfills them
// from the extension manifest after download. Local files must exist — a
// matched-but-missing bare path is an error, not a silent remote fallback.
type directExtractor struct{}

var directPattern = regexp.MustCompile(
	`^(?P<body>https?://[^?#]+|file://[^?#]+|[^?#]+)\.vsix(?P<suffix>[?#].*)?$`,
)

func (*directExtractor) Name() string { return "direct" }

func (*directExtractor) Pattern() *regexp.Regexp { return directPattern }

func (d *directExtractor) Extract(_ context.Context, rawURL string) (*Artifact, error) {
	body := directPattern.FindStringSubmatch(rawURL)

	if strings.HasPrefix(body[1], "http://") || strings.HasPrefix(body[1], "https://") {
		return &Artifact{DirectURL: rawURL, Source: d.Name()}, nil
	}

	if strings.HasPrefix(body[1], "file://") {
		path, err := filePathFromFileURL(rawURL)
		if err != nil {
			return nil, fmt.Errorf("extract/direct: %w", err)
		}
		return &Artifact{LocalPath: path, Source: d.Name()}, nil
	}

	// Bare path: must actually exist, otherwise it's not a usable local file.
	if _, err := os.Stat(rawURL); err != nil {
		return nil, fmt.Errorf("extract/direct: local vsix %q: %w", rawURL, err)
	}
	return &Artifact{LocalPath: rawURL, Source: d.Name()}, nil
}

// filePathFromFileURL decodes a file:// URL's path, handling percent-encoded
// characters (spaces etc.). The directPattern guarantees the .vsix suffix.
func filePathFromFileURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse file URL %q: %w", raw, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("not a file URL: %q", raw)
	}
	p, err := url.PathUnescape(u.Path)
	if err != nil {
		return "", fmt.Errorf("unescape file URL %q: %w", raw, err)
	}
	if !strings.HasSuffix(filepath.Base(p), ".vsix") {
		return "", fmt.Errorf("file URL does not name a .vsix: %q", raw)
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("local vsix %q: %w", p, err)
	}
	return p, nil
}
