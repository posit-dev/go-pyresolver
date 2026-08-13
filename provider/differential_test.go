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
)

// TestCandidatesAgreeWithAnExactCountOnTheRealIndex is the differential that
// licenses the found/rank split, run against real published metadata rather than
// fixtures.
//
// For every sampled package it requires that:
//
//   - found agrees EXACTLY with the exact implementation. This is the
//     correctness-bearing half: a disagreement means a range is being forbidden
//     that is fine, or offered when nothing is usable.
//   - best is IDENTICAL. Ranking before the usability walk is what makes this true,
//     because candidate.Rank is a stable sort over a pairwise Less and therefore
//     orders a subset consistently with the superset it came from. If that ever
//     stops holding, this is the test that says so.
//   - rank never UNDER-counts the usable versions. Over-counting is by design;
//     under-counting would make the solver prefer this package over one that
//     genuinely has fewer candidates.
//
// Set PYPIRSF_TEST_FILE to a full snapshot for a real run. Without it this uses the
// committed excerpt, which is small but is still real published metadata, so the
// test is meaningful in CI rather than skipped. GPR_SAMPLE and GPR_SEED bound and
// reproduce the sample.
func TestCandidatesAgreeWithAnExactCountOnTheRealIndex(t *testing.T) {
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

	// Default 0 means "all of them", which is what the excerpt wants.
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

	sampled := names
	if limit > 0 && limit < len(names) {
		rng := rand.New(rand.NewSource(seed))
		sampled = make([]string, 0, limit)
		for len(sampled) < limit {
			sampled = append(sampled, names[rng.Intn(len(names))])
		}
	}
	t.Logf("snapshot %s holds %d packages; comparing %d (seed %d)",
		path, len(names), len(sampled), seed)

	// A Provider per side, so neither sees the other's Unusable records. The index
	// is shared so both sides see the same data and the same memo; note that the
	// short-circuit side runs first and touches only the versions it reaches, so the
	// exhaustive side pays the cold reads. Nothing here is timed, so that is fine.
	p := provider.New(context.Background(), idx, testOptions(t))
	ref := provider.New(context.Background(), idx, testOptions(t))

	// discriminating counts the packages where the two implementations could
	// actually have disagreed about best: those with an UNUSABLE version ranked
	// above the chosen one. Everywhere else the top-ranked in-range version is
	// usable, both sides trivially return it, and the comparison proves nothing.
	//
	// ⚠️ This is the number that has to be non-zero, and `compared` is not a
	// substitute: it counts packages where neither side errored, including ones both
	// sides report as unavailable, which compare nothing but false == false.
	var compared, available, overCounted, discriminating int
	for _, name := range sampled {
		pkg := provider.Project(index.PackageName(name))

		gotBest, gotFound, gotRank, gotErr := p.Candidates(pkg, pep440set.All())
		wantBest, wantFound, wantCount, wantErr := ref.ExactCandidates(pkg, pep440set.All())

		// ⚠️ Errors are compared in ONE direction only, deliberately.
		//
		// p.usable returns an error when the index cannot answer, and the short-circuit
		// walk examines a SUBSET of the versions the exhaustive one does. So the
		// exhaustive reference can hit an unreadable older version that the real path
		// never reaches, and reporting that as a disagreement would be asserting the
		// exact property this design gives up. The other direction still holds and is
		// worth pinning: anything the short-circuit walk errors on, the exhaustive walk
		// must also have errored on, because it looked at strictly more.
		if gotErr != nil && wantErr == nil {
			t.Errorf("%s: the short-circuit walk errored (%v) where the exhaustive one did "+
				"not, but it examines strictly fewer versions", name, gotErr)
			continue
		}
		if gotErr != nil || wantErr != nil {
			continue
		}
		compared++

		if gotFound != wantFound {
			t.Errorf("%s: found = %v, exact found = %v — the correctness-bearing half disagrees",
				name, gotFound, wantFound)
			continue
		}
		if !gotFound {
			continue
		}
		available++

		if !gotBest.Equal(wantBest) {
			t.Errorf("%s: best = %v, exact best = %v — ranking before the usability walk is "+
				"what makes these identical", name, gotBest, wantBest)
		}
		if gotRank < wantCount {
			t.Errorf("%s: rank = %d but %d versions are usable — rank may over-count and must "+
				"never under-count", name, gotRank, wantCount)
		}
		if gotRank > wantCount {
			overCounted++
		}

		// The short-circuit walk only differs from the exhaustive one when it has to
		// SKIP something. rank counts the in-range versions and wantCount the usable
		// ones, so rank > count means at least one in-range version was unusable; and
		// best being lower-ranked than the top of the in-range list is what proves one
		// of those sat above the chosen version. Approximate that here by the cheap
		// observable: the two differ in count AND the chosen version is not the newest
		// in range.
		if gotRank > wantCount && !gotBest.Equal(topOfRange(t, p, pkg)) {
			discriminating++
		}
	}

	t.Logf("compared %d packages, %d with something available, %d where rank over-counted, "+
		"%d where an unusable version was skipped to reach best",
		compared, available, overCounted, discriminating)

	if compared == 0 {
		t.Fatal("compared nothing, so this would have passed vacuously")
	}
	// ⚠️ The assertion that keeps this test honest.
	//
	// Without it the suite can report a comfortable "compared 139 packages" while
	// every one of them had a usable top-ranked version, in which case both
	// implementations trivially agree and the skip this change is ABOUT is never
	// exercised. On the committed excerpt only a handful of packages discriminate,
	// the excerpt is machine-regenerable (index/fixture_gen_test.go), and nothing in
	// the generator selects for this property -- so a regeneration could take the
	// real coverage to zero silently. This turns that into a failure.
	if discriminating == 0 {
		t.Errorf("not one of the %d compared packages had an unusable version ranked above "+
			"the chosen one, so the short-circuit walk never skipped anything and this "+
			"differential compared two implementations doing identical work. Point "+
			"PYPIRSF_TEST_FILE at a fuller snapshot, or regenerate the excerpt to include "+
			"a package with an unusable release above a usable one", compared)
	}
}

// topOfRange is the newest version of pkg the provider would consider, ignoring
// usability -- the version the walk starts at.
func topOfRange(t *testing.T, p *provider.Provider, pkg provider.Package) pep440set.Set {
	t.Helper()
	vs, err := p.InRangeRanked(pkg, pep440set.All())
	if err != nil || len(vs) == 0 {
		return pep440set.Empty()
	}
	return pep440set.Exactly(vs[0])
}
