// Package vsix reads and rewrites VSIX extension archives. VSIX is a zip with
// the extension manifest at the well-known path extension/package.json.
package vsix

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// PatchOptions controls a single patch run.
type PatchOptions struct {
	// EngineVersion is the fork's semver engine version (prerelease OK; it is
	// normalized internally). The patched manifest will require "^"+EngineVersion.
	EngineVersion string
}

// PatchResult reports what a patch run did.
type PatchResult struct {
	OriginalEngine string // engines.vscode value read from source manifest ("" if none)
	PatchedEngine  string // "^"+EngineVersion after patching
	Patched        bool   // always true unless the manifest was never found
	EntriesTotal   int
	EntriesPatched int   // 1 (the manifest) or 0
	BytesWritten   int64 // bytes of (re)-written compressed payloads
}

// NeedsPatch decides whether targetVersion satisfies constraint, i.e. whether
// a rewrite is required. Non-destructive by default: any case we cannot
// confidently evaluate answers "no patch".
//
//   - empty/whitespace-only constraint → (false, nil): no requirement to compare.
//   - unparseable constraint → (false, nil): caller warns and skips; never guess.
//   - prerelease on targetVersion is STRIPPED before Check. Masterminds' Check
//     excludes prereleases by default, so "1.107.0-insider" would otherwise be
//     judged unsatisfiable against "^1.85.0" and wrongly trigger a patch.
func NeedsPatch(constraint, targetVersion string) (bool, error) {
	c := strings.TrimSpace(constraint)
	if c == "" {
		return false, nil
	}
	cons, err := semver.NewConstraint(c)
	if err != nil {
		// Unparseable constraint: keep the file, let the caller warn.
		return false, nil
	}
	v, err := semver.NewVersion(targetVersion)
	if err != nil {
		return false, fmt.Errorf("vsix: target engine version %q is not valid semver: %w", targetVersion, err)
	}
	// SetPrerelease/SetMetadata are value-receiver copies; operate on a copy
	// so we keep the literal ".SetPrerelease("".SetMetadata(""" sequence.
	vc := *v
	if vc, err = vc.SetPrerelease(""); err != nil {
		return false, fmt.Errorf("vsix: normalize target version: %w", err)
	}
	if vc, err = vc.SetMetadata(""); err != nil {
		return false, fmt.Errorf("vsix: normalize target version: %w", err)
	}
	return !cons.Check(&vc), nil
}

// ConstraintParses reports whether constraint can be compiled as a semver
// constraint. It exists so the pipeline can tell an unparseable constraint
// (keep original, warn) apart from a merely-unmet one (patch) for messaging,
// without exposing Masterminds' API to callers.
func ConstraintParses(constraint string) bool {
	_, err := semver.NewConstraint(constraint)
	return err == nil
}

// PatchVSIX rewrites src into dst in a single streaming pass: every entry is
// byte-copied raw except extension/package.json, which is decompressed, has
// its engines.vscode set to "^"+EngineVersion, and is recompressed (Deflate).
// The manifest is edited as a generic JSON document so all other package.json
// fields are preserved (never round-tripped through a struct subset).
//
// Raw-copy semantics are verified against archive/zip: CreateRaw's argument is
// value-copied (CreateRaw mutates the header's 32-bit size fields), OpenRaw
// exposes the raw compressed stream, and close() emits a trailing data
// descriptor exactly when the copied header carries bit 3, so both streaming
// (descriptor) and normal source entries yield self-consistent output.
//
// Non-goal: `Extra` fields on the recompressed manifest entry are dropped.
func PatchVSIX(ctx context.Context, src, dst string, opts PatchOptions) (*PatchResult, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return nil, fmt.Errorf("vsix: open source %s: %w", src, err)
	}
	defer zr.Close()

	out, err := os.Create(dst)
	if err != nil {
		return nil, fmt.Errorf("vsix: create %s: %w", dst, err)
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	res := &PatchResult{EntriesTotal: len(zr.File), Patched: true}

	for i, srcFile := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("vsix: cancelled at entry %d: %w", i, err)
		}
		if normEntry(srcFile.Name) == manifestEntry {
			n, err := patchManifestEntry(zw, srcFile, opts, res)
			if err != nil {
				return nil, err
			}
			res.BytesWritten += n
			res.EntriesPatched++
			continue
		}

		// Raw byte-copy: value-copy the header (CreateRaw mutates it) and
		// stream the source's compressed bytes untouched.
		fh := srcFile.FileHeader
		w, err := zw.CreateRaw(&fh)
		if err != nil {
			return nil, fmt.Errorf("vsix: raw-copy start %s: %w", srcFile.Name, err)
		}
		rc, err := srcFile.OpenRaw()
		if err != nil {
			return nil, fmt.Errorf("vsix: raw-copy open %s: %w", srcFile.Name, err)
		}
		n, err := io.Copy(w, rc)
		if err != nil {
			return nil, fmt.Errorf("vsix: raw-copy %s: %w", srcFile.Name, err)
		}
		res.BytesWritten += n
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("vsix: finalize %s: %w", dst, err)
	}
	if err := out.Sync(); err != nil {
		return nil, fmt.Errorf("vsix: sync %s: %w", dst, err)
	}
	return res, nil
}

// patchManifestEntry decompresses the manifest entry, rewrites engines.vscode,
// and recompresses it as Deflate, preserving all unrelated fields.
func patchManifestEntry(zw *zip.Writer, srcFile *zip.File, opts PatchOptions, res *PatchResult) (int64, error) {
	rc, err := srcFile.Open()
	if err != nil {
		return 0, fmt.Errorf("vsix: open manifest entry: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxManifestSize+1))
	if err != nil {
		return 0, fmt.Errorf("vsix: read manifest entry: %w", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, fmt.Errorf("vsix: decode manifest entry: %w", err)
	}

	engines, ok := doc["engines"].(map[string]any)
	if !ok {
		engines = map[string]any{}
		doc["engines"] = engines
	}
	if s, ok := engines["vscode"].(string); ok {
		res.OriginalEngine = s
	}
	patchedEngine := "^" + opts.EngineVersion
	engines["vscode"] = patchedEngine
	res.PatchedEngine = patchedEngine

	patched, err := json.Marshal(doc)
	if err != nil {
		return 0, fmt.Errorf("vsix: encode patched manifest: %w", err)
	}

	hdr := &zip.FileHeader{
		Name:          srcFile.Name,
		Method:        zip.Deflate,
		Modified:      srcFile.Modified,
		Comment:       srcFile.Comment,
		Extra:         nil, // non-goal: manifest Extra is dropped
		NonUTF8:       srcFile.NonUTF8,
		ExternalAttrs: srcFile.ExternalAttrs,
	}
	hdr.SetMode(srcFile.Mode())

	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return 0, fmt.Errorf("vsix: create patched manifest entry: %w", err)
	}
	n, err := w.Write(patched)
	if err != nil {
		return 0, fmt.Errorf("vsix: write patched manifest: %w", err)
	}
	return int64(n), nil
}
