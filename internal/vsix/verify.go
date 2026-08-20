package vsix

import (
	"fmt"
	"os"
	"os/exec"
)

// VerifyPatched reopens vsixPath, reads its manifest, and asserts that
// engines.vscode equals "^"+wantEngine. It is the in-process safety net run
// BEFORE the pipeline renames the patched file into place; a failure there
// must leave the temp file untouched (no torn output ever becomes final).
func VerifyPatched(vsixPath, wantEngine string) error {
	f, err := os.Open(vsixPath)
	if err != nil {
		return fmt.Errorf("vsix/verify: open %s: %w", vsixPath, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("vsix/verify: stat %s: %w", vsixPath, err)
	}

	m, err := ReadManifest(f, fi.Size())
	if err != nil {
		return fmt.Errorf("vsix/verify: read manifest of %s: %w", vsixPath, err)
	}

	want := "^" + wantEngine
	if got := m.Engines["vscode"]; got != want {
		return fmt.Errorf("vsix/verify: engines.vscode = %q, want %q", got, want)
	}
	return nil
}

// VerifyWithExternal runs the system `unzip -t` over vsixPath as an
// INDEPENDENT structural parser (the patcher's raw-copy and zip64 edge cases
// deserve a second, non-Go implementation of the spec). Returns a clear error
// if unzip is unavailable or reports problems.
func VerifyWithExternal(vsixPath string) error {
	unzipPath, err := exec.LookPath("unzip")
	if err != nil {
		return fmt.Errorf("vsix/verify: unzip not on PATH: %w (install unzip or rely on in-process VerifyPatched)", err)
	}
	out, err := exec.Command(unzipPath, "-t", vsixPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("vsix/verify: unzip -t rejected %s:\n%s\n%w", vsixPath, out, err)
	}
	return nil
}
