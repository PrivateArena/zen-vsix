# zen-vsix

Make a VS Code-style store extension installable on a local VS Code fork
(Antigravity, Codex, Claude Desktop, …).

Extensions are increasingly released against newer `engines.vscode` versions
than the forks you actually run. `zen-vsix` takes any supported store URL — or a
local `.vsix` path — and, **only** when the extension requires a newer engine
than your fork, rewrites the archive in a single streaming pass: it recompresses
*just* `extension/package.json` (setting `engines.vscode = ^<fork-version>`) and
byte-copies every other entry raw. No full extraction, no temp tree,
**O(manifest) memory**.

If the extension already satisfies your fork's engine, it is kept byte-for-byte.

## Install

```sh
cd zen-vsix
go build -o zen-vsix .
```

Requires Go 1.23+. The only dependency is `github.com/Masterminds/semver/v3`.

## Usage

```sh
zen-vsix <URL-or-local.vsix> [--engine-version X] [-o out] [-k] [--no-patch]

# config.json is picked up automatically; -c is only needed for a non-standard
# location:
zen-vsix /path/to/ext.vsix -c /somewhere/else/config.json
```

Inputs:

- Visual Studio Marketplace item URL
  `https://marketplace.visualstudio.com/items?itemName=pub.name`
- Open VSX item/API URL `https://open-vsx.org/extension/pub/name[/ver]`
- A direct `.vsix` URL
- A local `.vsix` path (`/path/to/ext.vsix`)

Flags:

| Flag | Meaning |
|------|---------|
| `-c`, `-config` | path to `config.json`; when omitted the file is auto-discovered (see below) |
| `--engine-version` | override the fork engine version (highest precedence) |
| `-o` | output dir (wins over config `output_dir`) |
| `-k`, `-keep-original` | also keep the pristine download next to the patched output |
| `--no-patch` | download only, never rewrite the manifest |
| `-h` | help |

Example:

```sh
zen-vsix https://marketplace.visualstudio.com/items?itemName=cdervis.vscode-pi
```

With the shipped `config.json` (pointing at an Antigravity fork) this detects the
fork's engine (`1.107.0`), patches whether needed, and writes
`out/<pub>.<name>-<ver>-patched.vsix`. Concurrency-safe: a `flock` on
`out/.lock` serializes the rewrite→rename critical section.

## Configuration (`config.json`)

```jsonc
{
  "vscode_path": "/path/to/fork/",     // fork root; engine auto-detected from resources/app/product.json
  "output_dir": "out",                 // relative to this config file's directory
  "force_engine_version": "",          // "" = auto-detect; else pins the engine
  "overwrite": true                    // false = refuse to clobber an existing output
}
```

Engine resolution precedence: `--engine-version` flag > config `force_engine_version`
> auto-detect from `resources/app/product.json` (falling back to
`resources/app/package.json`, `product.json`, `package.json`). No file found, and
no override → clear error pointing at the escape hatches.

### Where `config.json` is found

Without `-c`, the first existing file among these is loaded:

1. `$ZEN_VSIX_CONFIG` (if set, it must point at a readable file)
2. `./config.json` — the directory you ran `zen-vsix` from
3. `<dir of the zen-vsix binary>/config.json`
4. `<user config dir>/zen-vsix/config.json` — e.g. `~/.config/zen-vsix/config.json`

If none exists, built-in defaults apply (no `vscode_path`, `output_dir` = `out`
relative to the working directory). `-c` with an unreadable path is an error.

## Adding a store (yt-dlp-style extension point)

A store is one new file plus one append:

1. Create `internal/extract/<source>.go` implementing `extract.Extractor`:

   ```go
   type myStoreExtractor struct{ baseURL string }

   func (*myStoreExtractor) Name() string                       { return "mystore" }
   func (*myStoreExtractor) Pattern() *regexp.Regexp            { return regexp.MustCompile(`^https://mystore\.example/(...)`) }
   func (*myStoreExtractor) Extract(ctx context.Context, rawURL string) (*extract.Artifact, error) {
       // resolve rawURL → Artifact{Publisher, Name, Version, DirectURL, Source}
   }
   ```

2. Append it to the ordered `all` slice in `internal/extract/registry.go`
   (first match wins; put it **before** `directExtractor` if it owns `.vsix` URLs):

   ```go
   var all = []Extractor{
       &marketplaceExtractor{},
       &openVSXExtractor{},
       &myStoreExtractor{},   // <-- new
       &directExtractor{},
   }
   ```

Nothing else changes. The pipeline and CLI are source-agnostic.

## Safety model

- **Non-destructive by default.** Empty, whitespace, or *unparseable*
  `engines.vscode` constraint → the original is kept (warned), never guessed.
- **Verify before promote.** After rewriting, the patched archive is re-opened
  and its manifest re-read, then cross-checked by the system `unzip -t` (an
  independent parser). The result is renamed into place only after both pass.
- **Atomic placement.** Downloads land in a unique temp file; the patched
  output is written to a temp and renamed over the final name.
- **Raw-copy fidelity.** Non-manifest entries are copied as their raw compressed
  streams (bit-3 data-descriptor and zip64 sources covered by regression fixtures).

## ⚠️ “Patching” ≠ API compatibility

Rewriting `engines.vscode` makes VS Code *install* the extension. It does **not**
add APIs the fork lacks — an extension that calls an API introduced after your
fork may install fine but still break at runtime. Lowering the engine requirement
is a convenience for the install gate, not a compatibility guarantee.

## Verification

```sh
go test ./... -count=1 -race
```