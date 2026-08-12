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
// # ⚠️ THREE INPUTS FAIL, and that is the finding
//
// Two seeds, from an overflow in THIS package:
//
//	>99999999999999999999.0
//	~=1.0.99999999999999999999
//
// bound.go's releaseKey builds its comparison key with strconv.Atoi and BREAKS
// out of the loop on error, so a release segment at or above 2^63
// (9223372036854775808) is dropped along with every segment after it. The
// version's key becomes a PREFIX of its real release -- for the first seed, the
// empty key -- so it sorts below everything, and `>` that operand admits every
// version in existence while Check admits none. gpp orders the same pair
// correctly, so this divergence is this package's.
//
// One corpus entry the fuzzer found in 13 seconds, from a bug in the ORACLE:
//
//	~=0.0.posT     (testdata/fuzz/FuzzFromSpecifiers/8da712f769546bb9)
//
// `~=0.0.posT` and `~=0.0.post0` are the same specifier -- PEP 440 case-folds
// the suffix, and version.Parse renders both as 0.0.post0 -- but gpp's Check
// answers differently for them: it computes the ~= prefix from the RAW operand
// text, and its suffix test misses "posT", so it drops the last segment of
// "0.0.posT" instead of the post part and checks ==0.0.* where PEP 440 says
// ==0.*. Check(0.9) is then false for `~=0.0.posT` and true for `~=0.0.post0`.
// This package normalizes through version.Parse first, so Contains is right and
// Check is wrong here. Not a construct.go defect; an upstream one.
//
// All three are left failing deliberately. The seeds are not weakened, the
// corpus entry is committed rather than deleted, and neither bound.go nor
// construct.go is touched: a red test naming the exact inputs is the report.
//
// A supplementary 90-second sweep with the differential assertion reduced to
// "must not panic" (so the known disagreements did not end the run early) did
// 26.9M executions and found no panic.
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
