// Package config loads the user-owned zen-vsix configuration file.
//
// The config file is optional: an empty path (nothing discovered) or a missing
// file yields the defaults — see Discover for how a path is auto-selected. All
// relative paths inside the file (currently output_dir) are resolved
// relative to the directory containing config.json, NOT the current working
// directory, so the tool behaves identically regardless of where it is
// invoked from.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the on-disk configuration schema. Zero values are meaningful:
// empty strings mean "not set" and fall through to detection/defaults in the
// pipeline.
type Config struct {
	// VSIXPath points at the VS Code fork root (e.g. the Antigravity
	// install dir). It is used to auto-detect the fork's engine version
	// from resources/app/product.json.
	VSIXPath string `json:"vscode_path"`
	// OutputDir is where patched artifacts and the .lock file live.
	OutputDir string `json:"output_dir"`
	// ForceEngineVersion overrides engine auto-detection entirely.
	ForceEngineVersion string `json:"force_engine_version"`
	// Overwrite allows clobbering an existing patched output file.
	Overwrite bool `json:"overwrite"`

	// dir is the absolute directory of the config file that produced this
	// Config. It anchors relative path resolution. Empty for Default().
	dir string
}

// Default returns the configuration used when no config file exists (or the
// file is empty of keys). Relative output_dir "out" resolves against the
// config dir at call time, so Default() carries no anchor and ResolveOutputDir
// falls back to the current working directory.
func Default() *Config {
	return &Config{OutputDir: "out"}
}

// Load reads the config file at path. A missing file is NOT an error: it
// returns Default(). Malformed JSON is an error — a corrupt config must never
// be silently ignored, as the user's output_dir/overwrite intent would be
// lost.
func Load(path string) (*Config, error) {
	if path == "" {
		return Default(), nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: resolve %s: %w", path, err)
	}
	cfg.dir = filepath.Dir(abs)
	return cfg, nil
}

// ResolveOutputDir returns the absolute output directory. Relative values are
// anchored at the config file's directory; when the Config came from
// Default() (no config file) the current working directory is the anchor.
func (c *Config) ResolveOutputDir() (string, error) {
	out := c.OutputDir
	if out == "" {
		out = "out"
	}
	if !filepath.IsAbs(out) {
		base := c.dir
		if base == "" {
			wd, err := os.Getwd()
			if err != nil {
				return "", fmt.Errorf("config: getwd: %w", err)
			}
			base = wd
		}
		out = filepath.Join(base, out)
	}
	return filepath.Clean(out), nil
}

// ConfigDir returns the absolute directory of the config file this Config was
// loaded from, or "" for Default(). Exported for pipeline diagnostics.
func (c *Config) ConfigDir() string { return c.dir }
