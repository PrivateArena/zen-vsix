// Package app orchestrates the full zen-vsix pipeline: resolve URL → download
// (or use local) → read manifest → decide whether the fork's engine is
// satisfied → rewrite only when needed → verify → place output atomically.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"zen-vsix/internal/config"
	"zen-vsix/internal/detect"
	"zen-vsix/internal/download"
	"zen-vsix/internal/extract"
	"zen-vsix/internal/vsix"
)

// Options are the per-invocation knobs. Zero values fall through to the
// config file / auto-detection.
type Options struct {
	ConfigPath        string // config.json path; ""→ defaults
	EngineOverride    string // --engine-version (highest precedence)
	OutputDirOverride string // -o (wins over config output_dir)
	KeepOriginal      bool   // -k: preserve the pristine download next to output
	NoPatch           bool   // download only, never rewrite
}

// Result reports what a run did, for the CLI's human-readable summary.
type Result struct {
	Artifact       *extract.Artifact
	OriginalVSIX   string // kept/supplied unpatched artifact ("" if ephemeral & removed)
	PatchedVSIX    string // patched output ("" if none produced)
	Patched        bool
	OriginalEngine string // engines.vscode read from the artifact
	PatchedEngine  string // "^"+engine written in the patched manifest
	BytesWritten   int64
	SkippedReason  string // human note when no patch was produced
}

