package vsix

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyPatchedGood(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "src.vsix", buildVSIX(t, "^1.109.0", nil))
	patched := patchOnce(t, src, t.TempDir())
	if err := VerifyPatched(patched, "1.107.0"); err != nil {
		t.Fatalf("VerifyPatched on valid output: %v", err)
	}
}

func TestVerifyPatchedTampered(t *testing.T) {
	dir := t.TempDir()
	// Build a vsix already at ^1.107.0 and verify against a DIFFERENT want —
	// the mismatch must be caught.
	src := writeFile(t, dir, "src.vsix", buildVSIX(t, "^1.107.0", nil))
	if err := VerifyPatched(src, "1.200.0"); err == nil {
		t.Fatal("VerifyPatched must fail on mismatched engine")
	}
}

func TestVerifyPatchedMissingFile(t *testing.T) {
	if err := VerifyPatched(filepath.Join(t.TempDir(), "nope.vsix"), "1.107.0"); err == nil {
		t.Fatal("VerifyPatched must fail on missing file")
	}
}

func TestVerifyWithExternalOnRealFixture(t *testing.T) {
	if _, err := exec.LookPath("unzip"); err != nil {
		t.Skip("unzip not installed")
	}
	dir := t.TempDir()
	src := writeFile(t, dir, "src.vsix", buildVSIX(t, "^1.109.0", map[string][]byte{
		"extension/out/ext.js": []byte("// hello"),
	}))
	patched := patchOnce(t, src, t.TempDir())
	if err := VerifyWithExternal(patched); err != nil {
		t.Fatalf("VerifyWithExternal: %v", err)
	}
}

func TestVerifyWithExternalUnzipMissing(t *testing.T) {
	// Simulate absence by pointing PATH at an empty dir.
	empty := t.TempDir()
	old := os.Getenv("PATH")
	os.Setenv("PATH", empty)
	defer os.Setenv("PATH", old)
	if err := VerifyWithExternal("whatever.vsix"); err == nil {
		t.Fatal("VerifyWithExternal with unzip absent must error")
	} else if !strings.Contains(err.Error(), "unzip") {
		t.Fatalf("error should mention unzip: %v", err)
	}
}
