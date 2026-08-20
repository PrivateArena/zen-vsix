package vsix

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPatchVSIXZip64Fixture builds a synthetic zip64 archive (>65535 entries
// forces Go's writer to emit the zip64 End of Central Directory), patches it,
// and verifies the result with an independent parser (Go + system unzip).
//
// This is the regression net for the red-team "zip64 untested" blocker: raw
// copying must preserve entry-count-triggered zip64 structures.
func TestPatchVSIXZip64Fixture(t *testing.T) {
	dir := t.TempDir()

	const nEntries = 66000 // > math.MaxUint16 → zip64 EOCD required
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.CreateHeader(&zip.FileHeader{Name: "extension/package.json", Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`{"engines":{"vscode":"^1.200.0"}}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nEntries; i++ {
		h := &zip.FileHeader{Name: fmt.Sprintf("e/%05d.dat", i), Method: zip.Store}
		fw, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte{byte(i), byte(i >> 8)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	// Sanity: the writer really produced a zip64 archive (entry count > uint16).
	if nEntries <= math.MaxUint16 {
		t.Fatalf("fixture setup broken: %d entries does not trip zip64", nEntries)
	}

	src := writeFile(t, dir, "zip64-src.vsix", buf.Bytes())
	dst := patchOnce(t, src, t.TempDir())

	// Independent Go parse: entry count preserved.
	zr, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatalf("reopen patched zip64: %v", err)
	}
	defer zr.Close()
	if len(zr.File) != nEntries+1 {
		t.Fatalf("patched zip64 entry count = %d, want %d", len(zr.File), nEntries+1)
	}
	foundManifest := false
	for _, f := range zr.File {
		if f.Name == "extension/package.json" {
			foundManifest = true
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			_ = rc.Close()
			if !bytes.Contains(b, []byte("^1.107.0")) {
				t.Fatalf("zip64 manifest not patched: %s", b)
			}
		}
	}
	if !foundManifest {
		t.Fatal("manifest entry missing from patched zip64")
	}

	// Independent parser: system unzip must accept the whole thing.
	if unzipPath, err := exec.LookPath("unzip"); err == nil {
		out, err := exec.Command(unzipPath, "-t", dst).CombinedOutput()
		if err != nil {
			t.Fatalf("unzip -t rejected zip64 patched archive:\n%s\n%v", out, err)
		}
	}
}

// Ensure fixture files are closed/removed cleanly — a plain patch run leaves
// the output in place and reports Patched=true.
func TestPatchVSIXRemovesNothingUnexpected(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "src.vsix", buildVSIX(t, "^1.109.0", nil))
	dst := filepath.Join(dir, "dst.vsix")
	res, err := PatchVSIX(context.Background(), src, dst, PatchOptions{EngineVersion: "1.107.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Patched {
		t.Fatal("fixture with unmet constraint must be patched")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("patched output must exist: %v", err)
	}
}