// Run executes the pipeline. rawURL is a store URL or a local .vsix path.
func Run(ctx context.Context, rawURL string, opts Options) (*Result, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	outDir, err := resolveOutputDir(cfg, opts.OutputDirOverride)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("pipeline: cannot create output directory %s: %w", outDir, err)
	}

	ext, ok := extract.ForURL(rawURL)
	if !ok {
		return nil, extract.ErrNoExtractor
	}
	art, err := ext.Extract(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	// Critical section: patch→verify→rename serialized by an advisory flock
	// so concurrent runs can't tear output or race on the same output name.
	lock, err := acquireLock(ctx, filepath.Join(outDir, ".lock"))
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	src, tmpPath, err := obtainVSIX(ctx, art, outDir)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpPath) // no-op if already renamed away / local

	m, err := readManifestAt(src)
	if err != nil {
		return nil, err
	}

	// Backfill empty identity from the manifest (direct/local inputs).
	if art.Publisher == "" {
		art.Publisher = m.Publisher
	}
	if art.Name == "" {
		art.Name = m.Name
	}
	if art.Version == "" {
		art.Version = m.Version
	}

	res := &Result{Artifact: art, OriginalEngine: m.Engines["vscode"]}
	constraint := strings.TrimSpace(m.Engines["vscode"])

	engine := ""
	needPatch := false
	switch {
	case opts.NoPatch:
		res.SkippedReason = "no-patch requested; downloaded only (would " + skipBecause(constraint) + ")"
	case constraint == "":
		res.SkippedReason = "extension declares no engines.vscode; kept as-is"
	default:
		engine, err = resolveEngine(opts.EngineOverride, cfg.ForceEngineVersion, cfg.VSIXPath)
		if err != nil {
			return nil, err
		}
		needPatch, err = vsix.NeedsPatch(constraint, engine)
		if err != nil {
			return nil, err
		}
		if !needPatch && !vsix.ConstraintParses(constraint) {
			res.SkippedReason = "constraint is unparseable; kept original (non-destructive)"
		}
	}

	if needPatch {
		if err := setPatched(ctx, cfg, opts, outDir, src, tmpPath, rawURL, art, engine, res); err != nil {
			return nil, err
		}
	} else {
		if err := keepOriginal(ctx, cfg, outDir, tmpPath, rawURL, art, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// setPatched writes the patched artifact to a temp, verifies it (in-process +
// external unzip), then renames it atomically into place under the lock. Any
// failure leaves the temp un-promoted (no torn final output).
func setPatched(ctx context.Context, cfg *config.Config, opts Options, outDir, src, tmpPath, rawURL string, art *extract.Artifact, engine string, res *Result) error {
	final := filepath.Join(outDir, patchedName(art, baseFromInput(rawURL, tmpPath)))
	if !cfg.Overwrite {
		if _, err := os.Stat(final); err == nil {
			return fmt.Errorf("pipeline: output %s already exists (set overwrite=true to replace)", final)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("pipeline: stat output %s: %w", final, err)
		}
	}

	ptmp, err := os.CreateTemp(outDir, ".tmp-patched-*.vsix")
	if err != nil {
		return fmt.Errorf("pipeline: create patch temp: %w", err)
	}
	pname := ptmp.Name()
	ptmp.Close()

	if err := ctx.Err(); err != nil {
		os.Remove(pname)
		return fmt.Errorf("pipeline: cancelled before patch: %w", err)
	}

	pr, err := vsix.PatchVSIX(ctx, src, pname, vsix.PatchOptions{EngineVersion: engine})
	if err != nil {
		os.Remove(pname)
		return fmt.Errorf("pipeline: patch: %w", err)
	}
	if err := vsix.VerifyPatched(pname, engine); err != nil {
		os.Remove(pname)
		return fmt.Errorf("pipeline: post-patch verification: %w", err)
	}
	if err := vsix.VerifyWithExternal(pname); err != nil {
		os.Remove(pname)
		return fmt.Errorf("pipeline: external verification (%w)", err)
	}
	if err := os.Rename(pname, final); err != nil {
		os.Remove(pname)
		return fmt.Errorf("pipeline: rename patched into place: %w", err)
	}

	res.Patched = true
	res.PatchedVSIX = final
	res.PatchedEngine = pr.PatchedEngine
	res.OriginalEngine = pr.OriginalEngine
	res.BytesWritten = pr.BytesWritten

	// Keep-original: preserve the pristine download next to the output.
	if opts.KeepOriginal && tmpPath != "" {
		orig := filepath.Join(outDir, "original-"+baseFromInput(rawURL, tmpPath))
		if rerr := os.Rename(tmpPath, orig); rerr == nil {
			res.OriginalVSIX = orig
		} else {
			res.OriginalVSIX = src
		}
	}
	return nil
}

// keepOriginal handles the not-patched case: a remote download is renamed into
// out/ under its basename; a local input stays in place untouched.
func keepOriginal(_ context.Context, cfg *config.Config, outDir, tmpPath, rawURL string, art *extract.Artifact, res *Result) error {
	if art.LocalPath != "" {
		res.OriginalVSIX = art.LocalPath
		return nil
	}
	if tmpPath == "" {
		return nil
	}
	final := filepath.Join(outDir, baseFromInput(rawURL, tmpPath))
	if !cfg.Overwrite {
		if _, err := os.Stat(final); err == nil {
			return fmt.Errorf("pipeline: output %s already exists (set overwrite=true to replace)", final)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("pipeline: stat output %s: %w", final, err)
		}
	}
	if err := os.Rename(tmpPath, final); err != nil {
		return fmt.Errorf("pipeline: rename original into place: %w", err)
	}
	res.OriginalVSIX = final
	return nil
}

// obtainVSIX returns the vsix source path and (for remote inputs) the download
// temp. Local inputs have tmpPath "".
func obtainVSIX(ctx context.Context, art *extract.Artifact, outDir string) (src, tmp string, err error) {
	if art.LocalPath != "" {
		return art.LocalPath, "", nil
	}
	tf, err := os.CreateTemp(outDir, ".tmp-*.vsix")
	if err != nil {
		return "", "", fmt.Errorf("pipeline: create temp download: %w", err)
	}
	tf.Close()
	if _, err := download.Download(ctx, art.DirectURL, tf.Name()); err != nil {
		os.Remove(tf.Name())
		return "", "", err
	}
	return tf.Name(), tf.Name(), nil
}

// readManifestAt opens src and reads its extension manifest.
func readManifestAt(src string) (*vsix.Manifest, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, fmt.Errorf("pipeline: open vsix %s: %w", src, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("pipeline: stat vsix %s: %w", src, err)
	}
	return vsix.ReadManifest(f, fi.Size())
}

// resolveEngine applies precedence: CLI --engine-version > config
// force_engine_version > detect fork version. Detection failure is surfaced
// with a pointer at the escape hatches.
func resolveEngine(cli, cfgForce, vscodePath string) (string, error) {
	if cli != "" {
		return cli, nil
	}
	if cfgForce != "" {
		return cfgForce, nil
	}
	v, err := detect.DetectEngineVersion(vscodePath)
	if errors.Is(err, detect.ErrNoProduct) {
		return "", fmt.Errorf("pipeline: no fork engine version detected (vscode_path=%q); set force_engine_version in config.json or pass --engine-version: %w", vscodePath, err)
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// resolveOutputDir applies -o precedence over config, then anchors it.
func resolveOutputDir(cfg *config.Config, override string) (string, error) {
	if override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("pipeline: resolve -o %q: %w", override, err)
		}
		return filepath.Clean(abs), nil
	}
	return cfg.ResolveOutputDir()
}

// patchedName builds the output filename preferring backfilled pub.name-ver.
func patchedName(art *extract.Artifact, fallbackBase string) string {
	if art.Publisher != "" && art.Name != "" && art.Version != "" {
		return fmt.Sprintf("%s.%s-%s-patched.vsix", art.Publisher, art.Name, art.Version)
	}
	if fallbackBase == "" {
		fallbackBase = "artifact.vsix"
	}
	return strings.TrimSuffix(fallbackBase, filepath.Ext(fallbackBase)) + "-patched.vsix"
}

// baseFromInput derives a base filename for placement/original naming. The real
// URL path basename is preferred (remote downloads reach the temp path as
// ".tmp-<random>.vsix", which is not a meaningful output name); the path
// argument is only a tie-breaker and is ignored when the URL yields a name.
func baseFromInput(rawURL, _ string) string {
	if u, err := url.Parse(rawURL); err == nil {
		b := filepath.Base(u.Path)
		if b != "." && b != "/" && b != "" && !strings.HasPrefix(b, ".tmp-") {
			return b
		}
	}
	return "artifact.vsix"
}

func skipBecause(constraint string) string {
	if strings.TrimSpace(constraint) == "" {
		return "no engines.vscode constraint"
	}
	return "patch because engine constraint is unmet"
}
