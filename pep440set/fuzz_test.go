// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"errors"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

// fuzzSeeds cover every operator and the operand shapes the hand-rolled string
// arithmetic in construct.go has to survive: epochs (which move the "!" the
// segment splitter has to skip), local labels, dev and post segments, wildcards
// at several depths, `===`, the `v` prefix, whitespace, and the degenerate
// single-segment `~=` that has no upper bound to compute.
func fuzzSeeds() []string {
	return []string{
		"", "==1.0", "!=1.0", "<1.0", "<=1.0", ">1.0", ">=1.0", "~=1.0",
		"===1.0", "===foobar", "=1.0",
		"~=1", "~=1.0.0", "~=2.2.3", "~=1.0.post1", "~=0.0.1a1.post2",
		"==1.*", "!=1.*", "==1.0.*", "==1.0.0.0.*", "==1!1.*", "~=1.*",
		"==1!1.0", ">=1!0.1,<1!2.0", "1!1.0",
		"==1.0+local", "==1.0+ubuntu.1", ">1.0+a", "<=1.0+zzz",
		"==1.0rc1", ">1.0rc1", "<1.0rc1", ">=1.0a1", "==1.0b2",
		"==1.0.dev0", ">1.0.dev0", "<1.0.dev0", ">=1.0.post1", "<1.0.post1",
		"==1.0rc1.post1", "==1.0.post1.dev0", "==1.0rc1.dev0",
		"v1.0", "==v1.0", ">= 1.0", "  >=1.0  ,  <2.0  ",
		">=1.0,<2.0", ">=1.0,!=1.0.1,<2.0", "!=1.0,!=2.0,!=3.0",
		">=1.0||<0.5", "==1.0 || ==2.0",
		"==00.1", "==1.00.1", "==1.0.1.2.3.4.5",
		"==0", "==0.0.0", ">99999999999999999999.0", "~=1.0.99999999999999999999",
		"==99999999999999999999.*", "<1.5", ">=1.5", "==9223372036854775808",
		">1.0rc99999999999999999999",
		// The alias spellings `~=` does not treat as equivalent to the
		// canonical ones, and the `v` prefix, whose derived ~= prefix is not a
		// version at all.
		"~=1.0c1", "~=1.0.c1", "~=1.0.pre1", "~=1.0.preview1", "~=1.0.r1",
		"~=1.0.rev1", "~=0.0.posT", "~=0.0.post0", "~=1.0.pre1.r1", "~=v1.0",
		"~=1.0.POST1",
		"==*", "==.*", "==1..*", "== .*", "==a.*", "==1.0.*.*",
		">=1.0.dev", "==1.0-1", "==1.0_1", "==1.0.post", "==1.0-post1",
	}
}

// fuzzVersions is the fixed probe list: small, because the fuzzer runs it on
// every input, and spread across the corners (epoch, pre, post, dev, local) so
// a bound landing one position off the right group is visible.
func fuzzVersions() []string {
	return []string{
		"0.9", "1.0.dev0", "1.0a1", "1.0rc1", "1.0rc1+l", "1.0",
		"1.0+local", "1.0.post1", "1.0.post1.dev0", "1.0.0", "1.0.1",
		"1.1", "2.0", "2.2.3", "1!1.0",
		// Two probes past int64, one on each side of the dot: an ordering key
		// that truncates oversized segments misplaces both.
		"1.99999999999999999999", "99999999999999999999.0",
	}
}

// FuzzFromSpecifiers drives arbitrary specifier text through the mapping.
//
// # Why fuzz this and not the algebra
//
// Operand strings reach FromSpecifiers from a network-fetched snapshot: they
// are attacker-influenceable in the sense that matters here, since anyone can
// publish a package. construct.go does not parse them with a grammar -- it
// splits on ".", indexes the last element, and calls strconv.Atoi
// (incrementLastSegment, compatibleUpperBound, releasePrefixSpan). Those are
// exactly the operations that panic on an empty split result or an unexpected
// shape, and gpp's specifier grammar is what decides which shapes get that far.
//
// Two properties are asserted:
//
//   - Anything gpp accepts, FromSpecifiers survives. A panic here is a crash in
//     a resolver reading real index data, not a bad answer.
//   - Where a Set comes back, it agrees with Check. An error return is not a
//     failure: ErrUnrepresentable is deliberate, and an operand gpp's grammar
//     admits but version.Parse rejects is a refusal, which is a safe answer.
//
// # What this found, and how one of the findings was misread
//
// Three inputs failed when the fuzzer was first run, and all three were bugs in
// THIS package. Two seeds:
//
//	>99999999999999999999.0
//	~=1.0.99999999999999999999
//
// bound.go's releaseKey built its comparison key with strconv.Atoi and BROKE
// out of the loop on error, so a release segment at or above 2^63
// (9223372036854775808) was dropped along with every segment after it. The
// key became a PREFIX of the real release -- for the first seed, the empty key
// -- so the operand sorted below everything and `>` it admitted every version
// in existence while Check admitted none. gpp holds release segments as
// arbitrary-precision integers and ordered the same pair correctly.
//
// And one corpus entry the fuzzer found in 13 seconds:
//
//	~=0.0.posT     (testdata/fuzz/FuzzFromSpecifiers/8da712f769546bb9)
//
// ⚠️ THIS ONE WAS FIRST WRITTEN UP AS A BUG IN THE ORACLE. IT WAS NOT.
//
// The reasoning was that `~=0.0.posT` and `~=0.0.post0` are the same specifier,
// since PEP 440 case-folds the suffix and version.Parse renders both as
// 0.0.post0 -- so Check answering differently for them had to be Check's fault,
// and this package, which normalized through version.Parse first, had to be
// right. Every step of that is true except the conclusion. Running
// pypa/packaging 26.2 directly settles it: it agrees with Check on `~=0.0.posT`
// and on all six alias spellings of `~=1.0c1`. `~=` derives its prefix from the
// RAW OPERAND TEXT, by a deliberately incomplete and case-SENSITIVE suffix
// test, and two spellings of one version are therefore two different
// specifiers. construct.go now reproduces that rule; see compatibleUpperBound.
//
// The lesson is cheap to state and was expensive to learn: a differential
// disagreement is evidence about a PAIR. Deciding which side is wrong from the
// standard rather than from the reference implementation picks the more
// elegant answer, not the correct one. The corpus entry stays committed as a
// regression seed.
//
// With all three fixed, a 90-second sweep did 15.5M executions against the FULL
// differential assertion -- not a weakened must-not-panic one -- and found
// nothing.
func FuzzFromSpecifiers(f *testing.F) {
	for _, seed := range fuzzSeeds() {
		f.Add(seed)
	}

	versions := make([]version.Version, 0, len(fuzzVersions()))
	for _, s := range fuzzVersions() {
		v, err := version.Parse(s)
		if err != nil {
			f.Fatalf("probe version %q: %v", s, err)
		}
		versions = append(versions, v)
	}

	f.Fuzz(func(t *testing.T, spec string) {
		ss, err := version.NewSpecifiers(spec)
		if err != nil {
			// Not a specifier set at all; nothing this package promises about.
			return
		}

		set, err := FromSpecifiers(ss)
		if err != nil {
			if errors.Is(err, ErrUnrepresentable) {
				return
			}
			// A refusal on an operand that parses as a specifier but not as a
			// version, or a ~= with nothing to increment. Refusing is sound.
			return
		}

		for _, v := range versions {
			if got, want := set.Contains(v), ss.Check(v); got != want {
				t.Errorf("%q vs %s: Contains=%v Check=%v",
					spec, v.Original(), got, want)
			}
		}
	})
}
