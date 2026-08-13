// SPDX-License-Identifier: Apache-2.0 OR MIT

package candidate

import (
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pypirsf"
	"github.com/posit-dev/go-python-packaging/version"
)

// TestNewestIsAStrictWeakOrdering checks the property Rank's fast path leans on,
// against real published version strings rather than against the definition.
//
// # Why this is checked rather than argued
//
// Newest.Less delegates to version.Version.Compare, and PEP 440 defines a total
// order, so transitivity "follows". But Compare is upstream code with a
// reflect.DeepEqual fast path inside go-version's part.Parts.Compare, and the
// operands here are whatever publishers actually uploaded — epochs, local
// versions, post/dev releases, zero-padded calendar keys. An ordering bug in that
// path would not announce itself; it would show up as a resolution that picks a
// different legal version depending on what else was in range.
//
// All four properties of a strict weak ordering are checked:
//
//   - irreflexive: Less(v, v) is false
//   - asymmetric: never Less(a, b) and Less(b, a)
//   - transitive: Less(a, b) and Less(b, c) implies Less(a, c)
//   - equivalence is transitive: incomparability must be an equivalence
//     relation, which is the property that distinguishes a strict weak ordering
//     from a mere strict partial order — and the one a stable sort silently
//     needs when it treats "neither before the other" as a tie
func TestNewestIsAStrictWeakOrdering(t *testing.T) {
	const pkg = index.PackageName("x")
	pol := Newest{}

	classes := realVersions(t)
	var vs []version.Version
	var withTies int
	for _, cl := range classes {
		vs = append(vs, cl...)
		if len(cl) > 1 {
			withTies++
		}
	}
	if len(vs) < 3 {
		t.Fatalf("only %d versions to work with; this would prove nothing", len(vs))
	}
	t.Logf("drawing triples from %d published versions in %d equality classes, %d of which "+
		"hold more than one spelling", len(vs), len(classes), withTies)

	less := func(a, b version.Version) bool { return pol.Less(pkg, a, b) }
	// equiv is "neither sorts before the other", which is what a stable sort
	// treats as a tie.
	equiv := func(a, b version.Version) bool { return !less(a, b) && !less(b, a) }

	for _, v := range vs {
		if less(v, v) {
			t.Fatalf("not irreflexive: Less(%s, %s)", v, v)
		}
	}

	// ⚠️ Triples are drawn with a BIAS toward same-class members, and without it
	// this test is vacuous on the equivalence half. Equality classes are rare
	// (two spellings out of tens of thousands of versions), so three independent
	// uniform draws hit a mutually-equivalent triple with probability around
	// 1e-8 — measured as literally zero occurrences in 3,000,000 uniform triples.
	// Half the time each of b and c is drawn from the class its predecessor came
	// from instead.
	rng := rand.New(rand.NewSource(20260813))
	draw := func(near []version.Version) version.Version {
		if near != nil && rng.Intn(2) == 0 {
			return near[rng.Intn(len(near))]
		}
		cl := classes[rng.Intn(len(classes))]
		return cl[rng.Intn(len(cl))]
	}
	classOf := map[string][]version.Version{}
	for _, cl := range classes {
		for _, v := range cl {
			classOf[v.String()] = cl
		}
	}

	// 300,000 keeps the default run near two seconds while still exercising each
	// antecedent tens of thousands of times. Raise it with GPR_TRIPLES for a
	// deeper pass; the published figure was taken at 3,000,000.
	triples := 300000
	if s := os.Getenv("GPR_TRIPLES"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("GPR_TRIPLES=%q: %v", s, err)
		}
		triples = n
	}
	var (
		sawLessLess int
		sawEquivPar int
	)
	for i := 0; i < triples; i++ {
		a := draw(nil)
		b := draw(classOf[a.String()])
		c := draw(classOf[b.String()])

		if less(a, b) && less(b, a) {
			t.Fatalf("not asymmetric: Less(%s, %s) and Less(%s, %s)", a, b, b, a)
		}
		if less(a, b) && less(b, c) {
			sawLessLess++
			if !less(a, c) {
				t.Fatalf("not transitive: Less(%s, %s) and Less(%s, %s) but not Less(%s, %s)",
					a, b, b, c, a, c)
			}
		}
		if equiv(a, b) && equiv(b, c) {
			sawEquivPar++
			if !equiv(a, c) {
				t.Fatalf("incomparability is not transitive: %s ~ %s and %s ~ %s but not %s ~ %s",
					a, b, b, c, a, c)
			}
		}
	}

	t.Logf("%d triples: %d exercised transitivity of Less, %d exercised transitivity of "+
		"incomparability", triples, sawLessLess, sawEquivPar)

	// A run where no triple satisfied either antecedent would pass without
	// checking anything.
	if sawLessLess == 0 || sawEquivPar == 0 {
		t.Errorf("vacuous: %d ordered triples, %d equivalent triples", sawLessLess, sawEquivPar)
	}
}

// realVersions returns published versions from the snapshot, grouped into PEP
// 440 equality classes. It draws from many packages so the mix includes epochs,
// local versions and the calendar-style keys that only a few projects use.
func realVersions(t *testing.T) [][]version.Version {
	t.Helper()

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

	limit := 400
	if s := os.Getenv("GPR_SAMPLE"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil {
			t.Fatalf("GPR_SAMPLE=%q: %v", s, err)
		}
	}
	sampled := names
	if limit > 0 && limit < len(names) {
		rng := rand.New(rand.NewSource(7))
		sampled = make([]string, 0, limit)
		for len(sampled) < limit {
			sampled = append(sampled, names[rng.Intn(len(names))])
		}
	}

	seen := map[string]bool{}
	var out [][]version.Version
	for _, name := range sampled {
		all, err := idx.Versions(t.Context(), index.PackageName(name))
		if err != nil {
			continue
		}
		for _, v := range all {
			if s := v.String(); !seen[s] {
				seen[s] = true
				out = append(out, []version.Version{v})
			}
		}
	}

	// ⚠️ Equal spellings have to be INJECTED, and that is a finding, not a
	// workaround.
	//
	// index.RSFIndex.Versions collapses each PEP 440 equality class to one
	// representative, so no two versions it returns are ever equivalent — a
	// straight sample of real data exercises transitivity of Less thousands of
	// times and transitivity of incomparability exactly zero times. Under the
	// default Newest policy the order on real index output is TOTAL, and the
	// stability candidate.Rank provides is therefore vacuous there.
	//
	// That is worth knowing (it is why the fast path's reversed branch never has
	// to reason about ties on real input) and it is exactly why this cannot be
	// left as the only evidence: a Policy an embedder writes need not be total,
	// and neither is Newest on versions that did not come from this index. So the
	// equality classes PEP 440 defines and the index removes are put back here:
	// "1.2" and "1.2.0" are the same version and must be mutually incomparable.
	for i, cl := range out {
		alt, err := version.Parse(cl[0].String() + ".0")
		if err != nil || seen[alt.String()] {
			continue
		}
		// Guard the premise rather than trusting it: a spelling that is not
		// actually PEP 440-equal would make the "equivalence" half of this test
		// check something else entirely.
		if alt.Compare(cl[0]) != 0 {
			t.Fatalf("%s and %s were expected to be PEP 440-equal and are not",
				cl[0], alt)
		}
		out[i] = append(cl, alt)
	}
	return out
}
