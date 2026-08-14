// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// TestInRangeIsNotContiguous measures whether the admitted versions of a package
// form a CONTIGUOUS run of a version-ordered list.
//
// # What this decides
//
// If they did, Candidates could locate them with two binary searches per span
// instead of testing every version -- which is worth knowing, because building
// the probe bound for one version is now the single largest allocator in a
// resolution (37.8% of allocations on the benchmark's app-set entry, all of it
// pep440set.atBound reached from Set.Contains).
//
// Two things could break contiguity, and they are different in kind:
//
//   - an allowed set with several spans, which is a fact about the SOLVER's
//     state and is handled by searching once per span; and
//   - pre-release admission, which is a fact about the VERSIONS and is not,
//     because a pre-release sits between two finals in version order, so
//     removing it splits a run.
//
// This measures the second, which is the one that decides the question. It is
// reported rather than asserted: the answer is a property of published data and
// this test would be wrong to fail when that data changes.
func TestInRangeIsNotContiguous(t *testing.T) {
	path := os.Getenv("PYPIRSF_TEST_FILE")
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("expand home: %v", err)
		}
		path = filepath.Join(home, path[2:])
	}
	if path == "" {
		path = "../index/testdata/pypi-trimmed.rsf"
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()

	idx, err := index.NewRSFIndex(file, "production")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}

	names := file.Packages()
	sort.Strings(names)

	limit := 0
	if s := os.Getenv("GPR_SAMPLE"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil {
			t.Fatalf("GPR_SAMPLE=%q: %v", s, err)
		}
	}
	sampled := names
	if limit > 0 && limit < len(names) {
		rng := rand.New(rand.NewSource(1))
		sampled = make([]string, 0, limit)
		for len(sampled) < limit {
			sampled = append(sampled, names[rng.Intn(len(names))])
		}
	}

	// No package is enabled for pre-releases, which is the default a resolution
	// gets unless a requirement asks for one.
	var pre candidate.PrereleaseSet

	ctx := context.Background()
	var (
		packages       int
		versions       int
		preReleases    int
		withPre        int
		splitByPre     int
		allPreReleases int
	)
	for _, name := range sampled {
		pkg := index.PackageName(name)
		all, err := idx.Versions(ctx, pkg)
		if err != nil || len(all) == 0 {
			continue
		}
		packages++
		versions += len(all)

		// admitted[i] is whether all[i] survives pre-release admission. The list
		// is in version order, so a false between two trues is a SPLIT: the
		// admitted set is not one contiguous run and no pair of binary searches
		// can delimit it.
		var (
			seenAdmitted bool
			gapAfter     bool
			split        bool
			nPre         int
		)
		for _, v := range all {
			if !pre.Admits(pkg, v) {
				nPre++
				if seenAdmitted {
					gapAfter = true
				}
				continue
			}
			if gapAfter {
				split = true
			}
			seenAdmitted = true
		}
		preReleases += nPre
		if nPre > 0 {
			withPre++
		}
		if nPre == len(all) {
			allPreReleases++
		}
		if split {
			splitByPre++
		}
	}

	pct := func(a, b int) float64 {
		if b == 0 {
			return 0
		}
		return 100 * float64(a) / float64(b)
	}
	t.Logf("snapshot %s", path)
	t.Logf("  %d packages, %d versions", packages, versions)
	t.Logf("  %d versions are pre-releases (%.2f%%)", preReleases, pct(preReleases, versions))
	t.Logf("  %d packages publish at least one pre-release (%.2f%%)", withPre, pct(withPre, packages))
	t.Logf("  %d packages publish ONLY pre-releases (%.2f%%)", allPreReleases, pct(allPreReleases, packages))
	t.Logf("  %d packages have their admitted versions SPLIT by a pre-release (%.2f%%) "+
		"— for these, no pair of binary searches over a version-ordered list can "+
		"delimit the admitted set", splitByPre, pct(splitByPre, packages))

	if packages == 0 {
		t.Fatal("measured nothing")
	}
	// ⚠️ splitByPre is the number the whole binary-search decision turns on, so it
	// is asserted rather than merely logged. A corpus where nothing is split
	// would say the intersection COULD be two binary searches, which is a
	// different engineering answer -- and it would say it silently, since every
	// other line here is a t.Logf.
	//
	// The assertion is "at least one", not a threshold: the exact rate is a
	// property of published data and will drift, but a corpus with zero splits
	// means this test has stopped measuring what it is named for.
	if splitByPre == 0 {
		t.Error("no sampled package had its admitted versions split by a pre-release, so " +
			"this measured nothing about contiguity; on a full snapshot the rate is " +
			"around 4% of packages")
	}
}

// versionsAreAscending is the check behind the claim candidate.Rank's fast path
// is built on: that index.RSFIndex hands back versions in ascending order.
//
// ⚠️ It is a check on the IMPLEMENTATION, not on the contract.
// index.MetadataIndex explicitly promises no ordering, which is why Rank detects
// the shape rather than assuming it. This exists so that if RSFIndex ever stops
// being sorted, the reason the fast path went quiet is discoverable rather than
// mysterious.
func TestRSFIndexVersionsAreAscending(t *testing.T) {
	path := os.Getenv("PYPIRSF_TEST_FILE")
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("expand home: %v", err)
		}
		path = filepath.Join(home, path[2:])
	}
	if path == "" {
		path = "../index/testdata/pypi-trimmed.rsf"
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()

	idx, err := index.NewRSFIndex(file, "production")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}

	names := file.Packages()
	sort.Strings(names)

	limit := 0
	if s := os.Getenv("GPR_SAMPLE"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil {
			t.Fatalf("GPR_SAMPLE=%q: %v", s, err)
		}
	}
	sampled := names
	if limit > 0 && limit < len(names) {
		rng := rand.New(rand.NewSource(1))
		sampled = make([]string, 0, limit)
		for len(sampled) < limit {
			sampled = append(sampled, names[rng.Intn(len(names))])
		}
	}

	ctx := context.Background()
	var checked, inversions, equalAdjacent int
	for _, name := range sampled {
		all, err := idx.Versions(ctx, index.PackageName(name))
		if err != nil || len(all) < 2 {
			continue
		}
		checked++
		for i := 1; i < len(all); i++ {
			switch c := all[i].Compare(all[i-1]); {
			case c < 0:
				inversions++
			case c == 0:
				// Would mean dedup let two members of one PEP 440 equality
				// class through, which is a different bug entirely.
				equalAdjacent++
			}
		}
	}

	t.Logf("%d multi-version packages: %d adjacent inversions, %d adjacent equal pairs",
		checked, inversions, equalAdjacent)
	if checked == 0 {
		t.Fatal("checked nothing")
	}
	if inversions != 0 {
		t.Errorf("%d adjacent inversions: RSFIndex.Versions is no longer ascending, so "+
			"candidate.Rank's reversed fast path will stop firing and resolution will "+
			"get slower without getting wrong", inversions)
	}
	if equalAdjacent != 0 {
		t.Errorf("%d adjacent PEP 440-equal pairs: Versions is supposed to collapse each "+
			"equality class to one representative", equalAdjacent)
	}
}
