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

	limit := 0
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
	// ONE provider for the whole run: its memo is warm for every package after
	// the first call, which is the state the change is about.
	p := provider.New(ctx, idx, testOptions(t))

	var (
		calls        int
		repeat       int
		available    int
		narrowed     int
		multiVersion int
	)
	for _, name := range sampled {
		pkg := provider.Project(index.PackageName(name))

		all, err := idx.Versions(ctx, index.PackageName(name))
		if err != nil || len(all) == 0 {
			continue
		}
		if len(all) > 1 {
			multiVersion++
		}

		for _, allowed := range rangesOver(rng, all) {
			gotBest, gotFound, gotRank, gotErr := p.Candidates(pkg, allowed)

			// A fresh reference provider, so nothing it computed on the previous
			// range is reused.
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

	// Every package after its first range answered from the memo; count the calls
	// that could only have come from it.
	narrowed = calls - repeat

	t.Logf("snapshot %s: %d packages (%d with more than one version), %d Candidates calls, "+
		"%d served after the package's first (memoized), %d with something available",
		path, repeat, multiVersion, calls, narrowed, available)

	if calls == 0 {
		t.Fatal("made no comparable calls, so this would have passed vacuously")
	}
	// ⚠️ The assertion that keeps this honest. If every package were asked once,
	// the memo would never be READ and this would be the previous test with extra
	// steps.
	if narrowed <= 0 {
		t.Errorf("no package was asked about more than once, so the memo was written and "+
			"never read; %d calls over %d packages", calls, repeat)
	}
	if multiVersion == 0 {
		t.Error("every sampled package had a single version, so no range could narrow anything")
	}
}

// rangesOver builds allowed sets from a package's own published versions: the
// full range, then several narrowings anchored on real versions.
//
// Anchoring on real versions is what makes the narrowings discriminating. A range
// drawn from thin air mostly selects nothing, and a Candidates call that finds
// nothing agrees with the reference trivially.
func rangesOver(rng *rand.Rand, all []version.Version) []pep440set.Set {
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

	// A half-open window: everything strictly below a version, expressed as the
	// union of the exact sets below it. Built from Exactly and Union rather than
	// from a specifier so this does not depend on specifier parsing.
	if len(all) > 2 {
		cut := 1 + rng.Intn(len(all)-1)
		below := pep440set.Empty()
		for _, v := range all[:cut] {
			below = below.Union(pep440set.Exactly(v))
		}
		out = append(out, below)

		// And its complement within the list: everything from cut up.
		above := pep440set.Empty()
		for _, v := range all[cut:] {
			above = above.Union(pep440set.Exactly(v))
		}
		out = append(out, above)

		// A gappy set -- every other version -- which is the shape no contiguous
		// span can express and the one an "intersection is two binary searches"
		// optimization would have to handle.
		gappy := pep440set.Empty()
		for i := 0; i < len(all); i += 2 {
			gappy = gappy.Union(pep440set.Exactly(all[i]))
		}
		out = append(out, gappy)
	}
	return out
}
