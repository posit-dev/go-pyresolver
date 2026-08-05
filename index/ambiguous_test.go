// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"testing"
)

// TestMetadataIsDeterministicAcrossEquivalentKeys pins the guarantee RSFIndex's
// own type documentation makes: the same file resolves the same way, forever.
//
// The PEP 440-equality fallback exists because the producer records whatever
// version string a publisher used, so "0.1.0dev" and "0.1dev" can both appear
// and neither is wrong. When a lookup matches no key exactly, that fallback
// picks a key that compares equal.
//
// ⚠️ It used to pick by ITERATING A GO MAP AND TAKING THE FIRST MATCH, and Go
// randomizes map iteration order. On a package with two equal-comparing keys
// carrying different dependencies, repeated lookups against ONE index returned
// different answers -- measured on the real snapshot as 500 calls producing two
// distinct results, and observable from the CLI as alternating output across
// runs. That is a wrong answer delivered with total confidence, and it directly
// falsified the documented guarantee.
//
// The fixture package "ambiguous" carries exactly that shape.
func TestMetadataIsDeterministicAcrossEquivalentKeys(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()
	pkg := NewPackageName("ambiguous")
	ver := mustVersion(t, "0.1.dev0")

	first, err := idx.Metadata(ctx, pkg, ver)
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(first.RequiresDist) != 1 {
		t.Fatalf("precondition: expected exactly one requirement, got %v", first.RequiresDist)
	}
	want := first.RequiresDist[0].String()

	// Enough iterations that a 50/50 coin flip would be seen with overwhelming
	// probability. Deliberately re-queries the SAME index, because a per-index
	// cache would otherwise hide the nondeterminism behind its first answer.
	for i := 0; i < 200; i++ {
		got, err := idx.Metadata(ctx, pkg, ver)
		if err != nil {
			t.Fatalf("Metadata on iteration %d: %v", i, err)
		}
		if len(got.RequiresDist) != 1 {
			t.Fatalf("iteration %d: expected one requirement, got %v", i, got.RequiresDist)
		}
		if got.RequiresDist[0].String() != want {
			t.Fatalf("iteration %d returned %q where the first call returned %q -- the "+
				"equivalent-key fallback is not deterministic, which falsifies the "+
				"guarantee that the same file resolves the same way forever",
				i, got.RequiresDist[0].String(), want)
		}
	}
}

// TestMetadataPrefersTheCanonicallySpelledKey pins WHICH key the fallback picks,
// not merely that it is stable.
//
// Determinism alone would be satisfied by any fixed rule, including an accident
// of insertion order that a later refactor would silently change. Among keys that
// compare equal, the one whose spelling is already canonical wins, so the choice
// is a stated rule rather than an artifact.
//
// The "canonpref" fixture has keys "1.0.0" and "01.0.0". Both compare equal to a
// request for "1.0"; "1.0.0" round-trips through normalization and "01.0.0" does
// not. Neither equals the request's canonical string, so the exact-match path
// misses and the fallback runs.
//
// ⚠️ The keys are chosen so the two halves of the rule DISAGREE: "01.0.0" sorts
// first lexicographically, so a tiebreak-only implementation picks the
// non-canonical key and this test catches it. An earlier draft used keys where the
// canonical one happened to sort first anyway, and consequently passed against an
// implementation with canonical preference removed — caught by the mutation run,
// not by reading.
//
// Repeated deliberately. With two candidate keys, a single lookup against the old
// map-order implementation had a 50% chance of picking the right one by luck, so a
// one-shot assertion here would be an unreliable detector of the very bug this
// file exists for.
func TestMetadataPrefersTheCanonicallySpelledKey(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	for i := 0; i < 200; i++ {
		meta, err := idx.Metadata(ctx, NewPackageName("canonpref"), mustVersion(t, "1.0"))
		if err != nil {
			t.Fatalf("Metadata on iteration %d: %v", i, err)
		}
		if len(meta.RequiresDist) != 1 {
			t.Fatalf("iteration %d: expected one requirement, got %v", i, meta.RequiresDist)
		}
		if got := meta.RequiresDist[0].Name; got != "canonical" {
			t.Fatalf("iteration %d: RequiresDist = %q, want %q: the key \"1.0.0\" is its own "+
				"canonical spelling and must be preferred over \"01.0.0\", which normalizes to it",
				i, got, "canonical")
		}
	}
}

// TestMetadataTiebreakIsLexicographicWhenNoKeyIsCanonical covers the other half
// of the rule, using the "ambiguous" fixture where NEITHER key is canonical:
// "0.1.0dev" normalizes to "0.1.0.dev0" and "0.1dev" to "0.1.dev0".
//
// An earlier draft of this test asserted that "0.1dev" would win, on the mistaken
// belief that it canonicalizes to the requested "0.1.dev0". It does not — the
// canonical spelling of a dev release writes the segment as ".dev0", so the key is
// one character short of canonical. Measured against pypa/packaging rather than
// assumed, after the test failed.
//
// So the tiebreak decides, and "0.1.0dev" wins because "." sorts below "d".
// Recorded because the value looks arbitrary and a future reader would otherwise
// be tempted to "correct" it.
func TestMetadataTiebreakIsLexicographicWhenNoKeyIsCanonical(t *testing.T) {
	idx := openFixtureIndex(t)

	meta, err := idx.Metadata(context.Background(), NewPackageName("ambiguous"), mustVersion(t, "0.1.dev0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(meta.RequiresDist) != 1 {
		t.Fatalf("expected one requirement, got %v", meta.RequiresDist)
	}
	if got := meta.RequiresDist[0].Name; got != "alpha" {
		t.Errorf("RequiresDist = %q, want %q: neither key is canonical, so the "+
			"lexicographically smallest wins, and \"0.1.0dev\" < \"0.1dev\"", got, "alpha")
	}
}
