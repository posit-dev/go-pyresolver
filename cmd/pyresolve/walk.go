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

	"github.com/posit-dev/go-pyresolver/index"
)

// walkNotResolverNotice must appear in both --help and in every invocation's
// output: walk does not solve version constraints. It is a graph traversal
// over one selected version per package, nothing more, and must never be
// mistaken for the output of an actual resolver.
const walkNotResolverNotice = "walk is NOT dependency resolution: it does not solve version constraints. " +
	"It takes one version of each package -- the highest that is not a pre-release, per PEP 440's default -- " +
	"and follows its Requires-Dist entries as graph edges, " +
	"ignoring environment markers, extras conditions, and version specifiers."

const defaultWalkDepth = 3

var walkHelp = `Usage: pyresolve walk --rsf <path> <package> [--depth N] [--json]

Breadth-first walk of the dependency graph reachable from <package>: at each
step it selects one version of a package -- the highest that is not a
pre-release, per PEP 440's default -- and follows all of its Requires-Dist
entries as edges. It prints the reachable package names and a count.

Packages it could not expand are reported separately rather than aborting the
walk: those absent from the file, those with no captured dependency data, and
those whose captured data does not conform to PEP 508.

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

	// UnusableMetadata lists names whose selected version HAS dependency
	// metadata that does not conform to the specification -- in practice a
	// Requires-Dist entry PEP 508 rejects -- so the walk could not expand them.
	//
	// Kept distinct from NoDependencyData on purpose. "Nothing was captured" and
	// "what was captured does not conform" are different facts about the data and
	// call for different follow-up, and collapsing distinct states into one
	// report is a mistake this command has already made twice.
	UnusableMetadata []string `json:"unusable_metadata,omitempty"`
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
	var absent, noDeps, unusable []index.PackageName
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

		// Not simply the highest: PEP 440 excludes pre-releases from selection
		// unless nothing else is available. See selectHighest.
		highest, ok := selectHighest(vers)
		if !ok {
			noDeps = append(noDeps, item.name)
			continue
		}

		meta, err := idx.Metadata(ctx, item.name, highest)
		if err != nil {
			if errors.Is(err, index.ErrMetadataUnavailable) {
				// The package and version exist, but this version's metadata
				// was not captured — same practical outcome as no versions at
				// all, and equally not an absent package.
				noDeps = append(noDeps, item.name)
				continue
			}
			if errors.Is(err, index.ErrMetadataUnusable) {
				// The metadata is present but something in it does not conform,
				// so this version cannot be expanded. Reported as its own
				// category and the walk continues.
				//
				// ⚠️ It used to abort the entire traversal. On a production
				// snapshot that cost 507 root packages their whole walk over one
				// malformed entry reached transitively — `walk apache-airflow`
				// discarded 200+ already-resolved packages because a provider
				// package several hops away carried "apache-airflow (>=2.3.0.*)".
				// It also surfaced as exit status 1, which this CLI documents as
				// "usage or file error", so a legitimate data condition looked
				// like the user had done something wrong.
				//
				// Not folded into noDeps: "we captured nothing" and "what we
				// captured does not conform" are different facts about the data,
				// and collapsing distinct states into one report is a mistake this
				// command has already made twice.
				unusable = append(unusable, item.name)
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

	unusableStrs := make([]string, 0, len(unusable))
	for _, n := range unusable {
		unusableStrs = append(unusableStrs, n.String())
	}
	sort.Strings(unusableStrs)

	result := walkResult{
		Root:             root.String(),
		Depth:            maxDepth,
		Note:             walkNotResolverNotice,
		Packages:         names,
		Count:            len(names),
		Absent:           absentStrs,
		NoDependencyData: noDepsStrs,
		UnusableMetadata: unusableStrs,
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
	if len(result.UnusableMetadata) > 0 {
		ew.printf("\n%d present in this RSF with dependency metadata that does not conform to "+
			"PEP 508, so the walk stopped there: %v\n",
			len(result.UnusableMetadata), result.UnusableMetadata)
	}
	return ew.err
}
