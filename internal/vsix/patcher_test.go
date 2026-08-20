package vsix

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNeedsPatchConstraintEvaluation(t *testing.T) {
	cases := []struct {
		constraint string
		target     string
		want       bool
		name       string
	}{
		// (a) higher constraint, lower fork → needs patch
		{"^1.109.0", "1.107.0", true, "a-unmet"},
		// (c) lower constraint satisfied
		{"^1.90.0", "1.107.0", false, "c-satisfied"},
		// (d) prerelease target normalized → satisfied, no patch
		{"^1.85.0", "1.107.0-insider", false, "d-prerelease-normalized"},
		// (e) syntax variety, satisfying targets
		{"1.90.x", "1.90.5", false, "e1-wildcard-in-range"},
		{"1.90.x", "1.92.0", true, "e1-wildcard-out-of-range"},
		{">=1.85.0 <1.100.0", "1.95.0", false, "e2-range"},
		{">=1.85.0 <1.100.0", "1.105.0", true, "e2-range-unmet"},
		{"^1.85.0 || ^1.86.0", "1.86.5", false, "e3-union"},
		// whitespace-only → no patch (no constraint)
		{"   ", "1.107.0", false, "whitespace-no-constraint"},
		// empty → no patch
		{"", "1.107.0", false, "empty-no-constraint"},
		// unparseable → no patch (non-destructive)
		{"not-a-constraint-!!", "1.107.0", false, "unparseable-no-patch"},
	}
	for _, c := range cases {
		got, err := NeedsPatch(c.constraint, c.target)
		if err != nil {
			t.Fatalf("[%s] NeedsPatch(%q,%q) error: %v", c.name, c.constraint, c.target, err)
		}
		if got != c.want {
			t.Fatalf("[%s] NeedsPatch(%q,%q) = %v, want %v", c.name, c.constraint, c.target, got, c.want)
		}
	}
}

func TestNeedsPatchInvalidTargetErrors(t *testing.T) {
	if _, err := NeedsPatch("^1.0.0", "not-semver"); err == nil {
		t.Fatal("invalid target must error")
	}
}

func TestPatchVSIXPatchesManifest(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "src.vsix", buildVSIX(t, "^1.109.0", map[string][]byte{
		"extension/out/extension.js": []byte("console.log('hi');"),
	}))
	dst := filepath.Join(dir, "dst.vsix")

	res, err := PatchVSIX(context.Background(), src, dst, PatchOptions{EngineVersion: "1.107.0"})
	if err != nil {
		t.Fatalf("PatchVSIX: %v", err)
	}
	if res.OriginalEngine != "^1.109.0" {
		t.Fatalf("OriginalEngine = %q", res.OriginalEngine)
	}
	if res.PatchedEngine != "^1.107.0" {
		t.Fatalf("PatchedEngine = %q", res.PatchedEngine)
	}
	if res.EntriesPatched != 1 || res.EntriesTotal != 2 {
		t.Fatalf("EntriesPatched=%d Total=%d", res.EntriesPatched, res.EntriesTotal)
	}
	if res.EntriesTotal != 2 {
		t.Fatalf("EntriesTotal=%d, want 2", res.EntriesTotal)
	}

	// Manifest now requires ^1.107.0 and PRESERVES unrelated fields.
	m, err := ReadManifest(bytes.NewReader(mustReadFileBytes(t, dst)), fileSize(t, dst))
	if err != nil {
		t.Fatal(err)
	}
	if m.Engines["vscode"] != "^1.107.0" {
		t.Fatalf("patched engines.vscode = %q", m.Engines["vscode"])
	}
	if got := decompressOf(t, dst, "extension/package.json"); !bytes.Contains(got, []byte(`"extraField":"keep-me"`)) {
		t.Fatalf("unrelated manifest field lost: %s", got)
	}
}

func TestPatchVSIXNonManifestEntriesByteIdentical(t *testing.T) {
	dir := t.TempDir()
	extra := map[string][]byte{
		"extension/out/extension.js": []byte("console.log('hi'); // some content"),
		"extension/out/tracked.bin":  bytes.Repeat([]byte{0x00, 0xff, 0x42}, 3000),
		"extension/README.md":        []byte("# readme\nsome text\n"),
	}
	src := writeFile(t, dir, "src.vsix", buildVSIX(t, "^1.109.0", extra))

	// Source is a raw-copy stress corpus; patch it once.
	dir2 := t.TempDir()
	patched := patchOnce(t, src, dir2)

	// Raw compressed payload bytes must be identical for every non-manifest
	// entry — the strongest proof of "no recompression".
	for name := range extra {
		srcRaw := rawBytesOf(t, src, name)
		dstRaw := rawBytesOf(t, patched, name)
		if !bytes.Equal(srcRaw, dstRaw) {
			t.Fatalf("raw bytes of %q differ (entry was recompressed/re-encoded)", name)
		}
		// And the decompressed content still matches.
		if got := decompressOf(t, patched, name); !bytes.Equal(got, extra[name]) {
			t.Fatalf("content of %q changed: %q", name, got)
		}
	}
}

// patchOnce patches a fixture to ^1.107.0 and returns the dst path.
func patchOnce(t *testing.T, src, dir string) string {
	t.Helper()
	dst := filepath.Join(dir, "patched.vsix")
	if _, err := PatchVSIX(context.Background(), src, dst, PatchOptions{EngineVersion: "1.107.0"}); err != nil {
		t.Fatal(err)
	}
	return dst
}

func mustReadFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUnzip_tAcceptsPatched(t *testing.T) {
	unzip, err := exec.LookPath("unzip")
	if err != nil {
		t.Skip("unzip not installed")
	}
	dir := t.TempDir()
	src := writeFile(t, dir, "src.vsix", buildVSIX(t, "^1.109.0", map[string][]byte{
		"extension/out/extension.js": bytes.Repeat([]byte("code();"), 500),
	}))
	dst := patchOnce(t, src, t.TempDir())

	cmd := exec.Command(unzip, "-t", dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unzip -t rejected patched archive:\n%s\n%v", out, err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}
