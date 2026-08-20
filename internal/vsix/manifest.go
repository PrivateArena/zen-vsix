// Package vsix reads and rewrites VSIX extension archives. VSIX is a zip with
// the extension manifest at the well-known path extension/package.json.
package vsix

import (
	"archive/zip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// Manifest is the subset of extension/package.json the pipeline cares about.
type Manifest struct {
	Name      string            `json:"name"`
	Publisher string            `json:"publisher"`
	Version   string            `json:"version"`
	Engines   map[string]string `json:"engines"`
}

// manifestEntry is the canonical location of the extension manifest inside a
// VSIX zip. VSIX always uses forward-slash paths.
const manifestEntry = "extension/package.json"

// maxManifestSize caps how large a package.json we will read. VSIX manifests
// are small (KB); anything near 10 MB is malformed or hostile.
const maxManifestSize = 10 << 20 // 10 MiB

// ReadManifest locates extension/package.json inside a zip (case-insensitive
// fallback on the entry name) and decodes it with a 10 MB cap.
//
// Errors:
//   - missing entry → error listing near-miss entries (found, just mis-cased
//     or in the wrong directory);
//   - multi-disk/split zip → a clear "unsupported" error, not a raw parse
//     error (detected from the End of Central Directory record, since Go's
//     archive/zip surfaces multi-disk as a generic "not a valid zip file");
//   - manifest larger than the cap → rejected.
func ReadManifest(r io.ReaderAt, size int64) (*Manifest, error) {
	if size <= 0 {
		return nil, fmt.Errorf("vsix: empty or invalid archive (%d bytes)", size)
	}
	if multiDisk, err := isMultiDisk(r, size); err != nil {
		return nil, err
	} else if multiDisk {
		return nil, fmt.Errorf("vsix: unsupported archive: split/multi-disk zip archives are not supported")
	}

	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("vsix: open zip: %w", err)
	}

	f := findManifestEntry(zr.File)
	if f == nil {
		return nil, fmt.Errorf("vsix: manifest %q not found in archive; near misses: %s",
			manifestEntry, nearMisses(zr.File))
	}

	if f.UncompressedSize64 > maxManifestSize {
		return nil, fmt.Errorf("vsix: %s is %d bytes, exceeds %d cap", manifestEntry, f.UncompressedSize64, maxManifestSize)
	}

	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("vsix: open %s: %w", manifestEntry, err)
	}
	defer rc.Close()

	var m Manifest
	if err := json.NewDecoder(io.LimitReader(rc, maxManifestSize+1)).Decode(&m); err != nil {
		return nil, fmt.Errorf("vsix: decode %s: %w", manifestEntry, err)
	}
	return &m, nil
}

// isMultiDisk inspects the End of Central Directory record. EOCD layout:
//
//		0  signature (0x06054b50)
//		4  diskNumber            (2 bytes)
//		6  directoryDiskNumber   (2 bytes)
//	  ...
//		16 comment length        (2 bytes)
//		18 comment
//
// A non-zero disk number means the archive is split across files — clear error
// beats Go's generic ErrFormat.
func isMultiDisk(r io.ReaderAt, size int64) (bool, error) {
	const eocdLen = 22
	// Comment can be up to 65535 bytes; EOCD record is 22; search the tail.
	maxBack := int64(eocdLen + 65535)
	start := size - maxBack
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := r.ReadAt(buf, start); err != nil && err != io.EOF {
		return false, fmt.Errorf("vsix: read archive tail: %w", err)
	}
	for i := len(buf) - eocdLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) != 0x06054b50 {
			continue
		}
		diskNumber := binary.LittleEndian.Uint16(buf[i+4:])
		dirDiskNumber := binary.LittleEndian.Uint16(buf[i+6:])
		return diskNumber != 0 || dirDiskNumber != 0, nil
	}
	// No EOCD found — let zip.NewReader produce its own diagnosis.
	return false, nil
}

// findManifestEntry returns the exact entry, else a case-insensitive match.
func findManifestEntry(files []*zip.File) *zip.File {
	for _, f := range files {
		if normEntry(f.Name) == manifestEntry {
			return f
		}
	}
	for _, f := range files {
		if strings.EqualFold(normEntry(f.Name), manifestEntry) {
			return f
		}
	}
	return nil
}

// normEntry normalizes a zip entry name for comparison: forward slashes and
// no leading "./" (some packers historically emit backslashes or a prefix).
func normEntry(name string) string {
	n := strings.ReplaceAll(name, "\\", "/")
	n = strings.TrimPrefix(n, "./")
	return n
}

// nearMisses returns a compact, sorted list of entries that look like a
// misplaced or mis-cased manifest, to make the missing-entry error actionable.
func nearMisses(files []*zip.File) string {
	var hits []string
	for _, f := range files {
		if strings.EqualFold(path.Base(normEntry(f.Name)), "package.json") {
			hits = append(hits, f.Name)
		}
	}
	if len(hits) == 0 {
		return "none"
	}
	sort.Strings(hits)
	extra := ""
	if len(hits) > 5 {
		extra = fmt.Sprintf(" (+%d more)", len(hits)-5)
		hits = hits[:5]
	}
	return strings.Join(hits, ", ") + extra
}
