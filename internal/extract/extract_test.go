package extract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

func TestForURLDispatchOrder(t *testing.T) {
	cases := []struct {
		raw  string
		want string // extractor Name()
	}{
		{"https://marketplace.visualstudio.com/items?itemName=cdervis.vscode-pi", "marketplace"},
		{"https://marketplace.visualstudio.com/_apis/public/gallery/publishers/cdervis/vsextensions/vscode-pi/1.0.0/vspackage", "marketplace"},
		{"https://open-vsx.org/extension/foo/bar", "openvsx"},
		{"https://open-vsx.org/api/foo/bar/1.2.3", "openvsx"},
		{"https://example.com/stuff/ext.vsix", "direct"},
		{"https://example.com/ext.vsix?token=abc#frag", "direct"},
		{"file:///tmp/local.vsix", "direct"},
	}
	for _, c := range cases {
		e, ok := ForURL(c.raw)
		if !ok {
			t.Fatalf("ForURL(%q): no extractor", c.raw)
		}
		if e.Name() != c.want {
			t.Fatalf("ForURL(%q) = %s, want %s", c.raw, e.Name(), c.want)
		}
	}
}

func TestForURLUnknownIsNoMatch(t *testing.T) {
	for _, raw := range []string{
		"https://example.com/foo.txt",
		"https://github.com/owner/repo/releases/download/v1/thing.tar.gz",
		"open-vsx.org/extension/foo/bar", // missing scheme → no match
		"not a url at all",
		"",
	} {
		if _, ok := ForURL(raw); ok {
			t.Fatalf("ForURL(%q) unexpectedly matched", raw)
		}
	}
}

// Extension-recipe regression: a local .vsix file must dispatch to direct
// even when the basename contains store-like segments.
func TestForURLBareVsixDispatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ext.vsix")
	if err := os.WriteFile(p, []byte("pk"), 0o600); err != nil {
		t.Fatal(err)
	}
	e, ok := ForURL(p)
	if !ok || e.Name() != "direct" {
		t.Fatalf("ForURL(bare vsix) = %v,%v want direct", e, ok)
	}
}

// ---- direct extractor ----

func TestDirectHTTPURL(t *testing.T) {
	d := &directExtractor{}
	for _, raw := range []string{
		"https://example.com/ext.vsix",
		"http://example.com/a.b.vsix?token=x#frag",
	} {
		art, err := d.Extract(context.Background(), raw)
		if err != nil {
			t.Fatalf("Extract(%q): %v", raw, err)
		}
		if art.DirectURL != raw {
			t.Fatalf("DirectURL = %q, want %q", art.DirectURL, raw)
		}
		if art.LocalPath != "" || art.Publisher != "" || art.Name != "" || art.Version != "" {
			t.Fatalf("remote artifact must leave local/identity fields empty: %+v", art)
		}
		if art.Source != "direct" {
			t.Fatalf("Source = %q", art.Source)
		}
	}
}

func TestDirectLocalPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bundle.vsix")
	if err := os.WriteFile(p, []byte("PK"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &directExtractor{}
	art, err := d.Extract(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if art.LocalPath != p {
		t.Fatalf("LocalPath = %q, want %q", art.LocalPath, p)
	}
}

func TestDirectFileURL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "spaced name.vsix")
	if err := os.WriteFile(p, []byte("PK"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &directExtractor{}
	art, err := d.Extract(context.Background(), "file://"+p)
	if err != nil {
		t.Fatal(err)
	}
	if art.LocalPath != p {
		t.Fatalf("LocalPath = %q, want %q", art.LocalPath, p)
	}
}

func TestDirectMissingLocalPathErrors(t *testing.T) {
	d := &directExtractor{}
	p := filepath.Join(t.TempDir(), "ghost.vsix")
	if _, err := d.Extract(context.Background(), p); err == nil {
		t.Fatal("missing bare vsix path must error")
	}
}

func TestDirectNoMatch(t *testing.T) {
	d := &directExtractor{}
	if d.Pattern().MatchString("https://example.com/foo.txt") {
		t.Fatal("foo.txt must not match direct")
	}
	if d.Pattern().MatchString("foo.txt") {
		t.Fatal("foo.txt must not match direct")
	}
}

// ---- open-vsx extractor ----

func TestOpenVSXItemURL(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"bar","version":"1.2.3","files":{"download":"https://cdn.example/bar-1.2.3.vsix"}}`))
	}))
	defer ts.Close()

	o := &openVSXExtractor{baseURL: ts.URL}
	art, err := o.Extract(context.Background(), "https://open-vsx.org/extension/foo/bar")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/foo/bar" {
		t.Fatalf("API path = %q, want /api/foo/bar", gotPath)
	}
	if art.Publisher != "foo" || art.Name != "bar" || art.Version != "1.2.3" || art.DirectURL != "https://cdn.example/bar-1.2.3.vsix" {
		t.Fatalf("artifact = %+v", art)
	}
	if art.Source != "openvsx" {
		t.Fatalf("Source = %q", art.Source)
	}
}

func TestOpenVSXVersionSpecific(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"version":"0.9.0","files":{"download":"https://x/y.vsix"}}`))
	}))
	defer ts.Close()

	o := &openVSXExtractor{baseURL: ts.URL}
	if _, err := o.Extract(context.Background(), "https://open-vsx.org/api/foo/bar/0.9.0"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/foo/bar/0.9.0" {
		t.Fatalf("API path = %q, want version-qualified path", gotPath)
	}
}

func TestOpenVSXNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	o := &openVSXExtractor{baseURL: ts.URL}
	if _, err := o.Extract(context.Background(), "https://open-vsx.org/extension/ghost/ghost"); err == nil {
		t.Fatal("404 must error")
	}
}

// ---- marketplace extractor ----

