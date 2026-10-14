// Command zen-vsix turns a store URL (or local .vsix path) into a VSIX that
// installs on a local VS Code-style fork. When the extension's engines.vscode
// exceeds the fork's engine version, the manifest is rewritten in a single
// streaming pass; otherwise the artifact is kept as-is.
//
// Usage:
//
//	zen-vsix <URL-or-local.vsix> [-c config.json] [--engine-version X] [-o out] [-k] [--no-patch]
//
// Without -c the config file is auto-discovered: $ZEN_VSIX_CONFIG, then
// config.json in the working directory, next to the executable, or in the
// user config directory.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"zen-vsix/internal/app"
	"zen-vsix/internal/config"
	"zen-vsix/internal/extract"
)

const timeout = 60 * time.Second

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("zen-vsix", flag.ExitOnError)
	configPath := fs.String("c", "", "path to config.json (default: auto-discovered)")
	fs.StringVar(configPath, "config", "", "path to config.json (default: auto-discovered)")
	engine := fs.String("engine-version", "", "override fork engine version (highest precedence)")
	outputDir := fs.String("o", "", "output directory (wins over config output_dir)")
	keep := fs.Bool("k", false, "keep the pristine download next to the patched output")
	fs.BoolVar(keep, "keep-original", false, "keep the pristine download next to the patched output")
	noPatch := fs.Bool("no-patch", false, "download only; never rewrite the manifest")
	help := fs.Bool("h", false, "show help")

	// Go's flag package stops at the first positional; the documented usage
	// (zen-vsix <URL> [-c ...] [-o ...]) interleaves flags with the URL, so
	// extract the single positional manually and hand the rest to flag.
	positional, flagArgs, err := splitPositional(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zen-vsix: %v\n", err)
		return 1
	}
	_ = fs.Parse(flagArgs)

	if *help || len(positional) != 1 {
		usage(fs)
		if *help {
			return 0
		}
		return 1
	}
	rawURL := positional[0]

	cfgPath, err := resolveConfigPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "zen-vsix: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	res, err := app.Run(ctx, rawURL, app.Options{
		ConfigPath:        cfgPath,
		EngineOverride:    *engine,
		OutputDirOverride: *outputDir,
		KeepOriginal:      *keep,
		NoPatch:           *noPatch,
	})
	if err != nil {
		if errors.Is(err, extract.ErrNoExtractor) {
			fmt.Fprintln(os.Stderr, err)
		} else {
			fmt.Fprintf(os.Stderr, "zen-vsix: %v\n", err)
		}
		return 1
	}
	printSummary(res)
	return 0
}

// resolveConfigPath applies the config precedence: -c/-config wins and must
// point at a readable file; otherwise the file is auto-discovered ($ZEN_VSIX_CONFIG,
// ./config.json, config.json next to the binary, <user config>/zen-vsix/config.json).
// An empty result means "no config file" and the pipeline uses its defaults.
func resolveConfigPath(flagPath string) (string, error) {
	if flagPath == "" {
		return config.Discover()
	}
	if err := config.ValidatePath(flagPath); err != nil {
		return "", err
	}
	return flagPath, nil
}

// splitPositional separates the single positional argument from flags, working
// regardless of ordering. valueFlags are flags that consume a following token.
func splitPositional(args []string) (positional []string, flags []string, err error) {
	valueFlags := map[string]bool{"c": true, "config": true, "o": true, "engine-version": true}
	flagBase := func(a string) string {
		a = strings.TrimLeft(a, "-")
		if i := strings.IndexByte(a, '='); i >= 0 {
			a = a[:i]
		}
		return a
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			base := flagBase(a)
			if valueFlags[base] && !strings.Contains(a, "=") {
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("flag %s requires a value", a)
				}
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return positional, flags, nil
}

func usage(fs *flag.FlagSet) {
	fmt.Fprintf(os.Stderr, `zen-vsix: make a store extension installable on a local VS Code fork.

Usage:
  zen-vsix <URL-or-local.vsix> [flags]

Input:
  a marketplace/open-vsx URL, a direct .vsix URL, or a local .vsix path.

Flags:
`)
	fs.SetOutput(os.Stderr)
	fs.PrintDefaults()
	fmt.Fprint(os.Stderr, `
Without -c, config.json is loaded automatically when found in the current
directory, next to the zen-vsix binary, or at $ZEN_VSIX_CONFIG.

When the extension requires a newer engine than the fork, only
extension/package.json is rewritten (engines.vscode = ^<fork-version>);
every other entry is copied byte-for-byte. Otherwise the artifact is kept.
`)
}

func printSummary(r *app.Result) {
	source := r.Artifact.Source
	label := fmt.Sprintf("%s.%s-%s", first(r.Artifact.Publisher, "?"), first(r.Artifact.Name, "?"), first(r.Artifact.Version, "?"))
	_ = source

	switch {
	case r.Patched:
		fmt.Printf("patched:  %s\n", r.PatchedVSIX)
		fmt.Printf("engine:   %s -> %s\n", first(r.OriginalEngine, "(none)"), r.PatchedEngine)
		fmt.Printf("bytes:    %d rewritten\n", r.BytesWritten)
	case r.SkippedReason != "":
		fmt.Printf("not patched: %s\n", r.SkippedReason)
		fmt.Printf("artifact: %s\n", first(r.OriginalVSIX, label))
	default:
		fmt.Printf("not patched: engine constraint already satisfied\n")
		fmt.Printf("artifact: %s\n", first(r.OriginalVSIX, label))
	}
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
