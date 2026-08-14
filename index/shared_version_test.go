// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"strings"
	"sync"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// This file pins ONE promise, the one PackageMetadata.SupportsPython makes to
// callers in its doc comment: a parsed version.Version may be shared between
// goroutines. That promise was relaxed in this module on the strength of prose
// and a manual reproduction, and prose does not fail a build.
//
// # Why the guarantee needs pinning HERE and not only upstream
//
// go-python-packaging has its own test for the same fix. It does not run in this
// module's CI, and it cannot: the thing that makes the promise true for OUR
// callers is the version in OUR go.mod. Downgrading that pin to v0.5.0 -- by
// hand, by a `go get -u` gone wrong, or by a replace directive in a consumer --
// reintroduces the race with nothing in this repository objecting. These tests
// are what object.
//
// # The hazard, and what it takes to reach it
//
// Until go-python-packaging v0.6.0, rstudio/go-version's part.Parts.Normalize
// resliced the comparison key's release segment to drop trailing zeros, leaving
// len < cap, and part.Parts.Padding then appended into that spare capacity IN
// PLACE. A by-value copy of a Version shares the backing array with its
// original, so two goroutines comparing two copies wrote to the same memory.
//
// It fires only when BOTH of these hold:
//
//  1. The shared operand carries spare capacity, which it acquires only where
//     trailing zeros were stripped. "3.11" has none and is immune. "3.11.0"
//     strips to [3 11] with cap 3 and races.
//  2. The shared operand is the SHORTER of the pair, so it is the one Padding
//     grows.
//
// ⚠️ That narrowness has already produced a false negative here. An earlier
// attempt to reproduce this against production data came back clean, not
// because the code was safe but because no sampled version happened to end in
// ".0". Fixture strings below are chosen deliberately for the shape, and
// TestSharedVersionFixturesReachBothPaths asserts the shape rather than
// trusting this comment -- a comment cannot stop someone adding "1.2.3" to the
// table later.
//
// # Two paths, two separate safety arguments, both covered
//
// v0.6.0 fixes this in two different ways depending on the version:
//
//   - PACKABLE versions never touch part.Parts at all. Comparison runs off a
//     packed integer key computed at Parse time, so there is no slice to alias.
//   - UNPACKABLE versions still walk the general path, which now pads by
//     COPYING (padParts) instead of appending in place.
//
// Roughly 76.2% of distinct version strings in a production PyPI snapshot are
// packable (97.3% weighted by occurrence). The fallback is not exotic: about a
// quarter of distinct versions take it, under an entirely separate argument. A
// test covering only packable versions would say nothing about it, so the table
// covers both and the alloc guard proves which path each entry actually took.

// shareCase is one (shared, longer) pair for the comparison tests.
type shareCase struct {
	// name is the subtest name.
	name string

	// shared is the version a single parse of which every goroutine copies.
	// It must end in a trailing zero and be the shorter operand.
	shared string

	// longer has strictly more release segments than shared after trailing
	// zeros are stripped, so shared is what gets padded.
	longer string

	// packable records which of v0.6.0's two safety arguments this entry is
	// there to exercise. Asserted, not assumed: see wantAllocs.
	packable bool

	// why records what this entry covers that no other entry covers.
	why string
}

var shareCases = []shareCase{
	{
		name:     "packable",
		shared:   "1.2.0",
		longer:   "1.2.3.4",
		packable: true,
		why: "The common case, and the one the packed integer key covers. " +
			"No epoch, no local label, few small release segments, so " +
			"comparison never reaches part.Parts and there is no aliasing " +
			"to have.",
	},
	{
		name:     "unpackable-local",
		shared:   "1.2.0+shared",
		longer:   "1.2.3.4+other",
		packable: false,
		why: "A local version label disqualifies packing outright, so this " +
			"pair walks the general path and is safe only because padParts " +
			"copies. Local labels are the cleanest disqualifier to assert " +
			"from outside the library, since Version.Local() is exported.",
	},
	{
		name:     "unpackable-epoch",
		shared:   "1!1.2.0",
		longer:   "1!1.2.3.4",
		packable: false,
		why: "A non-zero epoch is a second, independent disqualifier. It is " +
			"here because the local-label entry could stop being unpackable " +
			"if the packer ever learned to carry a local label, and one " +
			"fixture standing for a whole path is one fixture too few.",
	},
	{
		name:     "unpackable-long-release",
		shared:   "1.2.3.4.5.6.7.0",
		longer:   "1.2.3.4.5.6.7.8.9",
		packable: false,
		why: "More than six release segments after stripping, the third " +
			"disqualifier, and the only one where the padding distance is " +
			"greater than one segment.",
	},
}

