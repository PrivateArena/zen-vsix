package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeConfig(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, DefaultFileName)
	if err := os.WriteFile(p, []byte(`{"output_dir":"out"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// isolateUserConfig points the per-user config dir at an empty temp folder so
// discovery never picks up the developer's real ~/.config/zen-vsix.
func isolateUserConfig(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", filepath.Join(t.TempDir(), "none"))
		return
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "none"))
}

func TestFindConfigInPrecedence(t *testing.T) {
	first := writeConfig(t, filepath.Join(t.TempDir(), "a"))
	second := writeConfig(t, filepath.Join(t.TempDir(), "b"))

	if got := findConfigIn([]string{filepath.Dir(first), filepath.Dir(second)}); got != first {
		t.Fatalf("findConfigIn = %q, want first hit %q", got, first)
	}
	if got := findConfigIn([]string{filepath.Dir(second), filepath.Dir(first)}); got != second {
		t.Fatalf("findConfigIn = %q, want first hit %q", got, second)
	}
}

func TestFindConfigInSkipsMisses(t *testing.T) {
	empty := t.TempDir()
	notAFile := filepath.Join(t.TempDir(), DefaultFileName)
	if err := os.MkdirAll(notAFile, 0o755); err != nil {
		t.Fatal(err) // a directory named config.json must not be loaded
	}
	hit := writeConfig(t, filepath.Join(t.TempDir(), "z"))

	dirs := []string{"", empty, filepath.Dir(notAFile), filepath.Dir(hit)}
	if got := findConfigIn(dirs); got != hit {
		t.Fatalf("findConfigIn = %q, want %q", got, hit)
	}
	if got := findConfigIn([]string{empty}); got != "" {
		t.Fatalf("findConfigIn(no config) = %q, want \"\"", got)
	}
}

func TestDiscoverEnvVarOverridesSearch(t *testing.T) {
	isolateUserConfig(t)
	cwd := writeConfig(t, t.TempDir())
	chdir(t, filepath.Dir(cwd))

	env := writeConfig(t, filepath.Join(t.TempDir(), "custom"))
	t.Setenv(EnvConfigPath, env)

	got, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != env {
		t.Fatalf("Discover = %q, want env path %q", got, env)
	}
}

func TestDiscoverEnvVarMissingFileIsError(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv(EnvConfigPath, filepath.Join(t.TempDir(), "absent.json"))
	if got, err := Discover(); err == nil {
		t.Fatalf("Discover = %q, want error for missing %s target", got, EnvConfigPath)
	}
}

func TestDiscoverFindsWorkingDirConfig(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv(EnvConfigPath, "")
	dir := writeConfig(t, t.TempDir())
	chdir(t, filepath.Dir(dir))

	got, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != dir {
		t.Fatalf("Discover = %q, want working dir config %q", got, dir)
	}
}

func TestDiscoverNothingFoundIsEmptyNoError(t *testing.T) {
	isolateUserConfig(t)
	t.Setenv(EnvConfigPath, "")
	chdir(t, t.TempDir()) // no config.json here nor in the per-user dir

	got, err := Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != "" {
		t.Fatalf("Discover = %q, want \"\" so defaults apply", got)
	}
	cfg, err := Load(got)
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if cfg.OutputDir != "out" || cfg.ConfigDir() != "" {
		t.Fatalf("Load(\"\") = %+v, want defaults", cfg)
	}
}

func TestSearchDirsStartsWithWorkingDir(t *testing.T) {
	isolateUserConfig(t)
	wd := writeConfig(t, t.TempDir())
	chdir(t, filepath.Dir(wd))

	dirs := SearchDirs()
	if len(dirs) == 0 || dirs[0] != filepath.Dir(wd) {
		t.Fatalf("SearchDirs = %v, want working dir %q first", dirs, filepath.Dir(wd))
	}
}

func TestValidatePathRejectsMissingAndDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.json")
	if err := ValidatePath(missing); err == nil {
		t.Fatal("ValidatePath(missing) = nil, want error")
	}
	dir := t.TempDir()
	if err := ValidatePath(dir); err == nil {
		t.Fatal("ValidatePath(directory) = nil, want error")
	}
	good := writeConfig(t, dir)
	if err := ValidatePath(good); err != nil {
		t.Fatalf("ValidatePath(file): %v", err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}
