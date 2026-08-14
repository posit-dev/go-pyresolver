// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/version"
)

// majorOnly ranks by MAJOR VERSION ONLY, so every version sharing a major is
// mutually incomparable -- a genuine tie, in quantity.
//
// It is a legal Policy: irreflexive, asymmetric, transitive, and its
// incomparability is transitive too (sharing a major is an equivalence
// relation). What it is not is TOTAL, and that is the entire point.
type majorOnly struct{}

func (majorOnly) Less(_ index.PackageName, a, b version.Version) bool {
	return majorOf(a) > majorOf(b)
}

func majorOf(v version.Version) int {
	s := v.String()
	if i := strings.IndexAny(s, ".+-"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "!"); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return n
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// TestCandidatesUnderATiedPolicyAgreesWithTheExactReference is the missing test
// for the property the memo's correctness argument is actually phrased in.
//
// # Why the rest of the suite cannot cover this
//
// Provider.Candidates ranks a package's FULL version list once and filters that
// order per call, and the argument that this picks the same version as ranking
// the in-range list is: candidate.Rank is a STABLE sort, so it orders a subset
// consistently with the superset. Stability only says anything when elements
// TIE.
//
// And nothing else in this package ever produces one. The default Newest policy
// is total on real index output -- TestRSFIndexVersionsAreAscending measures zero
// PEP 440-equal adjacent pairs, because index.RSFIndex collapses each equality
// class to one representative -- and the only other Policy in the tests,
// oldestFirst, is total as well. So an UNSTABLE sort would pass every existing
// provider test. That is the gap this closes: candidate/ diagnoses it for Rank
// in isolation, and the claim lives here.
//
// A tied policy also makes the memo's superset-versus-subset question sharp
// rather than academic, because which of several tied versions comes first is now
// decided entirely by input order.
func TestCandidatesUnderATiedPolicyAgreesWithTheExactReference(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("shared", "1.0").
		AddVersion("shared", "1.2").
		AddVersion("shared", "1.9").
		AddVersion("shared", "2.0").
		AddVersion("shared", "2.4").
		AddVersion("shared", "2.7").
		AddVersion("shared", "3.1").
		AddVersion("shared", "3.5")

	opts := testOptions(t)
	opts.Policy = majorOnly{}

	ctx := context.Background()
	pkg := provider.Project(index.PackageName("shared"))

	// Ranges chosen so the in-range subset starts at different points inside a
	// tie group. If ranking the superset disagreed with ranking the subset, this
	// is where it would show: the memo ranks 1.0 through 3.5 once, and each of
	// these filters a different window out of that one order.
	all, err := idx.Versions(ctx, index.PackageName("shared"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(all) != 8 {
		t.Fatalf("fixture has %d versions, want 8", len(all))
	}

	var ranges []pep440set.Set
	ranges = append(ranges, pep440set.All())
	for i := range all {
		// Every suffix of the version list, so the top of each window lands on a
		// different member of a tie group.
		s := pep440set.Empty()
		for _, v := range all[i:] {
			s = s.Union(pep440set.Exactly(v))
		}
		ranges = append(ranges, s)
		// And every prefix.
		s = pep440set.Empty()
		for _, v := range all[:i+1] {
			s = s.Union(pep440set.Exactly(v))
		}
		ranges = append(ranges, s)
	}

	// ONE provider, so every call after the first is served from the memo, and a
	// FRESH reference per call.
	p := provider.New(ctx, idx, opts)

	var compared, tiedTop int
	for _, allowed := range ranges {
		gotBest, gotFound, gotRank, gotErr := p.Candidates(pkg, allowed)

		ref := provider.New(ctx, idx, opts)
		wantBest, wantFound, wantCount, wantErr := ref.ExactCandidates(pkg, allowed)

		if gotErr != nil || wantErr != nil {
			t.Fatalf("over %v: got err %v, want err %v", allowed, gotErr, wantErr)
		}
		if gotFound != wantFound {
			t.Errorf("over %v: found = %v, exact found = %v", allowed, gotFound, wantFound)
			continue
		}
		if !gotFound {
			continue
		}
		compared++
		if !gotBest.Equal(wantBest) {
			t.Errorf("over %v: best = %v, exact best = %v — ranking the full list and "+
				"filtering after must agree with ranking the in-range list, and under a "+
				"policy with ties that is a claim about STABILITY",
				allowed, gotBest, wantBest)
		}
		if gotRank < wantCount {
			t.Errorf("over %v: rank = %d but %d usable", allowed, gotRank, wantCount)
		}

		// Count the calls where the top of the in-range list was part of a tie
		// group of more than one, which are the only ones that test anything.
		if v, ok := gotBest.Singleton(); ok {
			n := 0
			for _, c := range all {
				if allowed.Contains(c) && majorOf(c) == majorOf(v) {
					n++
				}
			}
			if n > 1 {
				tiedTop++
			}
		}
	}

	t.Logf("compared %d ranges, %d of them with the chosen version inside a tie group "+
		"of more than one", compared, tiedTop)

	if compared == 0 {
		t.Fatal("compared nothing")
	}
	// ⚠️ Without this the test could pass over ranges whose top version happened
	// to be alone in its tie group, which exercises nothing stability-dependent.
	if tiedTop == 0 {
		t.Error("no range put the chosen version inside a tie group, so nothing here " +
			"depended on the sort being stable")
	}
}

// TestRankIsStableUnderATiedPolicy pins the property directly, one level below
// Candidates: with ties present, Rank must preserve the INDEX's order among them.
//
// ⚠️ It asserts the property, not a literal ordering, and that is deliberate. A
// first draft hardcoded the expected slice from an assumption about the order
// MockIndex hands back. The assumption was wrong and the test failed against
// correct code -- a hardcoded expectation had quietly turned a stability test
// into an index-ordering test. Deriving the expectation from `all` means this
// keeps testing stability no matter what order the index chooses.
func TestRankIsStableUnderATiedPolicy(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("shared", "2.0").
		AddVersion("shared", "2.4").
		AddVersion("shared", "2.7").
		AddVersion("shared", "1.0").
		AddVersion("shared", "1.2")

	all, err := idx.Versions(context.Background(), index.PackageName("shared"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}

	ranked := candidate.Rank(index.PackageName("shared"), all, majorOnly{})

	if len(ranked) != len(all) {
		t.Fatalf("Rank returned %d of %d versions; it is a reordering, never a filter",
			len(ranked), len(all))
	}

	// 1. Majors must come out in descending order -- that is what the policy asks
	//    for, and it is the half stability says nothing about.
	for i := 1; i < len(ranked); i++ {
		if majorOf(ranked[i]) > majorOf(ranked[i-1]) {
			t.Errorf("Rank = %v: major %d follows major %d, but the policy sorts majors "+
				"descending", ranked, majorOf(ranked[i]), majorOf(ranked[i-1]))
			break
		}
	}

	// 2. THE STABILITY CLAIM: within one major, every version must appear in the
	//    same relative order the index gave, whatever that order is.
	indexPos := map[string]int{}
	for i, v := range all {
		indexPos[v.String()] = i
	}
	var tiedPairs int
	for i := 1; i < len(ranked); i++ {
		if majorOf(ranked[i]) != majorOf(ranked[i-1]) {
			continue
		}
		tiedPairs++
		if indexPos[ranked[i].String()] < indexPos[ranked[i-1].String()] {
			t.Errorf("Rank = %v: %s and %s tie under this policy but came back in the "+
				"opposite order to the index's (%d before %d). The memo's subset "+
				"argument rests on this not happening",
				ranked, ranked[i-1], ranked[i],
				indexPos[ranked[i-1].String()], indexPos[ranked[i].String()])
		}
	}

	t.Logf("index order %v, ranked %v, %d adjacent tied pairs checked", all, ranked, tiedPairs)
	if tiedPairs == 0 {
		t.Error("no two adjacent ranked versions shared a major, so nothing here " +
			"depended on stability")
	}
}