// releaseSegments returns the release component of a PEP 440 version string,
// with the epoch prefix, the pre/post/dev suffix and the local label removed.
//
// The fixtures above are deliberately simple enough that this does not need to
// be a parser. It exists so the guard below reads the FIXTURE STRING rather
// than asking the library, because what is being guarded is that the fixture
// still has the shape the hazard needs -- a question about the test data, not
// about the library under test.
func releaseSegments(s string) []string {
	if i := strings.Index(s, "!"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, "+"); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexFunc(s, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	}); i >= 0 {
		s = strings.TrimSuffix(s[:i], ".")
	}
	return strings.Split(s, ".")
}

// stripTrailingZeros mirrors what the comparison key does to a release segment,
// which is where the spare capacity the hazard needs comes from.
func stripTrailingZeros(segs []string) []string {
	for len(segs) > 1 && segs[len(segs)-1] == "0" {
		segs = segs[:len(segs)-1]
	}
	return segs
}

// TestSharedVersionFixturesReachBothPaths is the anti-vacuity guard for
// TestSharedParsedVersionIsRaceFree. It asserts three things a green race run
// cannot:
//
//  1. Every shared fixture ends in a trailing zero, so it carries the spare
//     capacity without which the hazard is unreachable and the race test is
//     decorative.
//  2. Every shared fixture is the shorter operand, so it is the side Padding
//     would have grown.
//  3. Each entry takes the path it claims to. The packed path allocates
//     NOTHING; the general path allocates, because padParts copies. That makes
//     allocation an observable, public-API discriminator between the two
//     safety arguments, so "we covered the fallback too" is measured rather
//     than asserted from a version string and a memory of the rules.
//
// Point 3 also fails loudly if the packability rules change upstream such that
// a fixture silently moves paths, which is exactly how a two-path test decays
// into a one-path test.
func TestSharedVersionFixturesReachBothPaths(t *testing.T) {
	var packable, unpackable int

	for _, c := range shareCases {
		t.Run(c.name, func(t *testing.T) {
			sharedRel := releaseSegments(c.shared)
			if sharedRel[len(sharedRel)-1] != "0" {
				t.Fatalf("shared fixture %q does not end in a zero release segment, so it "+
					"carries no spare capacity and cannot exercise the padding hazard; "+
					"see the note at the top of this file", c.shared)
			}

			sharedStripped := len(stripTrailingZeros(sharedRel))
			longerStripped := len(stripTrailingZeros(releaseSegments(c.longer)))
			if sharedStripped >= longerStripped {
				t.Fatalf("shared fixture %q strips to %d release segments and %q to %d: "+
					"shared must be strictly SHORTER or it is not the operand that gets "+
					"padded", c.shared, sharedStripped, c.longer, longerStripped)
			}

			a := mustVersion(t, c.shared)
			b := mustVersion(t, c.longer)
			allocs := testing.AllocsPerRun(100, func() { _ = a.Compare(b) })

			switch {
			case c.packable && allocs != 0:
				t.Errorf("%q vs %q allocates %v per comparison; a packable pair compares "+
					"off the packed integer key and must allocate nothing. This entry is "+
					"no longer covering the packed path.", c.shared, c.longer, allocs)
			case !c.packable && allocs == 0:
				t.Errorf("%q vs %q allocates nothing, so it took the packed path; this "+
					"entry exists to cover the padParts FALLBACK and no longer does. "+
					"Roughly a quarter of distinct real versions take that path.",
					c.shared, c.longer)
			}

			if c.packable {
				packable++
			} else {
				unpackable++
			}
		})
	}

	// The table could be edited down to one path and every subtest above would
	// still pass. Both paths must be represented at all.
	if packable == 0 {
		t.Error("no shareCase is packable; the packed-key path is untested")
	}
	if unpackable == 0 {
		t.Error("no shareCase is unpackable; the padParts fallback is untested, and it is " +
			"about a quarter of distinct real versions with its own safety argument")
	}
}

