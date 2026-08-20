package detect

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProduct(t *testing.T, root, rel, version string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"nameShort":"Fork","version":"`+version+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDetectAntigravityLayout(t *testing.T) {
	root := t.TempDir()
	writeProduct(t, root, "resources/app/product.json", "1.107.0")
	got, err := DetectEngineVersion(root)
	if err != nil {
		t.Fatalf("DetectEngineVersion error: %v", err)
	}
	if got != "1.107.0" {
		t.Fatalf("got %q, want 1.107.0", got)
	}
}

func TestDetectPrereleaseRetained(t *testing.T) {
	root := t.TempDir()
	writeProduct(t, root, "product.json", "1.107.0-insider")
	got, err := DetectEngineVersion(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.107.0-insider" {
		t.Fatalf("prerelease must be retained, got %q", got)
	}
}

func TestDetectEmptyDirIsErrNoProduct(t *testing.T) {
	root := t.TempDir()
	_, err := DetectEngineVersion(root)
	if !errors.Is(err, ErrNoProduct) {
		t.Fatalf("empty root: want ErrNoProduct, got %v", err)
	}
}

func TestDetectMissingRootIsErrNoProduct(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nope")
	_, err := DetectEngineVersion(root)
	if !errors.Is(err, ErrNoProduct) {
		t.Fatalf("missing root: want ErrNoProduct, got %v", err)
	}
}

func TestDetectEmptyVersionIsParseErrorNotErrNoProduct(t *testing.T) {
	root := t.TempDir()
	writeProduct(t, root, "resources/app/product.json", "")
	_, err := DetectEngineVersion(root)
	if errors.Is(err, ErrNoProduct) {
		t.Fatal("empty version must NOT be ErrNoProduct — a file was found")
	}
	if err == nil {
		t.Fatal("empty version must error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error should name the empty version, got: %v", err)
	}
}

func TestDetectGarbageVersionIsParseError(t *testing.T) {
	root := t.TempDir()
	writeProduct(t, root, "resources/app/product.json", "banana")
	_, err := DetectEngineVersion(root)
	if errors.Is(err, ErrNoProduct) {
		t.Fatal("garbage version must NOT be ErrNoProduct")
	}
	if err == nil {
		t.Fatal("garbage version must error")
	}
	if !strings.Contains(err.Error(), "not valid semver") {
		t.Fatalf("error should mention semver, got: %v", err)
	}
}

func TestDetectInvalidJSONIsParseError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "resources", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "resources", "app", "product.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := DetectEngineVersion(root)
	if errors.Is(err, ErrNoProduct) {
		t.Fatal("broken JSON must NOT be ErrNoProduct")
	}
	if err == nil {
		t.Fatal("broken JSON must error")
	}
}

func TestDetectFallbackPaths(t *testing.T) {
	// resources/app/package.json fallback
	root := t.TempDir()
	writeProduct(t, root, "resources/app/package.json", "2.0.0")
	if got, _ := DetectEngineVersion(root); got != "2.0.0" {
		t.Fatalf("resources/app/package.json fallback: got %q", got)
	}

	// root product.json fallback
	root2 := t.TempDir()
	writeProduct(t, root2, "product.json", "3.0.0")
	if got, _ := DetectEngineVersion(root2); got != "3.0.0" {
		t.Fatalf("root product.json fallback: got %q", got)
	}

	// root package.json fallback
	root3 := t.TempDir()
	writeProduct(t, root3, "package.json", "4.0.0")
	if got, _ := DetectEngineVersion(root3); got != "4.0.0" {
		t.Fatalf("root package.json fallback: got %q", got)
	}
}

func TestDetectProbeOrderFirstMatchWins(t *testing.T) {
	root := t.TempDir()
	// Both exist: resources/app/product.json must win.
	writeProduct(t, root, "resources/app/product.json", "1.107.0")
	writeProduct(t, root, "product.json", "9.9.9")
	got, err := DetectEngineVersion(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.107.0" {
		t.Fatalf("first-match order violated: got %q want 1.107.0", got)
	}
}
