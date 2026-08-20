// Package detect locates and parses the engine version of a VS Code-style
// fork (Antigravity, Codex, Claude Desktop, ...) so that patch decisions can
// be made against the fork's real API level instead of a hand-picked constant.
package detect

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Masterminds/semver/v3"
)

// ErrNoProduct is returned when no candidate version file exists anywhere
// under the probed root. It is deliberately distinct from parse errors: it
// means "look elsewhere / configure vscode_path", not "your file is corrupt".
var ErrNoProduct = errors.New("detect: no product.json or package.json found under root (check vscode_path)")

// probePaths are checked in order; the first file that exists wins. The
// Antigravity/VS Code fork layout (resources/app/product.json) is probed
// first because it is the canonical location of the true engine version.
var probePaths = []string{
	"resources/app/product.json",
	"resources/app/package.json",
	"product.json",
	"package.json",
}

// DetectEngineVersion probes root for a product/package.json and returns the
// top-level "version" field, validated as semver. Pre-release suffixes are
// preserved ("1.107.0-insider" stays as-is); the pipeline strips them later
// when it needs an API-level comparison.
//
// Error contract:
//   - ErrNoProduct when no candidate file exists at any probe path;
//   - a wrapped error (NOT ErrNoProduct) when a file was found but could not
//     be read, was not valid JSON, or carried an empty/garbage "version".
func DetectEngineVersion(root string) (string, error) {
	for _, rel := range probePaths {
		p := filepath.Join(root, rel)
		fi, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("detect: stat %s: %w", p, err)
		}
		if fi.IsDir() {
			continue
		}
		return fileVersion(p)
	}
	return "", ErrNoProduct
}

func fileVersion(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("detect: read %s: %w", path, err)
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("detect: parse %s: %w", path, err)
	}
	if doc.Version == "" {
		return "", fmt.Errorf("detect: %s: version field is empty", path)
	}
	if _, err := semver.NewVersion(doc.Version); err != nil {
		return "", fmt.Errorf("detect: %s: version %q is not valid semver: %w", path, doc.Version, err)
	}
	return doc.Version, nil
}
