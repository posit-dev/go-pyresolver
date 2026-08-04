// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/posit-dev/go-python-packaging/version"

	"github.com/posit-dev/go-pyresolver/index"
)

// walkNotResolverNotice must appear in both --help and in every invocation's
// output: walk does not solve version constraints. It is a graph traversal
// over "highest captured version of each package", nothing more, and must
// never be mistaken for the output of an actual resolver.
const walkNotResolverNotice = "walk is NOT dependency resolution: it does not solve version constraints. " +
	"It takes the highest captured version of each package and follows its Requires-Dist entries as graph edges, " +
	"ignoring environment markers, extras conditions, and version specifiers."

const defaultWalkDepth = 3

var walkHelp = `Usage: pyresolve walk --rsf <path> <package> [--depth N] [--json]

Breadth-first walk of the dependency graph reachable from <package>: at each
step it takes the HIGHEST captured version of a package and follows all of
its Requires-Dist entries as edges. It prints the reachable package names
and a count.

` + walkNotResolverNotice + `

Flags:
  --rsf <path>   path to the RSF file (or set PYRESOLVE_RSF)
  --depth N      maximum number of edges to follow from <package> (default ` + strconv.Itoa(defaultWalkDepth) + `)
  --json         emit JSON instead of text

` + noNetworkNotice + "\n"

// walkResult is the walk command's output shape.
//
// Absent and NoDependencyData are kept apart on purpose. "This RSF has never
// heard of the package" and "this RSF has the package but captured no usable
// dependency data for it" are different facts, and reporting the second as the
// first sends someone hunting for a typo in a name that is present and correct.
// The second case is common and expected: a package with no built distribution
// has no captured dependency metadata.
type walkResult struct {
	Root     string   `json:"root"`
	Depth    int      `json:"depth"`
	Note     string   `json:"note"`
	Packages []string `json:"packages"`
	Count    int      `json:"count"`

	// Absent lists names referenced by some dependency but having no record in
	// this RSF at all.
	Absent []string `json:"absent,omitempty"`

	// NoDependencyData lists names present in this RSF for which no usable
	// dependency metadata was captured, so the walk could not continue through
	// them.
	NoDependencyData []string `json:"no_dependency_data,omitempty"`
}

func runWalk(w io.Writer, args []string) error {
	fs, g := newFlagSet("walk")
	fs.Usage = func() {}
	depth := fs.Int("depth", defaultWalkDepth, "maximum number of edges to follow")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, printErr := fmt.Fprint(w, walkHelp)
			return printErr
		}
		return usageErrorf("walk: %v", err)
	}
	if fs.NArg() != 1 {
		return usageErrorf("walk: expected exactly one package name argument, got %v", fs.Args())
	}
	if *depth < 0 {
		return usageErrorf("walk: --depth must be >= 0, got %d", *depth)
	}

	path, err := resolveRSFPath(g.rsfPath)
	if err != nil {
		return err
	}

	return walkCmd(w, path, g.json, fs.Arg(0), *depth)
}

// walkCmd is the testable core.
func walkCmd(w io.Writer, path string, jsonOut bool, rootArg string, maxDepth int) error {
	file, idx, err := openRSF(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	ctx := context.Background()
	root := index.NewPackageName(rootArg)

	rootVers, verErr := idx.Versions(ctx, root)
	if verErr != nil {
		if errors.Is(verErr, index.ErrPackageNotFound) {
			return notFoundErrorf("package %q not found in this RSF", rootArg)
		}
		return usageErrorf("walk: %v", verErr)
	}
	if len(rootVers) == 0 {
		return notFoundErrorf("package %q has no captured dependency versions in this RSF", rootArg)
	}

	type queueItem struct {
		name  index.PackageName
		depth int
	}

	visited := map[index.PackageName]bool{root: true}
	var absent, noDeps []index.PackageName
	queue := []queueItem{{root, 0}}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		vers, err := idx.Versions(ctx, item.name)
		if err != nil {
			if errors.Is(err, index.ErrPackageNotFound) {
				absent = append(absent, item.name)
				continue
			}
			return usageErrorf("walk: %v", err)
		}
		if len(vers) == 0 {
			// Present in the file, but nothing captured. Distinct from absent.
			noDeps = append(noDeps, item.name)
			continue
		}
		if item.depth >= maxDepth {
			// Reachable, but we stop expanding it further.
			continue
		}

		sort.Sort(version.SortedVersions(vers))
		highest := vers[len(vers)-1]

		meta, err := idx.Metadata(ctx, item.name, highest)
		if err != nil {
			if errors.Is(err, index.ErrMetadataUnavailable) {
				// The package and version exist, but this version's metadata
				// was not captured — same practical outcome as no versions at
				// all, and equally not an absent package.
				noDeps = append(noDeps, item.name)
				continue
			}
			return usageErrorf("walk: %v", err)
		}

		for _, req := range meta.RequiresDist {
			depName := index.NewPackageName(req.Name)
			if visited[depName] {
				continue
			}
			visited[depName] = true
			queue = append(queue, queueItem{depName, item.depth + 1})
		}
	}

	names := make([]string, 0, len(visited))
	for n := range visited {
		names = append(names, n.String())
	}
	sort.Strings(names)

	absentStrs := make([]string, 0, len(absent))
	for _, n := range absent {
		absentStrs = append(absentStrs, n.String())
	}
	sort.Strings(absentStrs)

	noDepsStrs := make([]string, 0, len(noDeps))
	for _, n := range noDeps {
		noDepsStrs = append(noDepsStrs, n.String())
	}
	sort.Strings(noDepsStrs)

	result := walkResult{
		Root:             root.String(),
		Depth:            maxDepth,
		Note:             walkNotResolverNotice,
		Packages:         names,
		Count:            len(names),
		Absent:           absentStrs,
		NoDependencyData: noDepsStrs,
	}

	if jsonOut {
		return writeJSON(w, result)
	}

	ew := &errWriter{w: w}
	ew.println(walkNotResolverNotice)
	ew.printf("\nWalking %s (max depth %d):\n\n", result.Root, result.Depth)
	for _, n := range result.Packages {
		ew.println(n)
	}
	ew.printf("\n%d package(s) reachable\n", result.Count)
	if len(result.Absent) > 0 {
		ew.printf("\n%d referenced but absent from this RSF: %v\n", len(result.Absent), result.Absent)
	}
	if len(result.NoDependencyData) > 0 {
		ew.printf("\n%d present in this RSF but with no captured dependency data, so the walk "+
			"stopped there: %v\n", len(result.NoDependencyData), result.NoDependencyData)
	}
	return ew.err
}
