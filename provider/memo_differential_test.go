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

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-pyresolver/pypirsf"
	"github.com/posit-dev/go-python-packaging/version"
)

// TestCandidatesAgreeAcrossRepEATedCallsWithDifferentRanges is the differential
// for the ranked-list memo, and it exercises the thing
// TestCandidatesAgreeWithAnExactCountOnTheRealIndex structurally cannot.
//
// That test asks each package ONCE, always with pep440set.All(). The memo only
// does anything on the SECOND and later call for a package, and its correctness
// claim is specifically about calls with DIFFERENT allowed sets: the ranked full
// list is computed once and every call filters it, in place of each call ranking
// its own in-range list. A test that only ever passes All() would leave that
// entirely unchecked.
//
// So this asks each package many times, with ranges derived from its own
// published versions, against ONE provider whose memo is therefore warm from the
// second call on -- and compares every answer to a reference that re-reads the
// index and sorts from scratch each time.
//
// ⚠️ The reference is a FRESH provider per call. Sharing one would let the
// reference accumulate the same state the implementation does, which is how a
// differential quietly stops differentiating.
func TestCandidatesAgreeAcrossRepeatedCallsWithDifferentRanges(t *testing.T) {
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

	// ⚠️ A DEFAULT CAP, unlike the sibling differentials, and it is not timidity.
	//
	// This test asks each package up to eight times and builds a fixture per
	// package, so its cost per package is ~8x theirs. Left uncapped and pointed at
	// a full snapshot -- which is the obvious way to run it, and what its own doc
	// invited -- it swept 680,711 packages and ran for 34 minutes without reaching
	// an assertion, failing the package on the 40-minute limit. A knob whose
	// documented use hangs is worse than no knob.
	//
	// 20,000 is well under a minute against a production snapshot and comfortably
	// more than enough: the assertions are about a per-package memo, so coverage
	// grows with distinct packages and saturates early. GPR_SAMPLE=0 still means
	// "all", for anyone who wants it and has the time.
	//
	// ⚠️ Deliberately not a precise figure. Observed at 30s, 57s and 85s on the
	// same machine and the same snapshot -- the 85s reading predates the
	// specifier-built ranges below, and the rest is ambient load. Three different
	// numbers for "the same run" were briefly recorded in three places, which is
	// what a precise figure invites for something this contended.
	limit := 20000
	if s := os.Getenv("GPR_SAMPLE"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil {
			t.Fatalf("GPR_SAMPLE=%q: %v", s, err)
		}
	}
	seed := int64(1)
	if s := os.Getenv("GPR_SEED"); s != "" {
		if seed, err = strconv.ParseInt(s, 10, 64); err != nil {
			t.Fatalf("GPR_SEED=%q: %v", s, err)
		}
	}
	rng := rand.New(rand.NewSource(seed))
	sampled := names
	if limit > 0 && limit < len(names) {
		sampled = make([]string, 0, limit)
		for len(sampled) < limit {
			sampled = append(sampled, names[rng.Intn(len(names))])
		}
	}

	ctx := context.Background()

	// ⚠️ The provider reads through a COUNTING index, and that is the difference
	// between observing the memo and asserting arithmetic about this test's own
	// loop.
	//
	// An earlier version of this test reported "N calls served from a warm memo"
	// as calls-minus-packages. That number is a function of the loop shape and
	// nothing else: with the memo lookup neutered so every call missed, the test
	// still passed and still printed the same figure. It was quoted as evidence
	// in a changelog. Counting index calls is what actually distinguishes a memo
	// that is read from one that is merely written.
	counting := newCountingIndex(idx)
	p := provider.New(ctx, counting, testOptions(t))

	var (
		calls        int
		repeat       int
		available    int
		multiVersion int
	)
	distinct := map[index.PackageName]bool{}
	for _, name := range sampled {
		pkg := provider.Project(index.PackageName(name))

		// Read through idx, NOT through counting: this is the test setting up its
		// own ranges, not the provider doing its work, and counting it would
		// corrupt the very number the assertion below rests on.
		all, err := idx.Versions(ctx, index.PackageName(name))
		if err != nil || len(all) == 0 {
			continue
		}
		if len(all) > 1 {
			multiVersion++
		}
		distinct[index.PackageName(name)] = true

		for _, allowed := range rangesOver(t, rng, all) {
			gotBest, gotFound, gotRank, gotErr := p.Candidates(pkg, allowed)

			// A fresh reference provider on the UNCOUNTED index, so nothing it
			// computed on the previous range is reused and its reads do not
			// enter the count.
			ref := provider.New(ctx, idx, testOptions(t))
			wantBest, wantFound, wantCount, wantErr := ref.ExactCandidates(pkg, allowed)

			if gotErr != nil && wantErr == nil {
				t.Errorf("%s: the memoized walk errored (%v) where the exhaustive one did not",
					name, gotErr)
				continue
			}
			if gotErr != nil || wantErr != nil {
				continue
			}
			calls++

			if gotFound != wantFound {
				t.Errorf("%s over %v: found = %v, exact found = %v",
					name, allowed, gotFound, wantFound)
				continue
			}
			if !gotFound {
				continue
			}
			available++
			if !gotBest.Equal(wantBest) {
				t.Errorf("%s over %v: best = %v, exact best = %v — ranking the FULL list and "+
					"filtering after must pick the same version as ranking the in-range list",
					name, allowed, gotBest, wantBest)
			}
			if gotRank < wantCount {
				t.Errorf("%s over %v: rank = %d but %d versions are usable; rank must never under-count",
					name, allowed, gotRank, wantCount)
			}
		}
		repeat++
	}

	// The MEASURED numbers: how many times the provider actually reached the
	// index, against how many times it was asked.
	indexCalls := int(counting.versions.Load())

	t.Logf("snapshot %s: %d packages (%d with more than one version), %d Candidates calls, "+
		"%d with something available", path, repeat, multiVersion, calls, available)
	t.Logf("the provider made %d Versions() calls for %d distinct packages, so %d calls "+
		"(%.1f%%) were served from the memo",
		indexCalls, len(distinct), calls-indexCalls,
		100*float64(calls-indexCalls)/float64(max(calls, 1)))

	if calls == 0 {
		t.Fatal("made no comparable calls, so this would have passed vacuously")
	}
	if multiVersion == 0 {
		t.Error("every sampled package had a single version, so no range could narrow anything")
	}

	// ⚠️ THE ASSERTION THAT MAKES THIS A MEMO TEST.
	//
	// One index read per distinct package is exactly what the memo promises, and
	// it is not derivable from the loop: without the memo this is one read per
	// CALL, which is several times larger. Asserting equality rather than an
	// inequality is what makes it fail on a memo that is written and never read.
	//
	// ⚠️ Not <= either. Fewer reads than distinct packages would mean a package
	// was answered without ever being looked up, which is a different bug and
	// should not pass here.
	if indexCalls != len(distinct) {
		t.Errorf("the provider made %d Versions() calls for %d distinct packages; the memo "+
			"promises exactly one per package, and %d calls were made in total. A count "+
			"near the call count means the memo is being written and never read",
			indexCalls, len(distinct), calls)
	}

	// ⚠️ And the memo must actually be EXERCISED, not merely correct on a corpus
	// where every package was asked once. Without this the equality above is
	// satisfied trivially by calls == indexCalls == len(distinct).
	if calls <= indexCalls {
		t.Errorf("%d calls over %d index reads: no package was asked about more than once, "+
			"so the memo was never read", calls, indexCalls)
	}
}

