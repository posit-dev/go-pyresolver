// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// orderEntry is one position in the ascending grid below.
type orderEntry struct {
	name string
	b    bound
}

// ascendingPositions is the full ordering grid: every entry is strictly below
// the one after it. It is deliberately wider than TestBoundOrdering's -- it adds
// a second release group, a pre-release group with its own locals and
// post-releases, and the epoch/segment magnitudes past 2^63 -- because it exists
// to catch a REORDERING introduced by a change to how cmpBound derives its key.
//
// The ordering of the public versions in this grid was cross-checked against
// pypa/packaging 26.2, the reference implementation, rather than reasoned about:
// every pair of the public spellings below compares in this order there too.
//
// The local-carrying entries are the exception, on purpose. cmpBound orders two
// local labels with a raw string compare, which is NOT pypa/packaging's local
// ordering (it sorts numeric segments above alphabetic ones), and that does not
// affect any set: the only bounds that name a local variant are the pair
// Exactly builds around ONE label, which bracket it whichever way other labels
// sort. The two labels used here, "aaa" and "zzz", sort the same way under both
// rules.
func ascendingPositions(t *testing.T) []orderEntry {
	t.Helper()
	v := func(s string) version.Version { return mustV(t, s) }
	at := func(s string) orderEntry {
		return orderEntry{"at(" + s + ")", bound{v: v(s), edge: edgeAt}}
	}
	below := func(s string) orderEntry {
		return orderEntry{"belowRelease(" + s + ")", bound{v: v(s), edge: edgeBelowRelease}}
	}
	aboveExact := func(s string) orderEntry {
		return orderEntry{"aboveExact(" + s + ")", bound{v: v(s), edge: edgeAboveExact}}
	}
	aboveLocals := func(s string) orderEntry {
		return orderEntry{"aboveLocals(" + s + ")", bound{v: v(s), edge: edgeAboveLocals}}
	}
	aboveRelease := func(s string) orderEntry {
		return orderEntry{"aboveRelease(" + s + ")", bound{v: v(s), edge: edgeAboveRelease}}
	}

	return []orderEntry{
		{"-inf", negInf()},

		// The 1.0 release group, from its floor to its ceiling.
		below("1.0"),
		at("1.0.dev0"),
		at("1.0.dev1"),
		at("1.0a1"),
		at("1.0a1+aaa"),
		aboveLocals("1.0a1"),
		at("1.0a1.post0.dev0"),
		at("1.0a2"),
		at("1.0b1"),
		at("1.0rc1"),
		aboveExact("1.0rc1"),
		aboveLocals("1.0rc1"),
		at("1.0"),
		aboveExact("1.0"),
		at("1.0+aaa"),
		aboveExact("1.0+aaa"),
		at("1.0+zzz"),
		aboveExact("1.0+zzz"),
		aboveLocals("1.0"),
		at("1.0.post0.dev0"),
		at("1.0.post1"),
		at("1.0.post1+aaa"),
		aboveLocals("1.0.post1"),
		aboveRelease("1.0"),

		// Later release groups.
		below("1.0.1"),
		at("1.0.1"),
		aboveRelease("1.0.1"),

		// ⚠️ A pair differing ONLY past the sixth release segment. gpp packs a
		// release into six 32-bit fields and falls back to arbitrary-precision
		// comparison beyond that, so these two straddle the fast path's edge:
		// a packer that silently truncated at six segments, or whose fields
		// overlapped, would order them EQUAL and nothing else in this module
		// would notice. gpp has its own tests for the layout; this is the row
		// that makes a regression in it fail HERE, where the consequence is a
		// resolver picking the wrong version.
		at("1.0.1.2.3.4.5"),
		at("1.0.1.2.3.4.6"),

		below("1.1"),
		at("1.1"),
		below("1.5"),

		// Release segments and epochs past what an int64 holds.
		at("1.9223372036854775807"),
		at("1.9223372036854775808"),
		at("1.99999999999999999999"),
		at("2.0"),
		at("9223372036854775808"),
		at("99999999999999999999.0"),
		below("1!0.1"),
		at("1!1.0"),
		at("9223372036854775808!1.0"),

		{"+inf", posInf()},
	}
}

// TestBoundOrderingGrid pins the total order over the wide grid: strictly
// ascending, antisymmetric, and reflexively zero.
func TestBoundOrderingGrid(t *testing.T) {
	entries := ascendingPositions(t)
	for i := range entries {
		for j := range entries {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := cmpBound(entries[i].b, entries[j].b); got != want {
				t.Errorf("cmpBound(%s, %s) = %d, want %d",
					entries[i].name, entries[j].name, got, want)
			}
		}
	}
}

