package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Config discovery: when the user does not pass -c, zen-vsix still picks up a
// config.json it can find on disk, so relative output_dir / vscode_path keep
// working from the directory the tool lives in.
const (
	// DefaultFileName is the conventional config file name probed for in each
	// search directory.
	DefaultFileName = "config.json"

	// EnvConfigPath, when non-empty, replaces directory discovery: its value is
	// used as the config file path, and an unusable file is an error (an
	// explicit pointer must never be silently ignored).
	EnvConfigPath = "ZEN_VSIX_CONFIG"
)

// Discover returns the config file path to load when -c was not supplied.
// Precedence: $ZEN_VSIX_CONFIG, then the first existing config.json found in
// SearchDirs(). It returns "" (no error) when nothing is found, which makes
// Load fall through to Default().
func Discover() (string, error) {
	if env := os.Getenv(EnvConfigPath); env != "" {
		if err := ValidatePath(env); err != nil {
			return "", fmt.Errorf("%s: %w", EnvConfigPath, err)
		}
		return env, nil
	}
	dirs := SearchDirs()
	return findConfigIn(dirs), nil
}

// SearchDirs returns the directories probed for config.json, in precedence
// order: the working directory (where the user invoked the tool), the directory
// holding the executable (so a self-contained folder works from anywhere), then
// the per-user config directory (<userconfig>/zen-vsix). Directories that
// cannot be determined are skipped.
func SearchDirs() []string {
	var dirs []string
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	// A failure to locate the executable is not fatal: the remaining
	// locations are still searched.
	if exe, err := executableDir(); err == nil {
		dirs = append(dirs, exe)
	}
	if user, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(user, "zen-vsix"))
	}
	return dirs
}

// ValidatePath reports whether path can be used as a config file.
func ValidatePath(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("config: %s: %w", path, err)
	}
	if st.IsDir() {
		return fmt.Errorf("config: %s is a directory, expected a config file", path)
	}
	return nil
}

// findConfigIn returns config.json from the first directory in dirs that has
// one, or "" when none do.
func findConfigIn(dirs []string) string {
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, DefaultFileName)
		if ValidatePath(candidate) == nil {
			return candidate
		}
	}
	return ""
}

// executableDir is the absolute directory containing the running binary, with
// symlinks resolved so a ~/bin/zen-vsix symlink to a project build finds the
// config next to the real binary.
func executableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("config: locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}
