// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"testing"
)

// TestVersionsReturnsNoEqualDuplicates pins the interface contract: no two
// versions returned by Versions may compare equal.
//
// The stored keys record whatever string a publisher used, so one package can
// carry both "1.0" and "1.0.0". Those are the same version under PEP 440.
// Returning both hands a resolver two candidates it CANNOT tell apart — they
// compare equal, so no constraint selects between them and the choice falls to
// iteration order. Measured on a production snapshot: 59 such classes across 56
// packages, 10 of which disagree about dependencies, so the pick changed the
// resulting dependency graph.
//
// The "canonpref" fixture carries keys "1.0.0" and "01.0.0"; "ambiguous" carries
// "0.1.0dev" and "0.1dev". Before this change each returned two versions.
func TestVersionsReturnsNoEqualDuplicates(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	for _, name := range []string{"canonpref", "ambiguous", "flask", "padded", "broken"} {
		vers, err := idx.Versions(ctx, NewPackageName(name))
		if err != nil {
			t.Fatalf("Versions(%q): %v", name, err)
		}
		for i := range vers {
			for j := i + 1; j < len(vers); j++ {
				if vers[i].Equal(vers[j]) {
					t.Errorf("Versions(%q) returned %q and %q, which compare EQUAL under "+
						"PEP 440 — a resolver cannot select between them", name, vers[i], vers[j])
				}
			}
		}
	}
}

// TestVersionsCollapsesAClassToOneNormalizedVersion checks that an equality class
// yields exactly one version, and that its rendering is the PEP 440 normal form.
//
// ⚠️ IT DELIBERATELY DOES NOT CLAIM TO CHECK WHICH KEY WON, and an earlier draft
// of it did. That claim was untestable here: Versions returns PARSED versions, and
// parsing normalizes, so every member of a class renders identically —
// "01.0.0" and "1.0.0" both come back as "1.0.0". A mutation that inverted the
// canonical preference left this assertion passing, which is how the overreach was
// found.
//
// Which stored key won is observable only through the dependency data it carries,
// so that property lives in TestVersionsAndMetadataResolveTheSameRecord.
func TestVersionsCollapsesAClassToOneNormalizedVersion(t *testing.T) {
	idx := openFixtureIndex(t)

	vers, err := idx.Versions(context.Background(), NewPackageName("canonpref"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vers) != 1 {
		t.Fatalf("Versions returned %d versions, want 1: %v", len(vers), vers)
	}
	if got := vers[0].String(); got != "1.0.0" {
		t.Errorf("survivor = %q, want %q", got, "1.0.0")
	}
}

// TestVersionsAndMetadataAgreeOnEveryVersion is the coherence property, and it is
// the one that matters most.
//
// Dedup is only safe if the version Versions hands out resolves, through
// Metadata, to the record dedup treated as authoritative. If the two used
// separate implementations of "prefer the canonical spelling" they would agree
// only by coincidence, and a caller could hold a version whose dependencies came
// from the spelling that LOST the tiebreak — a wrong dependency graph with
// nothing in the data to justify it.
//
// Asserted for every version of every fixture package rather than for one case,
// because the failure would be silent and data-dependent.
//
// ⚠️ The property has to be stated precisely, and my first draft overreached by
// asserting Metadata simply succeeds. It legitimately does not for the "broken"
// fixture, whose stored requirement is unparseable — an unparseable requirement
// is FATAL by design, since silently dropping it would under-constrain the graph.
// That failure has nothing to do with dedup.
//
// What dedup must guarantee is narrower and is what is asserted: no version
// Versions reports may be MISSING A RECORD. ErrMetadataUnavailable on a version
// that was just listed would mean dedup invented a version, or kept a
// representative whose record cannot be found.
func TestVersionsAndMetadataAgreeOnEveryVersion(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	for _, name := range idx.Packages() {
		pkg := NewPackageName(name)
		vers, err := idx.Versions(ctx, pkg)
		if err != nil {
			t.Fatalf("Versions(%q): %v", name, err)
		}
		for _, v := range vers {
			_, err := idx.Metadata(ctx, pkg, v)
			if errors.Is(err, ErrMetadataUnavailable) {
				t.Errorf("Versions(%q) reported %q but Metadata cannot find a record for it: %v",
					name, v, err)
			}
		}
	}
}

// TestVersionsAndMetadataResolveTheSameRecord is the sharper half of the coherence
// property, and it needs its own test because the one above cannot see this
// failure: a MISSING record raises ErrMetadataUnavailable, but resolving the WRONG
// record of an equality class raises nothing at all.
//
// The "ambiguous" fixture is the case that can expose it, and the reason is worth
// stating: it has NO canonical member, so the representative Versions hands out
// does not equal any stored key. Metadata's exact-match lookup therefore misses
// and the equality fallback runs — meaning both sides independently choose, and a
// divergence between their rules becomes observable. For a class that DOES have a
// canonical member the exact-match path short-circuits the fallback and masks any
// disagreement, which is why that case cannot be used here.
//
// "0.1.0dev" carries requirement "alpha" and "0.1dev" carries "beta"; neither key
// is canonical, so the lexicographic tail decides and "0.1.0dev" wins.
func TestVersionsAndMetadataResolveTheSameRecord(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()
	pkg := NewPackageName("ambiguous")

	vers, err := idx.Versions(ctx, pkg)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vers) != 1 {
		t.Fatalf("Versions returned %d versions, want 1 after dedup: %v", len(vers), vers)
	}

	meta, err := idx.Metadata(ctx, pkg, vers[0])
	if err != nil {
		t.Fatalf("Metadata(%q): %v", vers[0], err)
	}
	if len(meta.RequiresDist) != 1 {
		t.Fatalf("expected one requirement, got %v", meta.RequiresDist)
	}
	if got := meta.RequiresDist[0].Name; got != "alpha" {
		t.Errorf("Metadata for the deduped representative %q returned %q, want %q -- Versions "+
			"and Metadata chose DIFFERENT members of the same equality class, so a caller "+
			"holds a version whose dependencies came from the spelling that lost",
			vers[0], got, "alpha")
	}
}