// TestSharedParsedVersionIsRaceFree shares ONE parsed version.Version across
// eight goroutines, which is the thing SupportsPython's doc now tells callers
// they may do.
//
// Run under -race. Without it this asserts only that the answers agree, which
// they did even when the race was live: the racing writes stored the same bytes
// to the same address, so the corruption was benign and invisible to every
// check except the race detector. That is precisely why this module's CI has to
// run -race, and why go-python-packaging's near-identical regression test sat
// inert in ITS CI until -race was added there.
//
// ⚠️ HOW TO READ A FAILING RUN. Every subtest below shares one goroutine closure,
// so all four report the same stack, and the race detector suppresses repeats of
// a stack it has already reported. Run the whole package against a broken
// dependency and you will see ONE subtest fail, not four -- that is the detector
// deduplicating, NOT three paths going uncovered. Each subtest was confirmed
// individually: pinned to go-python-packaging v0.5.0 and run one at a time with
// -run 'TestSharedParsedVersionIsRaceFree/^<name>$', all four report WARNING:
// DATA RACE. A regression confined to either path alone still produces the first
// report for that stack and still fails the build.
func TestSharedParsedVersionIsRaceFree(t *testing.T) {
	for _, c := range shareCases {
		t.Run(c.name, func(t *testing.T) {
			// want is computed from throwaway parses, so the shared value is
			// untouched until the goroutines start.
			want := mustVersion(t, c.shared).Compare(mustVersion(t, c.longer))
			if want != -1 {
				t.Fatalf("%q.Compare(%q) = %d, want -1: the fixture pair is not ordered "+
					"the way this table assumes", c.shared, c.longer, want)
			}

			shared := mustVersion(t, c.shared)
			longer := mustVersion(t, c.longer)

			var wg sync.WaitGroup
			for g := 0; g < 8; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					// By-value copies. Under v0.5.0 these shared a backing
					// array with the original and with each other.
					v, o := shared, longer
					for i := 0; i < 300; i++ {
						if got := v.Compare(o); got != -1 {
							t.Errorf("Compare(%s, %s) = %d, want -1", c.shared, c.longer, got)
							return
						}
						// The reverse direction too: the hazard is on the
						// shorter operand whichever side of the call it is.
						if got := o.Compare(v); got != 1 {
							t.Errorf("Compare(%s, %s) = %d, want 1", c.longer, c.shared, got)
							return
						}
					}
				}()
			}
			wg.Wait()
		})
	}
}

// pythonShareCase is one SupportsPython fixture: an interpreter target shared
// across goroutines, and the Requires-Python it is checked against.
type pythonShareCase struct {
	name string

	// target is the interpreter version. One parse, shared by every goroutine.
	target string

	// requiresPython is the constraint. ⚠️ Its OPERATOR is load-bearing, see
	// wantOperators.
	requiresPython string

	want     bool
	packable bool
	why      string
}

// ⚠️ Only `<` and `>` reach the hazard. The table in PackageMetadata's doc
// comment records the reproduction: `>=`, `<=`, `==` and `!=` re-parse the
// prospective version through Public() before comparing, which produces a fresh
// Version with no shared backing array, so they are immune. A SupportsPython
// concurrency test built on `>=3.8` -- the single most common Requires-Python
// spelling on PyPI, and the obvious thing to reach for -- would have been green
// under v0.5.0 too.
var directCompareOperators = []string{"<", ">"}

var pythonShareCases = []pythonShareCase{
	{
		name:           "packable-less-than",
		target:         "3.11.0",
		requiresPython: "<3.12.1",
		want:           true,
		packable:       true,
		why: "Exactly the row from the doc comment's table that reported " +
			"WARNING: DATA RACE under v0.5.0 with eight goroutines. If any " +
			"single case has to justify itself, it is this one.",
	},
	{
		name:           "packable-greater-than",
		target:         "3.11.0",
		requiresPython: ">3.9.1",
		want:           true,
		packable:       true,
		why: "The other racing row from that table, and the other direction " +
			"of comparison.",
	},
	{
		name:           "unpackable-epoch",
		target:         "1!3.11.0",
		requiresPython: "<1!3.12.1",
		want:           true,
		packable:       false,
		why: "The padParts fallback, reached through the EXPORTED method " +
			"rather than through Compare directly. An epoch-bearing " +
			"interpreter version is not a realistic Python release, and it " +
			"is not pretending to be: the contract SupportsPython documents " +
			"is about any parsed Version a caller shares, and the realistic " +
			"unpackable exposure is package versions rather than interpreter " +
			"targets -- which is what the resolver-level test covers. This " +
			"entry is here so the exported method's own fallback path is not " +
			"inferred from the Compare-level table above.",
	},
}

