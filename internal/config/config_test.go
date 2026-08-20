package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.OutputDir != "out" {
		t.Fatalf("Default().OutputDir = %q, want %q", c.OutputDir, "out")
	}
	if c.VSIXPath != "" || c.ForceEngineVersion != "" || c.Overwrite {
		t.Fatalf("Default() zero fields not zero: %+v", c)
	}
	if c.ConfigDir() != "" {
		t.Fatalf("Default() should have no config dir, got %q", c.ConfigDir())
	}
}

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load(missing) error: %v", err)
	}
	if c.OutputDir != "out" {
		t.Fatalf("missing file should yield Default, got %+v", c)
	}
}

func TestLoadMalformedJSONIsError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte("{not json!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("Load(malformed JSON) should error")
	}
}

func TestLoadValidFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.json")
	body := `{"vscode_path": "/opt/fork", "output_dir": "rel/out", "force_engine_version": "1.99.0", "overwrite": true}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if c.VSIXPath != "/opt/fork" || c.ForceEngineVersion != "1.99.0" || !c.Overwrite {
		t.Fatalf("fields not parsed: %+v", c)
	}
}

// The core contract: relative output_dir resolves against the CONFIG FILE's
// directory, not the process CWD.
func TestResolveOutputDirRelativeToConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "cfg", "nested")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(p, []byte(`{"output_dir": "out"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ResolveOutputDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cfgDir, "out")
	if got != want {
		t.Fatalf("ResolveOutputDir() = %q, want %q (must anchor at config dir, not CWD)", got, want)
	}
}

func TestResolveOutputDirAbsoluteAndEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"output_dir": "/abs/path"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := c.ResolveOutputDir(); got != "/abs/path" {
		t.Fatalf("absolute output_dir mangled: %q", got)
	}

	// Empty output_dir falls back to "out" anchored at config dir.
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.ResolveOutputDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "out"); got != want {
		t.Fatalf("empty output_dir: got %q want %q", got, want)
	}
}

func TestDefaultResolveOutputDirUsesCWD(t *testing.T) {
	// Default() has no config anchor; must fall back to CWD.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Default().ResolveOutputDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Clean(filepath.Join(wd, "out")); got != want {
		t.Fatalf("Default().ResolveOutputDir() = %q, want %q", got, want)
	}
}
