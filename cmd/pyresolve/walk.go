// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
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

A requirement pinned to a URL ("name @ url") is reported rather than followed.
That form names a specific distribution, so the name is a local label for it and
not a package in this RSF.

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
	Root  string `json:"root"`
	Depth int    `json:"depth"`
	Note  string `json:"note"`

	// Packages lists the names reached that HAVE a record in this RSF, and Count
	// is its length. Names in Absent are excluded: they are referenced by some
	// dependency but are not in this file, so calling them reachable packages
	// both double-reports them and inflates Count with things that cannot be
	// installed from here.
	//
	// Names in NoDependencyData and UnusableMetadata ARE included. Those
	// packages exist in this RSF; the walk simply could not expand them.
	Packages []string `json:"packages"`
	Count    int      `json:"count"`

	// SelectedVersions maps each package to the one version this walk used for
	// it, PEP 440-normalized.
	//
	// The walk takes ONE version of each package and its shape depends on which,
	// so without this a reader cannot tell which version produced any edge, and
	// two walks over different snapshots print identically while describing
	// different graphs.
	//
	// ⚠️ PARTIAL BY DESIGN. A package appears here only if a version was
	// selected for it, so names under Absent (no record) and NoDependencyData
	// (nothing captured, or nothing selectable) are missing. Do not treat a
	// missing entry as "version unknown" -- it means no version was chosen, and
	// the category lists say why. Names under UnusableMetadata DO appear: a
	// version was selected and its metadata then failed to parse.
	//
	// A map rather than turning Packages into objects, because Packages is
	// published JSON and changing its element type would break consumers.
	SelectedVersions map[string]string `json:"selected_versions,omitempty"`

	// Absent lists names referenced by some dependency but having no record in
	// this RSF at all. Disjoint from Packages.
	Absent []string `json:"absent,omitempty"`

	// NoDependencyData lists names present in this RSF for which no usable
	// dependency metadata was captured, so the walk could not continue through
	// them.
	NoDependencyData []string `json:"no_dependency_data,omitempty"`

	// DirectURLRequirements lists requirements pinned to a URL with PEP 508's
	// "name @ url" form, which the walk does not follow.
	//
	// Recorded as whole requirement strings rather than names, because the NAME IS
	// NOT AN IDENTITY HERE. It is a local label for whatever the URL provides, so
	// reporting "clip" would suggest a PyPI package that the requirement has
	// nothing to do with. The URL is the only identifying part.
	DirectURLRequirements []string `json:"direct_url_requirements,omitempty"`

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
	selected := map[index.PackageName]string{}
	var absent, noDeps, unusable []index.PackageName
	var directURL []string
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
		// Not simply the highest: PEP 440 excludes pre-releases from selection
		// unless nothing else is available. See selectHighest.
		highest, ok := selectHighest(vers)
		if !ok {
			noDeps = append(noDeps, item.name)
			continue
		}
		// Recorded for every package we select a version for, not only the ones
		// we go on to expand. The walk takes ONE version of each package and its
		// answer depends on which -- without this, a reader could not tell which
		// version produced any edge, and two runs over different snapshots looked
		// identical while walking different graphs.
		selected[item.name] = highest.String()

		// ⚠️ Selection deliberately happens BEFORE this check, where it used to
		// happen after. At the depth cutoff we already hold the version list, so
		// selecting costs nothing and lets every reachable package report its
		// version instead of only those within the cutoff -- with --depth 1 that
		// was almost none of them.
		//
		// One behavior change falls out: a package AT the cutoff whose selection
		// fails is now classified as having no usable version, where before the
		// cutoff returned first and it was reported as plainly reachable. That is
		// the more honest answer -- having no selectable version is a fact about
		// the package, not about how deep the walk happened to go.
		if item.depth >= maxDepth {
			// Reachable, but we stop expanding it further.
			continue
		}

		meta, err := idx.Metadata(ctx, item.name, highest)
		if err != nil {
			if errors.Is(err, index.ErrMetadataUnavailable) {
				// The package and version exist, but this version's metadata
				// was not captured — same practical outcome as no versions at
				// all, and equally not an absent package.
				//
				// ⚠️ THIS BRANCH IS UNREACHABLE TODAY, AND IS KEPT DELIBERATELY.
				// Mutation testing across 8 real walks could not reach it, which
				// looks like dead code worth deleting. It is not:
				//
				//   - It is unreachable only for *index.RSFIndex, which is the
				//     concrete type this command holds. RSFIndex builds Versions
				//     and Metadata from the same decoded map, and selectHighest
				//     only ever returns a member of what Versions just returned,
				//     so Metadata always finds a record. index/dedupe_test.go's
				//     TestVersionsAndMetadataAgreeOnEveryVersion pins exactly
				//     that invariant, so this is a guaranteed property of one
				//     implementation rather than an accident.
				//   - The MetadataIndex CONTRACT (index/index.go) permits
				//     ErrMetadataUnavailable for a known version whose metadata
				//     would need a build (sdist-only), and index.MockIndex
				//     already returns it. Any index that is not backed purely by
				//     an RSF -- a JSON or DB index -- will reach this.
				//
				// So deleting it would trade dead code for a latent gap that
				// reappears the moment walkCmd is generalized to the interface,
				// and the failure would be a silently wrong walk rather than a
				// compile error.
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
			if req.URL != "" {
				// ⚠️ A DIRECT REFERENCE IS A DIFFERENT PACKAGE FROM THE INDEX ENTRY
				// OF THE SAME NAME, and following the name substitutes an unrelated
				// project for the one the publisher asked for.
				//
				// PEP 508's "name @ url" form pins the distribution to that URL. The
				// name is a local label for it, not a lookup key: `memery` requires
				// "clip @ git+https://github.com/openai/CLIP@main", which is OpenAI's
				// CLIP and has nothing to do with the "clip" on PyPI. Walking into
				// the index by name reported that unrelated project as reachable,
				// and would have reported ITS dependencies as reachable too.
				//
				// Reported as its own category rather than traversed. This RSF holds
				// PyPI, so a direct reference is by definition not in it, and
				// resolving one would mean fetching the URL — which this tool
				// deliberately never does.
				directURL = append(directURL, req.String())
				continue
			}
			depName := index.NewPackageName(req.Name)
			if visited[depName] {
				continue
			}
			visited[depName] = true
			queue = append(queue, queueItem{depName, item.depth + 1})
		}
	}

	// A name is added to `visited` when its EDGE is discovered, before anything
	// is known about whether it exists. Names that turn out to have no record in
	// this RSF are therefore removed here, or the same name is reported twice
	// with contradictory meanings: once under Packages ("reachable") and once
	// under Absent ("no record in this RSF"). The trailing "N package(s)
	// reachable" counted them too, inflating the total with names that cannot be
	// installed from this file.
	//
	// ⚠️ Only ABSENT names are removed. NoDependencyData and UnusableMetadata
	// names DO have records here -- they are real packages that the walk merely
	// could not expand, one because nothing was captured and one because what
	// was captured does not conform. Dropping those would understate the closure
	// and lose the distinction between "not in this file" and "in this file but
	// unreadable", which is the collapse this command keeps having to fix. The
	// finding this fixes (rstudio/package-manager#19466 F10) grouped absent and
	// uncaptured together; they are not the same, and only the first is wrong.
	absentSet := make(map[index.PackageName]bool, len(absent))
	for _, n := range absent {
		absentSet[n] = true
	}

	names := make([]string, 0, len(visited))
	for n := range visited {
		if absentSet[n] {
			continue
		}
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

	// Deduplicated: the same direct reference can be reached from several
	// packages, and listing it repeatedly says nothing extra.
	sort.Strings(directURL)
	directURL = slices.Compact(directURL)

	// Keyed by the reported name, and only for packages that made it into
	// Packages, so the map cannot disagree with the list beside it.
	selectedVersions := make(map[string]string, len(names))
	for _, n := range names {
		if v, ok := selected[index.NewPackageName(n)]; ok {
			selectedVersions[n] = v
		}
	}

	result := walkResult{
		Root:                  root.String(),
		Depth:                 maxDepth,
		Note:                  walkNotResolverNotice,
		Packages:              names,
		Count:                 len(names),
		SelectedVersions:      selectedVersions,
		Absent:                absentStrs,
		NoDependencyData:      noDepsStrs,
		UnusableMetadata:      unusableStrs,
		DirectURLRequirements: directURL,
	}

	if jsonOut {
		return writeJSON(w, result)
	}

	ew := &errWriter{w: w}
	ew.println(walkNotResolverNotice)
	ew.printf("\nWalking %s (max depth %d):\n\n", result.Root, result.Depth)
	for _, n := range result.Packages {
		// A package with no selected version prints bare rather than with a
		// placeholder: the category lists below say why it has none, and
		// inventing "(unknown)" here would suggest the version is a mystery
		// rather than that none was chosen.
		if v, ok := result.SelectedVersions[n]; ok {
			ew.printf("%s %s\n", n, v)
		} else {
			ew.println(n)
		}
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
	if len(result.DirectURLRequirements) > 0 {
		ew.printf("\n%d requirement(s) pinned to a URL, which name a specific distribution "+
			"rather than a package in this RSF and were not followed: %v\n",
			len(result.DirectURLRequirements), result.DirectURLRequirements)
	}
	return ew.err
}