// TestSupportsPythonSharedTargetFixturesAreLive is the anti-vacuity guard for
// the test below: the constraint operator must be one that actually compares
// the target directly, the target must carry a trailing zero, and each entry
// must take the path it claims.
func TestSupportsPythonSharedTargetFixturesAreLive(t *testing.T) {
	var packable, unpackable int

	for _, c := range pythonShareCases {
		t.Run(c.name, func(t *testing.T) {
			direct := false
			for _, op := range directCompareOperators {
				// A bare "<" also prefixes "<=", which is immune, so the
				// character after the operator has to be checked too.
				if strings.HasPrefix(c.requiresPython, op) &&
					!strings.HasPrefix(c.requiresPython, op+"=") {
					direct = true
				}
			}
			if !direct {
				t.Fatalf("Requires-Python %q does not use a direct-comparison operator "+
					"(%v). Every other operator re-parses the target through Public() "+
					"first and is immune, so this case would be green under v0.5.0 as "+
					"well and would prove nothing.", c.requiresPython, directCompareOperators)
			}

			rel := releaseSegments(c.target)
			if rel[len(rel)-1] != "0" {
				t.Fatalf("target %q does not end in a zero release segment, so it carries "+
					"no spare capacity; see the note at the top of this file", c.target)
			}

			ss, err := version.NewSpecifiers(c.requiresPython)
			if err != nil {
				t.Fatalf("NewSpecifiers(%q): %v", c.requiresPython, err)
			}
			target := mustVersion(t, c.target)
			if got := (PackageMetadata{RequiresPython: ss}).SupportsPython(target); got != c.want {
				t.Fatalf("SupportsPython(%s) against %q = %v, want %v",
					c.target, c.requiresPython, got, c.want)
			}

			// ⚠️ The allocation probe is on Compare against the constraint's
			// OPERAND, not on Check itself. Specifier stores its operand as a
			// string and re-parses it on every call, so Check allocates about
			// 27 objects whichever path the comparison takes and cannot
			// discriminate. What Check does for `<` and `>` is compare the
			// target against that freshly parsed operand directly -- which is
			// exactly why those two operators reach the hazard and the others
			// do not -- so probing that comparison probes the right thing.
			operand := mustVersion(t, strings.TrimLeft(c.requiresPython, "<>"))
			allocs := testing.AllocsPerRun(100, func() { _ = target.Compare(operand) })
			switch {
			case c.packable && allocs != 0:
				t.Errorf("Compare(%s, %s) allocates %v; a packable pair compares off the "+
					"packed key and must allocate nothing", c.target, operand.String(), allocs)
			case !c.packable && allocs == 0:
				t.Errorf("Compare(%s, %s) allocates nothing, so it took the packed path; "+
					"this entry exists to cover the padParts fallback through the "+
					"exported method and no longer does", c.target, operand.String())
			}

			if c.packable {
				packable++
			} else {
				unpackable++
			}
		})
	}

	if packable == 0 || unpackable == 0 {
		t.Errorf("pythonShareCases covers %d packable and %d unpackable targets; both "+
			"paths must be represented", packable, unpackable)
	}
}

// TestSupportsPythonSharedTargetIsRaceFree is the exported-API half. It shares
// one parsed interpreter version and one parsed Specifiers across eight
// goroutines and calls PackageMetadata.SupportsPython, which is the shape a
// caller fanning a resolution out across goroutines actually has: parse the
// interpreter once, hand it to everything.
//
// That shape is what the doc comment forbade under v0.5.0 and now permits. Run
// under -race.
func TestSupportsPythonSharedTargetIsRaceFree(t *testing.T) {
	for _, c := range pythonShareCases {
		t.Run(c.name, func(t *testing.T) {
			ss, err := version.NewSpecifiers(c.requiresPython)
			if err != nil {
				t.Fatalf("NewSpecifiers(%q): %v", c.requiresPython, err)
			}
			// One PackageMetadata and one target, both shared. Sharing the
			// metadata as well as the target is deliberate: the Specifiers
			// inside it carry parsed versions of their own, which are the
			// other operand of every comparison.
			meta := PackageMetadata{RequiresPython: ss}
			target := mustVersion(t, c.target)

			var wg sync.WaitGroup
			for g := 0; g < 8; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					m, v := meta, target
					for i := 0; i < 300; i++ {
						if got := m.SupportsPython(v); got != c.want {
							t.Errorf("SupportsPython(%s) against %q = %v, want %v",
								c.target, c.requiresPython, got, c.want)
							return
						}
					}
				}()
			}
			wg.Wait()
		})
	}
}
