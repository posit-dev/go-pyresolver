// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/posit-dev/go-python-packaging/version"

	"github.com/posit-dev/go-pyresolver/index"
)

const versionsHelp = `Usage: pyresolve versions --rsf <path> <package> [--json]

List every version of <package> that has captured dependency metadata in
the RSF, sorted ascending by PEP 440 version ordering.

Flags:
  --rsf <path>   path to the RSF file (or set PYRESOLVE_RSF)
  --json         emit JSON instead of text

` + noNetworkNotice + "\n"

// versionsResult is the versions command's output shape.
type versionsResult struct {
	Package  string   `json:"package"`
	Versions []string `json:"versions"`
	Count    int      `json:"count"`

	// UnparseableKeys lists stored version keys this RSF holds for the package
	// that PEP 440 rejects, so they cannot be reported as versions.
	//
	// Reported because an empty Versions list has two very different causes and
	// conflating them sends someone hunting for data that is present. A package
	// whose every key is rejected HAS dependency metadata; it is recorded under a
	// string the specification does not accept.
	UnparseableKeys []string `json:"unparseable_keys,omitempty"`
}

func runVersions(w io.Writer, args []string) error {
	fs, g := newFlagSet("versions")
	fs.Usage = func() {}
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, printErr := fmt.Fprint(w, versionsHelp)
			return printErr
		}
		return usageErrorf("versions: %v", err)
	}
	if fs.NArg() != 1 {
		return usageErrorf("versions: expected exactly one package name argument, got %v", fs.Args())
	}

	path, err := resolveRSFPath(g.rsfPath)
	if err != nil {
		return err
	}

	return versionsCmd(w, path, g.json, fs.Arg(0))
}

// versionsCmd is the testable core.
func versionsCmd(w io.Writer, path string, jsonOut bool, pkgArg string) error {
	file, idx, err := openRSF(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	pkg := index.NewPackageName(pkgArg)
	vers, err := idx.Versions(context.Background(), pkg)
	if err != nil {
		if errors.Is(err, index.ErrPackageNotFound) {
			return notFoundErrorf("package %q not found in this RSF", pkgArg)
		}
		return usageErrorf("versions: %v", err)
	}

	sort.Sort(version.SortedVersions(vers))

	strs := make([]string, len(vers))
	for i, v := range vers {
		strs[i] = v.String()
	}

	// Only asked for when there is something to explain: a non-empty version list
	// needs no caveat, and this walks the package's keys a second time.
	var unparseable []string
	if len(strs) == 0 {
		unparseable, err = idx.UnparseableVersionKeys(context.Background(), pkg)
		if err != nil {
			return usageErrorf("versions: %v", err)
		}
	}

	result := versionsResult{
		Package:         pkg.String(),
		Versions:        strs,
		Count:           len(strs),
		UnparseableKeys: unparseable,
	}

	if jsonOut {
		return writeJSON(w, result)
	}

	ew := &errWriter{w: w}
	if result.Count == 0 {
		if len(result.UnparseableKeys) > 0 {
			// The data IS here. Saying otherwise sends the reader looking for
			// something missing when the problem is the spelling of the key.
			ew.printf("%s: %d version key(s) present in this RSF, but none is a valid "+
				"PEP 440 version, so no versions can be reported: %v\n",
				result.Package, len(result.UnparseableKeys), result.UnparseableKeys)
			return ew.err
		}
		ew.printf("%s: no versions with captured dependency data in this RSF\n", result.Package)
		return ew.err
	}
	for _, v := range result.Versions {
		ew.println(v)
	}
	ew.printf("%d version(s)\n", result.Count)
	return ew.err
}