// TestBoundOrderingTransitive checks the property a sort depends on directly.
// newSet feeds cmpBound to sort.SliceStable, and a comparison that is not
// transitive produces a silently mis-ordered set rather than an error.
func TestBoundOrderingTransitive(t *testing.T) {
	entries := ascendingPositions(t)
	for i := range entries {
		for j := range entries {
			for k := range entries {
				ij := cmpBound(entries[i].b, entries[j].b)
				jk := cmpBound(entries[j].b, entries[k].b)
				if ij <= 0 && jk <= 0 {
					if got := cmpBound(entries[i].b, entries[k].b); got > 0 {
						t.Fatalf("not transitive: %s <= %s <= %s but cmpBound(%s, %s) = %d",
							entries[i].name, entries[j].name, entries[k].name,
							entries[i].name, entries[k].name, got)
					}
				}
			}
		}
	}
}

// TestBoundEqualPositions pins the positions that must compare EQUAL. This is
// the invariant canonicalization rests on: newSet fuses spans that meet, and
// Equal is a structural walk over bounds, so two spellings of one position
// comparing unequal splits a set into two representations that no longer
// compare equal to each other.
//
// ⚠️ The trailing-zero cases are the ones a "faster key" is most likely to
// break. 1.0, 1.0.0 and 1.00 are the same version -- confirmed against
// pypa/packaging 26.2 -- so a key that carried the segment COUNT, or that
// compared the rendered release text, would order them apart while every other
// test still passed.
func TestBoundEqualPositions(t *testing.T) {
	v := func(s string) version.Version { return mustV(t, s) }

	equal := []struct {
		name string
		a, b bound
	}{
		{"at(1.0) == at(1.0.0)",
			bound{v: v("1.0"), edge: edgeAt}, bound{v: v("1.0.0"), edge: edgeAt}},
		{"at(1.0) == at(1.0.0.0)",
			bound{v: v("1.0"), edge: edgeAt}, bound{v: v("1.0.0.0"), edge: edgeAt}},
		{"at(1.0) == at(1.00)",
			bound{v: v("1.0"), edge: edgeAt}, bound{v: v("1.00"), edge: edgeAt}},
		{"at(1) == at(1.0.0)",
			bound{v: v("1"), edge: edgeAt}, bound{v: v("1.0.0"), edge: edgeAt}},
		{"at(1!1.0) == at(1!1.0.0)",
			bound{v: v("1!1.0"), edge: edgeAt}, bound{v: v("1!1.0.0"), edge: edgeAt}},
		{"at(1!1.0) == at(01!1.0)",
			bound{v: v("1!1.0"), edge: edgeAt}, bound{v: v("01!1.0"), edge: edgeAt}},
		{"at(0!1.0) == at(1.0)",
			bound{v: v("0!1.0"), edge: edgeAt}, bound{v: v("1.0"), edge: edgeAt}},
		// ⚠️ THESE TWO ARE NOT ABOUT LEADING ZEROS, so do not read them as that
		// guard. The group key comes from gpp's parsed release segments, which
		// are math/big integers: "1.00000000000000000001" is the integer 1
		// before a key exists, and no zero-stripping runs at all. What they
		// guard is the layer above -- that a segment past 2^63 survives the
		// parse at full precision, and that the key orders it by VALUE rather
		// than truncating to an int or comparing rendered text. Leading-zero
		// spellings are covered directly by TestLeadingZeroSpellings.
		{"at(1.00000000000000000001) == at(1.1)",
			bound{v: v("1.00000000000000000001"), edge: edgeAt},
			bound{v: v("1.1"), edge: edgeAt}},
		{"at(1.099999999999999999990) == at(1.0099999999999999999990)",
			bound{v: v("1.099999999999999999990"), edge: edgeAt},
			bound{v: v("1.0099999999999999999990"), edge: edgeAt}},
		// The release group is a property of (epoch, release) alone: every edge
		// naming the group ignores the anchor's pre, post, dev and local parts.
		{"belowRelease(1.0rc1) == belowRelease(1.0)",
			bound{v: v("1.0rc1"), edge: edgeBelowRelease},
			bound{v: v("1.0"), edge: edgeBelowRelease}},
		{"aboveRelease(1.0.post9) == aboveRelease(1.0)",
			bound{v: v("1.0.post9"), edge: edgeAboveRelease},
			bound{v: v("1.0"), edge: edgeAboveRelease}},
		// aboveLocals is a property of the PUBLIC version, so the anchor's own
		// label does not move it.
		{"aboveLocals(1.0+a) == aboveLocals(1.0)",
			bound{v: v("1.0+a"), edge: edgeAboveLocals},
			bound{v: v("1.0"), edge: edgeAboveLocals}},
		{"aboveLocals(1.0+a) == aboveLocals(1.0+b)",
			bound{v: v("1.0+a"), edge: edgeAboveLocals},
			bound{v: v("1.0+b"), edge: edgeAboveLocals}},
	}

	for _, tc := range equal {
		if got := cmpBound(tc.a, tc.b); got != 0 {
			t.Errorf("%s: cmpBound = %d, want 0", tc.name, got)
		}
		if got := cmpBound(tc.b, tc.a); got != 0 {
			t.Errorf("%s (reversed): cmpBound = %d, want 0", tc.name, got)
		}
	}
}