// gappyLimit bounds the version-list size for which rangesOver builds a set by
// repeated Union.
//
// ⚠️ Repeated Union is QUADRATIC and it is this test's own fixture cost, not the
// cost of anything under test. Each Union re-canonicalizes a span list that has
// grown by one, so building a gappy set over n versions is O(n^2) cmpBound calls,
// and cmpBound reaches version.Compare, which in go-python-packaging v0.5.0
// renders a string (see rstudio/package-manager#19713).
//
// Unbounded, that wedged the whole package: pointed at a full snapshot with no
// sample cap, this test spent 34 minutes inside rangesOver on packages with
// hundreds of versions and never reached the assertions. A profile of the hang
// showed the stack in Union, not in Candidates. 64 keeps the gappy shape -- which
// is the one no pair of binary searches can express, and therefore the one worth
// having -- while making its cost irrelevant.
const gappyLimit = 64

// rangesOver builds allowed sets from a package's own published versions: the
// full range, then several narrowings anchored on real versions.
//
// Anchoring on real versions is what makes the narrowings discriminating. A range
// drawn from thin air mostly selects nothing, and a Candidates call that finds
// nothing agrees with the reference trivially.
func rangesOver(t *testing.T, rng *rand.Rand, all []version.Version) []pep440set.Set {
	t.Helper()

	out := []pep440set.Set{pep440set.All()}
	if len(all) == 0 {
		return out
	}

	// Exactly one version, drawn from anywhere in the list -- including the
	// OLDEST, which is the case where the memoized ranked list has to be walked
	// past every newer version to reach the answer.
	out = append(out, pep440set.Exactly(all[rng.Intn(len(all))]))
	out = append(out, pep440set.Exactly(all[0]))
	out = append(out, pep440set.Exactly(all[len(all)-1]))

	// Contiguous windows and an exclusion, built through specifiers so each is
	// one or two spans regardless of how many versions the package has. `!=` is
	// the interesting one: it is two spans, so it exercises a multi-span allowed
	// set without costing anything.
	pivot := all[rng.Intn(len(all))]
	for _, spec := range []string{">=" + pivot.String(), "<" + pivot.String(), "!=" + pivot.String()} {
		specs, err := version.NewSpecifiers(spec)
		if err != nil {
			// A published version whose canonical rendering is not a legal
			// specifier operand would be a fact worth knowing, not something to
			// skip past quietly.
			t.Fatalf("NewSpecifiers(%q): %v", spec, err)
		}
		s, err := pep440set.FromSpecifiers(specs)
		if err != nil {
			t.Fatalf("FromSpecifiers(%q): %v", spec, err)
		}
		out = append(out, s)
	}

	// A gappy set -- every other version -- which is the shape no contiguous span
	// can express and the one an "intersection is two binary searches"
	// optimization would have to handle. Only for short lists; see gappyLimit.
	if len(all) > 2 && len(all) <= gappyLimit {
		gappy := pep440set.Empty()
		for i := 0; i < len(all); i += 2 {
			gappy = gappy.Union(pep440set.Exactly(all[i]))
		}
		out = append(out, gappy)
	}
	return out
}