// TestVersionsIsDeterministic guards against the dedup itself reintroducing map
// order. The selection walks a map, so an implementation that picked the first
// member of each class rather than the preferred one would be stable within a
// call and vary across calls.
func TestVersionsIsDeterministic(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()
	pkg := NewPackageName("ambiguous")

	first, err := idx.Versions(ctx, pkg)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	want := make([]string, 0, len(first))
	for _, v := range first {
		want = append(want, v.String())
	}

	for i := 0; i < 200; i++ {
		got, err := idx.Versions(ctx, pkg)
		if err != nil {
			t.Fatalf("Versions on iteration %d: %v", i, err)
		}
		if len(got) != len(want) {
			t.Fatalf("iteration %d returned %d versions, first call returned %d", i, len(got), len(want))
		}
		for j, v := range got {
			if v.String() != want[j] {
				t.Fatalf("iteration %d: version %d = %q, first call gave %q", i, j, v, want[j])
			}
		}
	}
}

// TestPreferKeyRuleAndOrdering checks the shared rule directly, and it is the test
// that actually pins the canonical preference — because as noted above, Versions
// cannot observe which key won.
//
// It also checks the property that makes preferKey usable as a sort comparator:
// irreflexivity and asymmetry. That is not pedantry. sort.Slice with a comparator
// that calls two elements mutually not-less-than treats them as tied and leaves
// their relative order to the input permutation, which for a map walk means the
// dedup silently goes back to being nondeterministic. Two of my own attempted
// mutations of preferKey produced exactly that, and this assertion is what
// distinguished "the rule is wrong" from "the comparator is broken".
func TestPreferKeyRuleAndOrdering(t *testing.T) {
	type key struct {
		s         string
		canonical bool
	}
	keys := []key{
		{"1.0.0", true},
		{"01.0.0", false},
		{"1.0.00", false},
		{"0.1.dev0", true},
	}

	for _, a := range keys {
		if preferKey(a.s, a.canonical, a.s, a.canonical) {
			t.Errorf("preferKey is not irreflexive for %q", a.s)
		}
		for _, b := range keys {
			if a.s == b.s {
				continue
			}
			ab := preferKey(a.s, a.canonical, b.s, b.canonical)
			ba := preferKey(b.s, b.canonical, a.s, a.canonical)
			if ab == ba {
				t.Errorf("preferKey is not asymmetric for %q vs %q: both directions returned %v",
					a.s, b.s, ab)
			}
		}
	}

	// Canonical wins regardless of lexicographic order, which is the whole point.
	if !preferKey("1.0.0", true, "01.0.0", false) {
		t.Error("a canonical key must beat a non-canonical one even when it sorts later")
	}
	// Between two non-canonical keys the tiebreak decides.
	if !preferKey("01.0.0", false, "1.0.00", false) {
		t.Error("between two non-canonical keys the lexicographically smaller must win")
	}
}

// TestUnparseableVersionKeysDistinguishesTwoEmptyStates is the regression test for
// a state collapse that made the CLI report present data as absent.
//
// Versions skips a key PEP 440 rejects, which is right for a resolver — a few
// non-conforming keys are normal and one must not make a whole package
// unreachable. But when EVERY key is rejected, Versions returns an empty slice,
// which is indistinguishable from "nothing was captured for this package".
//
// Those are different facts. On a production snapshot `holygrail` holds exactly
// one key, "0.2.1.Perceval", carrying a real dependency on sqlobject — so the data
// is present and the old message ("no versions with captured dependency data")
// sent the reader looking for something missing.
func TestUnparseableVersionKeysDistinguishesTwoEmptyStates(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	// "flask" has both parseable keys and one bad one ("not-a-version"), so the
	// bad key must be reported even though versions exist.
	bad, err := idx.UnparseableVersionKeys(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("UnparseableVersionKeys(flask): %v", err)
	}
	if len(bad) != 1 || bad[0] != "not-a-version" {
		t.Errorf("flask unparseable keys = %v, want [not-a-version]", bad)
	}

	// A package whose keys all parse reports none.
	bad, err = idx.UnparseableVersionKeys(ctx, NewPackageName("padded"))
	if err != nil {
		t.Fatalf("UnparseableVersionKeys(padded): %v", err)
	}
	if len(bad) != 0 {
		t.Errorf("padded unparseable keys = %v, want none", bad)
	}
}

// TestUnparseableVersionKeysPropagatesNotFound keeps the accessor consistent with
// the rest of the type: an unknown package is ErrPackageNotFound, not an empty
// result, so a caller cannot mistake "no such package" for "no bad keys".
func TestUnparseableVersionKeysPropagatesNotFound(t *testing.T) {
	idx := openFixtureIndex(t)

	_, err := idx.UnparseableVersionKeys(context.Background(), NewPackageName("nonexistent"))
	if !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("expected ErrPackageNotFound for an unknown package, got %v", err)
	}
}
