package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"zen-vsix/internal/extract"
	"zen-vsix/internal/vsix"
)

// buildVSIXBytes returns a minimal valid vsix with the given engines.vscode.
func buildVSIXBytes(engine string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "extension/package.json", Method: zip.Deflate})
	_, _ = w.Write([]byte(`{"name":"vscode-pi","publisher":"cdervis","version":"2.0.0","engines":{"vscode":"` + engine + `"},"extra":"preserved"}`))
	w2, _ := zw.Create("extension/out/ext.js")
	_, _ = w2.Write([]byte("// extension code\n"))
	_ = zw.Close()
	return buf.Bytes()
}

// forkTree writes a product.json reporting version v and returns the root.
func forkTree(t *testing.T, v string) string {
	t.Helper()
	root := t.TempDir()
	p := filepath.Join(root, "resources", "app", "product.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"nameShort":"Fork","version":"`+v+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// writeConfig writes a config.json with absolute output dir.
func writeConfig(t *testing.T, forkRoot, outDir string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	body := fmt.Sprintf(`{"vscode_path": %q, "output_dir": %q, "force_engine_version": "", "overwrite": true}`,
		forkRoot, outDir)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// marketplaceServer serves both the extensionquery POST and a .vsix download.
func marketplaceServer(t *testing.T, vsixData []byte) *httptest.Server {
	t.Helper()
	var ts *httptest.Server
	dl := "/download.vsix"
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == dl {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(vsixData)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		body := fmt.Sprintf(`{"results":[{"extensions":[
			{"publisher":{"publisherName":"cdervis"},"extensionName":"vscode-pi","version":"2.0.0",
			 "versions":[{"version":"2.0.0","files":[
			 	{"assetType":"Microsoft.VisualStudio.Services.VSIXPackage","source":%q,"targetPlatform":"universal"}
			 ]}]}
		]}]}`, ts.URL+dl)
		_, _ = w.Write([]byte(body))
	}))
	return ts
}

func manifestOfFile(t *testing.T, path string) *vsix.Manifest {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	m, err := vsix.ReadManifest(f, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRunMarketplacePatchesToForkEngine(t *testing.T) {
	vsixData := buildVSIXBytes("^1.109.0") // unmet by fork 1.107.0 → patch needed
	ts := marketplaceServer(t, vsixData)
	defer ts.Close()

	restore := extract.SetRegistryForTesting([]extract.Extractor{
		extract.NewMarketplaceExtractor(ts.URL),
		extract.NewDirectExtractor(),
	})
	defer restore()

	outDir := t.TempDir()
	cfgPath := writeConfig(t, forkTree(t, "1.107.0"), outDir)

	raw := "https://marketplace.visualstudio.com/items?itemName=cdervis.vscode-pi"
	res, err := Run(context.Background(), raw, Options{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Patched {
		t.Fatalf("met=%q fork must be patched, Patched=false (reason: %s)", res.Artifact, res.SkippedReason)
	}
	if res.PatchedEngine != "^1.107.0" {
		t.Fatalf("PatchedEngine = %q", res.PatchedEngine)
	}
	if res.OriginalEngine != "^1.109.0" {
		t.Fatalf("OriginalEngine = %q", res.OriginalEngine)
	}
	want := filepath.Join(outDir, "cdervis.vscode-pi-2.0.0-patched.vsix")
	if res.PatchedVSIX != want {
		t.Fatalf("PatchedVSIX = %q, want %q", res.PatchedVSIX, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("patched file missing: %v", err)
	}
	m := manifestOfFile(t, want)
	if m.Engines["vscode"] != "^1.107.0" {
		t.Fatalf("patched manifest engines.vscode = %q", m.Engines["vscode"])
	}
}

func TestRunLocalVsixProducesPatchedSibling(t *testing.T) {
	// patch.sh parity: a local .vsix with an unmet constraint yields a
	// -patched artifact in out/.
	outDir := t.TempDir()
	src := filepath.Join(t.TempDir(), "bundle.vsix")
	if err := os.WriteFile(src, buildVSIXBytes("^1.109.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeConfig(t, forkTree(t, "1.107.0"), outDir)

	res, err := Run(context.Background(), src, Options{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("Run(local): %v", err)
	}
	if !res.Patched {
		t.Fatalf("local unmet constraint must patch")
	}
	want := filepath.Join(outDir, "cdervis.vscode-pi-2.0.0-patched.vsix")
	if res.PatchedVSIX != want {
		t.Fatalf("PatchedVSIX = %q, want %q", res.PatchedVSIX, want)
	}
	if m := manifestOfFile(t, want); m.Engines["vscode"] != "^1.107.0" {
		t.Fatalf("engines.vscode = %q", m.Engines["vscode"])
	}
}

func TestRunSatisfiedIsUntouched(t *testing.T) {
	outDir := t.TempDir()
	src := filepath.Join(t.TempDir(), "ok.vsix")
	if err := os.WriteFile(src, buildVSIXBytes("^1.90.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeConfig(t, forkTree(t, "1.107.0"), outDir)

	res, err := Run(context.Background(), src, Options{ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("Run(satisfied): %v", err)
	}
	if res.Patched {
		t.Fatalf("satisfied constraint must NOT be patched")
	}
	if res.PatchedVSIX != "" {
		t.Fatalf("no patched file expected, got %q", res.PatchedVSIX)
	}
	if res.OriginalVSIX != src {
		t.Fatalf("local input must be reported as original, got %q", res.OriginalVSIX)
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "*patched*.vsix"))
	if len(matches) != 0 {
		t.Fatalf("satisfied run wrote patched files unexpectedly: %v", matches)
	}
}

func TestRunEngineOverrideWins(t *testing.T) {
	outDir := t.TempDir()
	src := filepath.Join(t.TempDir(), "a.vsix")
	// Constraint satisfied by auto-detected fork 1.107.0, NOT by override 1.50.0.
	if err := os.WriteFile(src, buildVSIXBytes("^1.90.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeConfig(t, forkTree(t, "1.107.0"), outDir)

	res, err := Run(context.Background(), src, Options{ConfigPath: cfgPath, EngineOverride: "1.50.0"})
	if err != nil {
		t.Fatalf("Run(override): %v", err)
	}
	if !res.Patched {
		t.Fatalf("--engine-version must override detection and force patch")
	}
	if res.PatchedEngine != "^1.50.0" {
		t.Fatalf("PatchedEngine = %q, want ^1.50.0", res.PatchedEngine)
	}
}

func TestRunNoPatchNeverRewrites(t *testing.T) {
	vsixData := buildVSIXBytes("^1.109.0")
	ts := marketplaceServer(t, vsixData)
	defer ts.Close()
	restore := extract.SetRegistryForTesting([]extract.Extractor{extract.NewMarketplaceExtractor(ts.URL)})
	defer restore()

	outDir := t.TempDir()
	cfgPath := writeConfig(t, forkTree(t, "1.107.0"), outDir)
	res, err := Run(context.Background(),
		"https://marketplace.visualstudio.com/items?itemName=cdervis.vscode-pi",
		Options{ConfigPath: cfgPath, NoPatch: true})
	if err != nil {
		t.Fatalf("Run(no-patch): %v", err)
	}
	if res.Patched {
		t.Fatal("no-patch must never rewrite")
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "*patched*.vsix"))
	if len(matches) != 0 {
		t.Fatalf("no-patch wrote patched files: %v", matches)
	}
	if _, err := os.Stat(res.OriginalVSIX); err != nil {
		t.Fatalf("no-patch original must be kept at %q: %v", res.OriginalVSIX, err)
	}
}

func TestRunUnknownURLIsErrNoExtractor(t *testing.T) {
	outDir := t.TempDir()
	cfgPath := writeConfig(t, forkTree(t, "1.107.0"), outDir)
	_, err := Run(context.Background(), "https://example.com/random.txt", Options{ConfigPath: cfgPath})
	if err == nil {
		t.Fatal("unknown URL must error")
	}
	if !strings.Contains(err.Error(), "no extractor") {
		t.Fatalf("err should name ErrNoExtractor: %v", err)
	}
}

func TestRunConcurrentSameOutputNoCorruption(t *testing.T) {
	vsixData := buildVSIXBytes("^1.109.0")
	ts := marketplaceServer(t, vsixData)
	defer ts.Close()
	restore := extract.SetRegistryForTesting([]extract.Extractor{extract.NewMarketplaceExtractor(ts.URL)})
	defer restore()

	outDir := t.TempDir()
	cfgPath := writeConfig(t, forkTree(t, "1.107.0"), outDir)
	raw := "https://marketplace.visualstudio.com/items?itemName=cdervis.vscode-pi"

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = Run(context.Background(), raw, Options{ConfigPath: cfgPath})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent run %d: %v", i, err)
		}
	}
	// Last-writer-wins under the lock; the survivor must be a valid patched zip.
	want := filepath.Join(outDir, "cdervis.vscode-pi-2.0.0-patched.vsix")
	m := manifestOfFile(t, want)
	if m.Engines["vscode"] != "^1.107.0" {
		t.Fatalf("final engine = %q", m.Engines["vscode"])
	}
}

// Manifest extras round-trip check: read via generic doc to assert "extra"
// was preserved by the map-based manifest rewrite.
func TestRunManifestExtraPreserved(t *testing.T) {
	outDir := t.TempDir()
	src := filepath.Join(t.TempDir(), "extra.vsix")
	if err := os.WriteFile(src, buildVSIXBytes("^1.109.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeConfig(t, forkTree(t, "1.107.0"), outDir)
	res, err := Run(context.Background(), src, Options{ConfigPath: cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(res.PatchedVSIX)
	defer f.Close()
	fi, _ := f.Stat()
	doc := map[string]any{}
	if err := readManifestDoc(f, fi.Size(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["extra"] != "preserved" {
		t.Fatalf("unrelated manifest field lost: %v", doc)
	}
}

func readManifestDoc(r interface {
	ReadAt(p []byte, off int64) (int, error)
}, size int64, out any) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if f.Name == "extension/package.json" {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			return json.NewDecoder(rc).Decode(out)
		}
	}
	return fmt.Errorf("no manifest")
}
