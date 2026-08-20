package vsix

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// zipReaderBytes lists zip entries from in-memory bytes.
func zipReaderBytes(t *testing.T, data []byte) ([]*zip.File, error) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	return zr.File, nil
}

// readEntryBytes extracts entry f's content.
func readEntryBytes(t *testing.T, f *zip.File) []byte {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// buildVSIX returns the bytes of a valid VSIX-style zip whose manifest is a
// JSON document with the given engines.vscode constraint. Extra entries are
// added verbatim. bytes.Buffer is non-seekable, so entries carry bit-3 data
// descriptors — this doubles as the streaming-source fixture for test (f).
func buildVSIX(t *testing.T, engine string, extra map[string][]byte) []byte {
	t.Helper()
	return buildVSIXDoc(t, `{"name":"pi","publisher":"cdervis","version":"1.0.0","engines":{"vscode":"`+engine+`"},"extraField":"keep-me"}`, extra)
}

func buildVSIXDoc(t *testing.T, doc string, extra map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.CreateHeader(&zip.FileHeader{Name: "extension/package.json", Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(doc)); err != nil {
		t.Fatal(err)
	}

	// Preserve insertion order for deterministic output (test b iterates).
	for _, name := range []string{"extension/out/extension.js", "extension/out/tracked.bin", "extension/README.md"} {
		data := extra[name]
		if data == nil {
			continue
		}
		w2, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w2.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeFile materializes data to a temp file and returns its path.
func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// rawBytesOf returns the raw (still-compressed) payload of entry name in the
// zip at path, proving a byte-for-byte raw copy (no recompression).
func rawBytesOf(t *testing.T, zipPath, name string) []byte {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.OpenRaw()
			if err != nil {
				t.Fatal(err)
			}
			// OpenRaw returns an io.Reader; consume directly (no Close).
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatalf("entry %s not in %s", name, zipPath)
	return nil
}

// decompressOf returns the extracted content of entry name.
func decompressOf(t *testing.T, zipPath, name string) []byte {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatalf("entry %s not in %s", name, zipPath)
	return nil
}
