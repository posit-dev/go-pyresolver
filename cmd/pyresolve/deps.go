// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/posit-dev/go-python-packaging/version"

	"github.com/posit-dev/go-pyresolver/index"
)

const depsHelp = `Usage: pyresolve deps --rsf <path> <package> [version] [--json]

Print the dependency metadata for one version of <package>: its
Requires-Python constraint, its Requires-Dist requirement strings, and its
declared extras.

When [version] is omitted, the highest captured PEP 440 version is used, except
that pre-releases are skipped unless a package has nothing else — the default
PEP 440 selection rule. Name a version explicitly to inspect a pre-release.

Flags:
  --rsf <path>   path to the RSF file (or set PYRESOLVE_RSF)
  --json         emit JSON instead of text

` + noNetworkNotice + "\n"

// depsResult is the deps command's output shape.
type depsResult struct {
	Package        string `json:"package"`
	Version        string `json:"version"`
	RequiresPython string `json:"requires_python,omitempty"`

	// RequiresPythonRaw and RequiresPythonUnreadable are only emitted when the
	// record's interpreter constraint could not be parsed. Together they say
	// "the publisher declared this, and we could not read it" -- which the
	// RequiresPython field alone cannot express, since an unreadable constraint
	// and an absent one both leave it empty.
	RequiresPythonRaw        string `json:"requires_python_raw,omitempty"`
	RequiresPythonUnreadable bool   `json:"requires_python_unreadable,omitempty"`

	RequiresDist  []string `json:"requires_dist"`
	ProvidesExtra []string `json:"provides_extra,omitempty"`
}

func runDeps(w io.Writer, args []string) error {
	fs, g := newFlagSet("deps")
	fs.Usage = func() {}
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, printErr := fmt.Fprint(w, depsHelp)
			return printErr
		}
		return usageErrorf("deps: %v", err)
	}

	var pkgArg, verArg string
	switch fs.NArg() {
	case 1:
		pkgArg = fs.Arg(0)
	case 2:
		pkgArg, verArg = fs.Arg(0), fs.Arg(1)
	default:
		return usageErrorf("deps: expected a package name and an optional version, got %v", fs.Args())
	}

	path, err := resolveRSFPath(g.rsfPath)
	if err != nil {
		return err
	}

	return depsCmd(w, path, g.json, pkgArg, verArg)
}

// depsCmd is the testable core. verArg is the empty string when the caller
// omitted the version, meaning "select a version per PEP 440's default" — see
// selectHighest, which is the highest one that is not a pre-release.
func depsCmd(w io.Writer, path string, jsonOut bool, pkgArg, verArg string) error {
	file, idx, err := openRSF(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	ctx := context.Background()
	pkg := index.NewPackageName(pkgArg)

	ver, err := resolveVersion(ctx, idx, pkg, pkgArg, verArg)
	if err != nil {
		return err
	}

	meta, err := idx.Metadata(ctx, pkg, ver)
	if err != nil {
		if errors.Is(err, index.ErrPackageNotFound) {
			return notFoundErrorf("package %q not found in this RSF", pkgArg)
		}
		if errors.Is(err, index.ErrMetadataUnavailable) {
			return notFoundErrorf("package %q has no captured dependency metadata for version %s in this RSF", pkgArg, ver)
		}
		if errors.Is(err, index.ErrMetadataUnusable) {
			// The record is present and does not conform. Distinct from
			// ErrMetadataUnavailable above, where nothing was captured at all.
			// The wrapped error names the offending string, so it is passed
			// through rather than summarized.
			return unusableErrorf("package %q version %s has dependency metadata that does not conform: %v", pkgArg, ver, err)
		}
		return usageErrorf("deps: %v", err)
	}

	reqs := make([]string, len(meta.RequiresDist))
	for i, r := range meta.RequiresDist {
		reqs[i] = r.String()
	}

	result := depsResult{
		Package:                  pkg.String(),
		Version:                  ver.String(),
		RequiresPython:           meta.RequiresPython.String(),
		RequiresPythonUnreadable: meta.RequiresPythonUnreadable,
		RequiresDist:             reqs,
		ProvidesExtra:            meta.ProvidesExtra,
	}
	if meta.RequiresPythonUnreadable {
		result.RequiresPythonRaw = meta.RequiresPythonRaw
	}

	if jsonOut {
		return writeJSON(w, result)
	}

	ew := &errWriter{w: w}
	ew.printf("%s %s\n", result.Package, result.Version)
	// Three states, not two. "(unconstrained)" was printed for both an absent
	// constraint and an unreadable one, which asserts the publisher declared
	// nothing when the publisher declared something we could not read -- and
	// hides that this version is being admitted for every interpreter by
	// fallback rather than by declaration.
	switch {
	case result.RequiresPythonUnreadable:
		ew.printf("Requires-Python: (unreadable: %q, treated as unconstrained)\n",
			result.RequiresPythonRaw)
	case result.RequiresPython != "":
		ew.printf("Requires-Python: %s\n", result.RequiresPython)
	default:
		ew.println("Requires-Python: (unconstrained)")
	}
	if len(result.RequiresDist) == 0 {
		ew.println("Requires-Dist: (none)")
	} else {
		ew.println("Requires-Dist:")
		for _, r := range result.RequiresDist {
			ew.printf("  %s\n", r)
		}
	}
	if len(result.ProvidesExtra) == 0 {
		ew.println("Provides-Extra: (none)")
	} else {
		ew.printf("Provides-Extra: %s\n", strings.Join(result.ProvidesExtra, ", "))
	}
	return ew.err
}

// resolveVersion returns ver parsed from verArg, or -- when verArg is empty
// -- the highest PEP 440 version with captured dependency metadata for pkg.
func resolveVersion(ctx context.Context, idx *index.RSFIndex, pkg index.PackageName, pkgArg, verArg string) (version.Version, error) {
	if verArg != "" {
		v, err := version.Parse(verArg)
		if err != nil {
			return version.Version{}, usageErrorf("invalid version %q: %v", verArg, err)
		}
		return v, nil
	}

	vers, err := idx.Versions(ctx, pkg)
	if err != nil {
		if errors.Is(err, index.ErrPackageNotFound) {
			return version.Version{}, notFoundErrorf("package %q not found in this RSF", pkgArg)
		}
		return version.Version{}, usageErrorf("deps: %v", err)
	}
	if len(vers) == 0 {
		return version.Version{}, notFoundErrorf("package %q has no captured dependency versions in this RSF", pkgArg)
	}

	// Not simply the highest: PEP 440 excludes pre-releases from selection
	// unless nothing else is available. See selectHighest.
	v, ok := selectHighest(vers)
	if !ok {
		return version.Version{}, notFoundErrorf("package %q has no captured dependency versions in this RSF", pkgArg)
	}
	return v, nil
}