func marketplaceTestServer(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var reqs atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("request carried Authorization header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	return ts, &reqs
}

func TestMarketplaceItemURL(t *testing.T) {
	const resp = `{"results":[{"extensions":[{
		"publisher":{"publisherName":"cdervis","displayName":"C.D."},
		"extensionName":"vscode-pi",
		"version":"2.0.0",
		"versions":[{"version":"2.0.0","files":[
			{"assetType":"Microsoft.VisualStudio.Services.VSIXPackage","source":"https://x/pi-2.0.0-darwin.vsix","targetPlatform":"darwin-x64"},
			{"assetType":"Microsoft.VisualStudio.Services.VSIXPackage","source":"https://x/pi-2.0.0.vsix","targetPlatform":"universal"}
		]}]}]}]}`
	ts, reqs := marketplaceTestServer(t, resp)
	defer ts.Close()

	m := &marketplaceExtractor{baseURL: ts.URL}
	art, err := m.Extract(context.Background(), "https://marketplace.visualstudio.com/items?itemName=cdervis.vscode-pi")
	if err != nil {
		t.Fatal(err)
	}
	if reqs.Load() != 1 {
		t.Fatalf("expected exactly 1 API call, got %d", reqs.Load())
	}
	if art.Publisher != "cdervis" || art.Name != "vscode-pi" || art.Version != "2.0.0" {
		t.Fatalf("identity: %+v", art)
	}
	if art.DirectURL != "https://x/pi-2.0.0.vsix" {
		t.Fatalf("DirectURL = %q, want universal asset preferred", art.DirectURL)
	}
}

func TestMarketplacePreferEmptyPlatformFallback(t *testing.T) {
	const resp = `{"results":[{"extensions":[{
		"extensionName":"e","version":"1.0.0",
		"versions":[{"version":"1.0.0","files":[
			{"assetType":"Microsoft.VisualStudio.Services.VSIXPackage","source":"https://x/only-darwin.vsix","targetPlatform":"darwin-x64"}
		]}]}]}]}`
	ts, _ := marketplaceTestServer(t, resp)
	defer ts.Close()

	m := &marketplaceExtractor{baseURL: ts.URL}
	art, err := m.Extract(context.Background(), "https://marketplace.visualstudio.com/items?itemName=p.e")
	if err != nil {
		t.Fatal(err)
	}
	if art.DirectURL != "https://x/only-darwin.vsix" {
		t.Fatalf("fallback to first VSIXPackage expected, got %q", art.DirectURL)
	}
}

func TestMarketplaceEmptyResultIsErrNotFound(t *testing.T) {
	// Empty extensions[] — historically an index-out-of-range panic.
	ts, _ := marketplaceTestServer(t, `{"results":[{"extensions":[]}]}`)
	defer ts.Close()

	m := &marketplaceExtractor{baseURL: ts.URL}
	_, err := m.Extract(context.Background(), "https://marketplace.visualstudio.com/items?itemName=ghost.ghost")
	if err == nil {
		t.Fatal("empty result must error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error should name not-found: %v", err)
	}
}

func TestMarketplaceEmptyVersionsIsErrNotFound(t *testing.T) {
	ts, _ := marketplaceTestServer(t, `{"results":[{"extensions":[{"versions":[]}]}]}`)
	defer ts.Close()

	m := &marketplaceExtractor{baseURL: ts.URL}
	if _, err := m.Extract(context.Background(), "https://marketplace.visualstudio.com/items?itemName=a.b"); err == nil {
		t.Fatal("empty versions must error")
	}
}

func TestMarketplaceNoVSIXPackageIsErrNotFound(t *testing.T) {
	const resp = `{"results":[{"extensions":[{"version":"1.0.0","versions":[{"version":"1.0.0","files":[
		{"assetType":"Microsoft.VisualStudio.Services.Icon","source":"https://x/i.png","targetPlatform":""}
	]}]}]}]}`
	ts, _ := marketplaceTestServer(t, resp)
	defer ts.Close()

	m := &marketplaceExtractor{baseURL: ts.URL}
	if _, err := m.Extract(context.Background(), "https://marketplace.visualstudio.com/items?itemName=a.b"); err == nil {
		t.Fatal("no VSIXPackage asset must error")
	}
}

func TestMarketplaceAPIPassthrough(t *testing.T) {
	// Direct API link form: artifact carries the raw URL, no network call.
	m := &marketplaceExtractor{baseURL: "http://unused.invalid"}
	raw := "https://marketplace.visualstudio.com/_apis/public/gallery/publishers/cdervis/vsextensions/vscode-pi/2.0.0/vspackage"
	art, err := m.Extract(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if art.DirectURL != raw {
		t.Fatalf("DirectURL = %q, want passthrough %q", art.DirectURL, raw)
	}
}

func TestMarketplaceItemWithExtraQueryParams(t *testing.T) {
	ts, _ := marketplaceTestServer(t, `{"results":[{"extensions":[{"versions":[{"version":"1.0.0","files":[
		{"assetType":"Microsoft.VisualStudio.Services.VSIXPackage","source":"https://x/y.vsix","targetPlatform":""}
	]}]}]}]}`)
	defer ts.Close()

	m := &marketplaceExtractor{baseURL: ts.URL}
	art, err := m.Extract(context.Background(), "https://marketplace.visualstudio.com/items?itemName=cdervis.vscode-pi&tab=overview")
	if err != nil {
		t.Fatal(err)
	}
	if art.Name != "vscode-pi" {
		t.Fatalf("extra query params broke name parsing: %+v", art)
	}
}

func TestRegistryAppendSemantics(t *testing.T) {
	// The extension recipe is one append to `all` — verify nothing later in
	// the slice can shadow an appended store when both patterns match.
	before := len(all)
	all = append(all, &fakeMatcher{})
	defer func() { all = all[:before] }()

	for _, raw := range []string{"https://x/a.vsix", "https://open-vsx.org/extension/a/b"} {
		if _, ok := ForURL(raw); !ok {
			t.Fatalf("appended extractor must receive unmatched input %q", raw)
		}
	}
}

type fakeMatcher struct{}

func (*fakeMatcher) Name() string { return "fake" }
func (*fakeMatcher) Pattern() *regexp.Regexp {
	return regexp.MustCompile(`.+`)
}
func (*fakeMatcher) Extract(ctx context.Context, _ string) (*Artifact, error) {
	return &Artifact{Source: "fake"}, nil
}