// TestBoundKeyAgreesWithLiteral holds the two ways a bound can get its key
// together: derived once by newBound, or derived on demand by pos() for a bound
// written as a literal.
//
// Both paths exist on purpose -- a literal is the readable form in a test table,
// and a missed construction site should stay CORRECT and merely slow rather than
// silently sorting to a wrong position -- so they have to agree on every pair,
// in every mix of keyed and unkeyed operands.
func TestBoundKeyAgreesWithLiteral(t *testing.T) {
	entries := ascendingPositions(t)
	keyed := func(b bound) bound {
		if b.inf != 0 {
			return b
		}
		return newBound(b.v, b.edge)
	}

	for i := range entries {
		for j := range entries {
			a, b := entries[i].b, entries[j].b
			want := cmpBound(a, b)
			for _, mix := range []struct {
				name string
				a, b bound
			}{
				{"keyed/unkeyed", keyed(a), b},
				{"unkeyed/keyed", a, keyed(b)},
				{"keyed/keyed", keyed(a), keyed(b)},
			} {
				if got := cmpBound(mix.a, mix.b); got != want {
					t.Errorf("cmpBound(%s, %s) [%s] = %d, want %d",
						entries[i].name, entries[j].name, mix.name, got, want)
				}
			}
		}
	}
}

// TestPosKeyPublicFallback pins the two edges of newPosKey's public-version
// handling, which is where it stops being a pure cache of what cmpBound used to
// compute inline.
func TestPosKeyPublicFallback(t *testing.T) {
	// A version with no local label IS its own public version, so the key
	// reuses it rather than re-parsing the rendered spelling. The two must be
	// the same version.
	plain := mustV(t, "1.0rc1.post2.dev3")
	k := newPosKey(plain)
	if !k.pubOK {
		t.Fatal("a local-free version must have a usable public version")
	}
	reparsed := mustV(t, plain.Public())
	if k.pub.Compare(reparsed) != 0 || k.pub.String() != reparsed.String() {
		t.Errorf("reused public %s differs from re-parsed %s",
			k.pub.String(), reparsed.String())
	}

	// A local label is stripped, and the stripped version is what the key
	// carries.
	local := mustV(t, "1.0+ubuntu.1")
	lk := newPosKey(local)
	if !lk.pubOK || lk.pub.Local() != "" || lk.public != "1.0" {
		t.Errorf("newPosKey(1.0+ubuntu.1): public=%q local=%q pubOK=%v, want public 1.0 with no local",
			lk.public, lk.pub.Local(), lk.pubOK)
	}

	// An uninitialized Version renders as nothing, which does not parse. The
	// public comparison is left out for it, exactly as it was when cmpBound
	// parsed inline and got an error.
	if zk := newPosKey(version.Version{}); zk.pubOK {
		t.Error("the zero Version has no public version to compare")
	}
}

// TestEqualSpellingsCanonicalizeAlike is the invariant above, one level up:
// sets built from two spellings of the same version must BE equal, not merely
// hold bounds that compare equal.
func TestEqualSpellingsCanonicalizeAlike(t *testing.T) {
	for _, pair := range [][2]string{
		{"1.0", "1.0.0"},
		{"1.0", "1.00"},
		{"1!2", "1!2.0.0"},
	} {
		a, b := Exactly(mustV(t, pair[0])), Exactly(mustV(t, pair[1]))
		if !a.Equal(b) {
			t.Errorf("Exactly(%s) != Exactly(%s): %s vs %s",
				pair[0], pair[1], a.String(), b.String())
		}
		if !a.Contains(mustV(t, pair[1])) {
			t.Errorf("Exactly(%s) does not contain %s", pair[0], pair[1])
		}
	}
}
