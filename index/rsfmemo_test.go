// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// These cover the parsed memo added above deps: that a second call answers from
// it, that it answers the SAME thing, and that a caller holding a previous
// answer cannot reach into it.
//
// ⚠️ The aliasing tests are the ones that matter. Without the copy in
// cloneMetadata the metadata one fails, and it fails in the direction that is
// hardest to notice in production: the memo keeps serving a value some
// unrelated caller edited, forever, with nothing at the mutation site to
// suggest it. cmd/pyresolve's `versions` subcommand really does sort the result
// of Versions in place, so this is not a hypothetical caller.
//
// ⚠️ THE VERSIONS HALF IS NOW LIVE. It was written as a regression test for the
// day the version memo started holding parsed values rather than keys, and could
// not fail until then, because what Versions returned had been re-parsed and was
// never in the cache. That day arrived: the memo holds parsed versions, and
// deleting the copy in Versions makes this fail with "the second call saw the
// first call's mutations". It was the ONLY test that failed on that deletion when
// the memo landed; shared_memo_test.go then added two more that do.
//
// ⚠️ All three are in THIS package. Nothing outside index/ catches the deletion,
// cmd/pyresolve's own tests included -- and cmd/pyresolve is the caller that
// sorts the result of Versions in place, which is the whole reason the copy
// exists. It gets away with it because one invocation calls Versions once. So the
// protection here is not redundant with an end-to-end test somewhere; there is no
// end-to-end test that would notice.
//
// ⚠️ "Three tests" is a count, not three independent guards. The other two catch
// it through a white-box pointer-identity check against an unexported field, and
// in TestSharedMemoizedVersionsAreRaceFree that check is a t.Fatalf PRECONDITION
// -- it aborts before the concurrent phase rather than detecting the defect. This
// test is the only one that observes the damage through the exported API, which
// is why it is the one that must not be folded into the others.
//
// It is a test of slice IDENTITY. The separate question of whether the parsed
// version.Version VALUES are safe to share between goroutines is
// TestSharedMemoizedVersionsAreRaceFree in shared_memo_test.go.

func TestVersionsMemoIsNotAliasedByTheCaller(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()
	pkg := NewPackageName("flask")

	first, err := idx.Versions(ctx, pkg)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(first) < 2 {
		t.Fatalf("fixture gave %d versions; this test needs at least 2", len(first))
	}
	want := renderVersions(first)

	// What a real caller does: sort the slice it was handed. Reversed here so
	// the damage is visible whatever the memo's own order happens to be.
	sort.Sort(sort.Reverse(version.SortedVersions(first)))
	// And overwrite an element outright, which no ordering can disguise.
	first[0] = mustVersion(t, "99.99.99")

	second, err := idx.Versions(ctx, pkg)
	if err != nil {
		t.Fatalf("Versions (second call): %v", err)
	}
	if got := renderVersions(second); !equalStrings(got, want) {
		t.Errorf("the second call saw the first call's mutations:\n got %v\nwant %v", got, want)
	}
}

func TestMetadataMemoIsNotAliasedByTheCaller(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()
	pkg := NewPackageName("flask")
	ver := mustVersion(t, "3.0.0")

	first, err := idx.Metadata(ctx, pkg, ver)
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(first.RequiresDist) != 2 || len(first.ProvidesExtra) != 2 {
		t.Fatalf("fixture gave %d requirements and %d extras; this test needs 2 of each",
			len(first.RequiresDist), len(first.ProvidesExtra))
	}
	wantReqs := renderRequirements(first.RequiresDist)
	wantExtras := append([]string(nil), first.ProvidesExtra...)

	first.RequiresDist[0] = requirement.Requirement{}
	first.ProvidesExtra[0] = "clobbered"

	second, err := idx.Metadata(ctx, pkg, ver)
	if err != nil {
		t.Fatalf("Metadata (second call): %v", err)
	}
	if got := renderRequirements(second.RequiresDist); !equalStrings(got, wantReqs) {
		t.Errorf("RequiresDist: the second call saw the first call's mutations:\n got %v\nwant %v", got, wantReqs)
	}
	if !equalStrings(second.ProvidesExtra, wantExtras) {
		t.Errorf("ProvidesExtra: the second call saw the first call's mutations:\n got %v\nwant %v",
			second.ProvidesExtra, wantExtras)
	}
}

// ⚠️ The test above assigns to RequiresDist[0] and ProvidesExtra[0] -- the two
// slices cloneMetadata copies -- and passed for as long as Requirement.Extras
// went uncopied, because no fixture requirement carried brackets and no
// assertion reached a level deeper than the outer slice. That is the shape of
// the gap: Extras is an exported []string reachable THROUGH the copied
// RequiresDist, so copying the outer slice alone leaves it aliased and the memo
// serves the mutation to every later caller for the life of the index.
//
// So this mutates one level deeper, on a fixture package that exists to carry
// brackets. The nil case travels with it: `urllib3` has no "[...]" clause, and
// go-python-packaging documents that as nil rather than empty, which the copy
// must preserve.
func TestMetadataMemoExtrasAreNotAliasedByTheCaller(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()
	pkg := NewPackageName("bracketed")
	ver := mustVersion(t, "1.0.0")

	first, err := idx.Metadata(ctx, pkg, ver)
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(first.RequiresDist) != 2 {
		t.Fatalf("fixture gave %d requirements; this test needs 2", len(first.RequiresDist))
	}

	withExtras, plain := -1, -1
	for i, r := range first.RequiresDist {
		if len(r.Extras) > 0 {
			withExtras = i
		} else if r.Extras == nil {
			plain = i
		}
	}
	if withExtras < 0 || plain < 0 {
		t.Fatalf("fixture must carry one bracketed requirement and one plain one; got %v",
			renderRequirements(first.RequiresDist))
	}
	wantExtras := append([]string(nil), first.RequiresDist[withExtras].Extras...)
	wantRendered := renderRequirements(first.RequiresDist)

	// The mutation the outer-slice copy does not stop.
	first.RequiresDist[withExtras].Extras[0] = "CLOBBERED"

	second, err := idx.Metadata(ctx, pkg, ver)
	if err != nil {
		t.Fatalf("Metadata (second call): %v", err)
	}
	if got := second.RequiresDist[withExtras].Extras; !equalStrings(got, wantExtras) {
		t.Errorf("Extras: the second call saw the first call's mutation:\n got %v\nwant %v", got, wantExtras)
	}
	// Asserted on the rendering too, because that is what a caller of this
	// module actually consumes: a clobbered extra reaches the solver as a
	// different virtual package.
	if got := renderRequirements(second.RequiresDist); !equalStrings(got, wantRendered) {
		t.Errorf("RequiresDist rendering changed:\n got %v\nwant %v", got, wantRendered)
	}
	if second.RequiresDist[plain].Extras != nil {
		t.Errorf("a requirement with no [...] clause came back with Extras = %v, want nil",
			second.RequiresDist[plain].Extras)
	}
}

// The same gap, in the mock. The stated value of MockIndex copying at all is
// that a mock-backed test catches a mutating caller BEFORE it reaches a real
// index -- and it catches only what it copies. Extras was missing from the mock
// and from cloneMetadata for exactly as long as it was missing from either, so
// the two implementations agreed on the wrong answer.
func TestMockMetadataExtrasAreNotAliasedByTheCaller(t *testing.T) {
	ctx := context.Background()
	idx := NewMockIndex("test").
		AddVersion("app", "1.0.0", "requests[socks,security]>=2.0", "urllib3")

	pkg := NewPackageName("app")
	ver := mustVersion(t, "1.0.0")

	first, err := idx.Metadata(ctx, pkg, ver)
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(first.RequiresDist) != 2 || len(first.RequiresDist[0].Extras) != 2 {
		t.Fatalf("mock gave %v; this test needs a bracketed requirement first",
			renderRequirements(first.RequiresDist))
	}
	wantExtras := append([]string(nil), first.RequiresDist[0].Extras...)

	first.RequiresDist[0].Extras[0] = "CLOBBERED"

	second, err := idx.Metadata(ctx, pkg, ver)
	if err != nil {
		t.Fatalf("Metadata (second call): %v", err)
	}
	if got := second.RequiresDist[0].Extras; !equalStrings(got, wantExtras) {
		t.Errorf("Extras: the second call saw the first call's mutation:\n got %v\nwant %v", got, wantExtras)
	}
	if second.RequiresDist[1].Extras != nil {
		t.Errorf("a requirement with no [...] clause came back with Extras = %v, want nil",
			second.RequiresDist[1].Extras)
	}
}

// A memoized answer must be the answer, not a differently-shaped one. In
// particular the nil-versus-empty distinction PackageMetadata has always made --
// "the record declared no requirements" comes back nil -- must survive a round
// trip through the memo, since a caller branching on nil would otherwise behave
// differently on the second call than on the first.
func TestMemoizedMetadataMatchesTheFirstAnswer(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	cases := []struct {
		pkg string
		ver string
	}{
		{"flask", "3.0.0"},        // requirements and extras
		{"flask", "3.0.1"},        // requirements, no extras
		{"flask", "3.0.2"},        // unreadable Requires-Python, nothing else
		{"padded", "1.0"},         // Requires-Python only
		{"ambiguous", "0.1.dev0"}, // resolved through the PEP 440-equality fallback
		{"canonpref", "1.0"},      // ditto, with the canonical-preference tiebreak
	}

	for _, tc := range cases {
		pkg := NewPackageName(tc.pkg)
		ver := mustVersion(t, tc.ver)

		first, err := idx.Metadata(ctx, pkg, ver)
		if err != nil {
			t.Fatalf("%s %s: Metadata: %v", tc.pkg, tc.ver, err)
		}
		second, err := idx.Metadata(ctx, pkg, ver)
		if err != nil {
			t.Fatalf("%s %s: Metadata (memoized): %v", tc.pkg, tc.ver, err)
		}

		if (first.RequiresDist == nil) != (second.RequiresDist == nil) {
			t.Errorf("%s %s: RequiresDist nil-ness changed between calls: first nil=%v, second nil=%v",
				tc.pkg, tc.ver, first.RequiresDist == nil, second.RequiresDist == nil)
		}
		if (first.ProvidesExtra == nil) != (second.ProvidesExtra == nil) {
			t.Errorf("%s %s: ProvidesExtra nil-ness changed between calls: first nil=%v, second nil=%v",
				tc.pkg, tc.ver, first.ProvidesExtra == nil, second.ProvidesExtra == nil)
		}
		if got, want := renderRequirements(second.RequiresDist), renderRequirements(first.RequiresDist); !equalStrings(got, want) {
			t.Errorf("%s %s: memoized RequiresDist = %v, want %v", tc.pkg, tc.ver, got, want)
		}
		if !equalStrings(second.ProvidesExtra, first.ProvidesExtra) {
			t.Errorf("%s %s: memoized ProvidesExtra = %v, want %v",
				tc.pkg, tc.ver, second.ProvidesExtra, first.ProvidesExtra)
		}
		if second.RequiresPythonRaw != first.RequiresPythonRaw ||
			second.RequiresPythonUnreadable != first.RequiresPythonUnreadable ||
			second.RequiresPython.String() != first.RequiresPython.String() ||
			second.Origin != first.Origin ||
			second.Name != first.Name ||
			second.Version.String() != first.Version.String() {
			t.Errorf("%s %s: memoized metadata differs from the first answer:\nfirst  %+v\nsecond %+v",
				tc.pkg, tc.ver, first, second)
		}
	}
}

// A repeated failure must classify identically, whether or not the memo is what
// answered. A resolution that backtracks asks about the same rejected version
// many times; if the second answer lost the sentinel the provider would stop
// reporting "try another version" and start aborting the resolve.
//
// The two cases take different routes on purpose. ErrMetadataUnusable is a fact
// about a stored record and IS memoized, under that record's key.
// ErrMetadataUnavailable names a version the corpus does not have, so there is
// no stored key to file it under and it is recomputed every time -- see
// TestMemoDoesNotGrowWithLookupsOfVersionsThatDoNotExist for why that is not an
// oversight. This test is what keeps the two indistinguishable to a caller.
func TestMemoizedMetadataFailuresKeepTheirSentinel(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	cases := []struct {
		name string
		pkg  string
		ver  string
		want error
	}{
		{"unparseable requirement", "broken", "1.0", ErrMetadataUnusable},
		{"version never captured", "flask", "9.9.9", ErrMetadataUnavailable},
	}

	for _, tc := range cases {
		pkg := NewPackageName(tc.pkg)
		ver := mustVersion(t, tc.ver)

		_, firstErr := idx.Metadata(ctx, pkg, ver)
		if !errors.Is(firstErr, tc.want) {
			t.Fatalf("%s: first call err = %v, want %v", tc.name, firstErr, tc.want)
		}
		_, secondErr := idx.Metadata(ctx, pkg, ver)
		if !errors.Is(secondErr, tc.want) {
			t.Errorf("%s: memoized err = %v, want %v", tc.name, secondErr, tc.want)
		}
		if firstErr.Error() != secondErr.Error() {
			t.Errorf("%s: memoized message differs:\nfirst  %v\nsecond %v", tc.name, firstErr, secondErr)
		}
	}
}

// The memo must not turn a package that is absent into one that is merely
// missing this version, nor cache the absence against a version key.
func TestMemoDoesNotSwallowPackageNotFound(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()
	pkg := NewPackageName("no-such-package")
	ver := mustVersion(t, "1.0")

	for i := range 2 {
		if _, err := idx.Metadata(ctx, pkg, ver); !errors.Is(err, ErrPackageNotFound) {
			t.Errorf("call %d: err = %v, want ErrPackageNotFound", i+1, err)
		}
		if _, err := idx.Versions(ctx, pkg); !errors.Is(err, ErrPackageNotFound) {
			t.Errorf("call %d: Versions err = %v, want ErrPackageNotFound", i+1, err)
		}
	}
}

// Keying the memo by the STORED key means several requested spellings share one
// entry, so nothing REQUEST-SCOPED may be memoized -- and an error message names
// the request. Memoizing the finished ErrMetadataUnusable message told the
// second caller that its request for the FIRST caller's spelling had failed,
// which is a falsehood aimed at exactly the person reading the log to find out
// what they asked for.
//
// The fixture's `broken` package stores "1.0" with an unparseable requirement,
// so "1.0" hits it directly and "1.0.0" reaches the same record through PEP 440
// equality. Both must be told about themselves.
func TestUnusableErrorNamesTheVersionTHISCallerAsked(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()
	pkg := NewPackageName("broken")

	for _, spelling := range []string{"1.0", "1.0.0", "1.0.0.0"} {
		_, err := idx.Metadata(ctx, pkg, mustVersion(t, spelling))
		if !errors.Is(err, ErrMetadataUnusable) {
			t.Fatalf("%s: err = %v, want ErrMetadataUnusable", spelling, err)
		}
		if want := "\"broken\" " + spelling + ":"; !strings.Contains(err.Error(), want) {
			t.Errorf("%s: message does not name the version THIS caller asked for.\n"+
				" got %v\nwant a message containing %q", spelling, err, want)
		}
		// The fact about the record must still be there -- this is not a
		// trade of accuracy for genericity.
		if !strings.Contains(err.Error(), "!!! not a requirement") {
			t.Errorf("%s: message lost the offending requirement string: %v", spelling, err)
		}
	}
}

// ⚠️ The memo must not grow with what a caller ASKS FOR, only with what the file
// HOLDS. This is the property that decides whether an RSFIndex can sit in a
// long-lived server, and it is not a property of well-behaved callers -- it has
// to be a property of the key.
//
// An earlier draft of the memo keyed it by the request's ver.String(), which
// looked total and deterministic and was both, and was still unbounded: the
// blob cache does not cache a package it could not find, so it is bounded by
// the corpus, but a memoized miss is filed under a string nothing constrains.
// 20,000 lookups of versions that do not exist left 20,000 permanent entries
// while the blob cache stayed at one package. Alias spellings did the same to
// the successes -- "3.0.0", "3.0.0.0" and "3.0.0.0.0" are three requests naming
// one record, and each minted its own entry.
//
// Both halves are asserted, because fixing only the miss would leave the same
// unbounded growth reachable through a spelling that succeeds.
func TestMemoDoesNotGrowWithLookupsOfVersionsThatDoNotExist(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()
	pkg := NewPackageName("flask")

	// Versions that do not exist. The count is large enough that a per-request
	// entry would be unmistakable.
	for i := range 2000 {
		ver := mustVersion(t, fmt.Sprintf("9.9.%d", i))
		if _, err := idx.Metadata(ctx, pkg, ver); !errors.Is(err, ErrMetadataUnavailable) {
			t.Fatalf("9.9.%d: err = %v, want ErrMetadataUnavailable", i, err)
		}
	}

	// Alias spellings of ONE stored record: "3.0.0", "3.0.0.0", and so on. Each
	// renders differently and each is PEP 440-equal to the stored "3.0.0".
	spelling := "3.0.0"
	for range 20 {
		ver := mustVersion(t, spelling)
		meta, err := idx.Metadata(ctx, pkg, ver)
		if err != nil {
			t.Fatalf("Metadata(%s): %v", spelling, err)
		}
		if len(meta.RequiresDist) != 2 {
			t.Fatalf("Metadata(%s) resolved to the wrong record: %v",
				spelling, renderRequirements(meta.RequiresDist))
		}
		spelling += ".0"
	}

	idx.memoMu.RLock()
	entries := len(idx.parsed[pkg])
	idx.memoMu.RUnlock()

	// flask stores four version keys, one of which PEP 440 rejects. One entry
	// is the ceiling here because only one distinct record was ever resolved;
	// the ceiling that matters is that it does not scale with the 2,020 calls.
	if entries != 1 {
		t.Errorf("the memo holds %d entries after 2,020 lookups of one record and "+
			"2,000 versions that do not exist; want 1, one per STORED key resolved", entries)
	}

	idx.mu.RLock()
	blobs := len(idx.decoded)
	idx.mu.RUnlock()
	if blobs != 1 {
		t.Errorf("the blob cache holds %d packages, want 1", blobs)
	}
}

// guardTrailingZeroVersions fails if no fixture version ends in ".0".
//
// The concurrency test below can only expose a shared version.Version through
// version.Version.Compare, and Compare only writes into shared memory when the
// operand it pads has spare release capacity -- which a version acquires only
// where cmpkey reslices trailing zeros away. "3.0.1" is immune, "3.0.0" is not.
// So a fixture with no trailing-zero version turns that test green while
// checking nothing, and nothing in the test itself would say so.
//
// It opens its OWN index rather than probing the one under test: a probe would
// warm the version memo for every package, and the concurrent phase would then
// never take the first-call path where one goroutine builds the order while
// others read it.
func guardTrailingZeroVersions(t *testing.T, pkgs []string) {
	t.Helper()
	idx := openFixtureIndex(t)
	for _, name := range pkgs {
		vers, err := idx.Versions(context.Background(), NewPackageName(name))
		if err != nil {
			continue
		}
		for _, v := range vers {
			if strings.HasSuffix(v.String(), ".0") {
				return
			}
		}
	}
	t.Fatal("no fixture version ends in \".0\", so no comparison can pad into shared " +
		"capacity and the concurrency test below cannot detect a shared version.Version")
}

// Run with -race. Both memos are written under memoMu while the underlying
// blob cache is written under mu, and the two are taken in sequence rather than
// nested, so this also exercises the ordering.
//
// The property under test is that Versions and Metadata hand every goroutine
// its own state, from a COLD memo -- goroutines racing to be the one that builds
// each package's plan.
//
// ⚠️ Cold is what it covers and cold is ALL it covers, which is narrower than it
// reads. Every goroutine calls Versions exactly ONCE, so on a 48-goroutine,
// 6-package run most of them take the first-call path and get a slice nobody else
// holds. With the defensive copy in Versions deleted, this test catches it 8
// times in 20 -- measured, one fresh process per run -- against 20 in 20 for the
// warm-memo test. It reaches the same hazard under a v0.5.0 dependency pin at the
// same 8-in-20 rate. It is a scheduling lottery for the sharing case, and a
// lottery is not a guard: it is a flake generator that happens to be pointing at
// something real.
//
// So it is kept for what it does cover -- concurrent first calls, the memoMu/mu
// ordering, and Metadata's slice copies -- and the sharing case is covered
// deterministically, by warming the memo first, in
// TestSharedMemoizedVersionsAreRaceFree (shared_memo_test.go).
func TestMemoIsSafeUnderConcurrentUse(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	pkgs := []string{"flask", "padded", "nodeps", "ambiguous", "canonpref", "broken"}
	guardTrailingZeroVersions(t, pkgs)

	var wg sync.WaitGroup
	for i := range 48 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			pkg := NewPackageName(pkgs[i%len(pkgs)])
			vers, err := idx.Versions(ctx, pkg)
			if err != nil {
				t.Errorf("Versions(%s): %v", pkg, err)
				return
			}
			// Sorting drives version.Version.Compare, which pads the shorter
			// operand's release segment into spare capacity -- so if Versions
			// ever handed two goroutines values backed by one array, this is
			// where -race would say so. It fires only because the fixture
			// carries versions that END IN ".0" (flask's 3.0.0, canonpref's
			// 1.0.0, padded's 1.0): reslicing trailing zeros is what leaves the
			// spare capacity behind, and a fixture of "3.0.1"-shaped versions
			// would sort just as busily and prove nothing. Asserted by
			// guardTrailingZeroVersions above rather than left to this comment.
			//
			// ⚠️ It does NOT reliably catch versionList's own slice being handed
			// back, now that the memo holds parsed versions and the two COULD be
			// the same object. See the note above this function: one call per
			// goroutine means most of them build their own plan. Slice identity
			// is TestVersionsMemoIsNotAliasedByTheCaller's job, and sharing under
			// a WARM memo is TestSharedMemoizedVersionsAreRaceFree's.
			sort.Sort(sort.Reverse(version.SortedVersions(vers)))

			for _, v := range vers {
				meta, err := idx.Metadata(ctx, pkg, v)
				if err != nil {
					continue // broken/1.0 is unusable by design
				}
				// Write to the returned slice from many goroutines at once.
				// If Metadata handed back the memo's own slice this is a data
				// race on it, and -race reports it.
				for j := range meta.RequiresDist {
					meta.RequiresDist[j] = meta.RequiresDist[len(meta.RequiresDist)-1-j]
				}
			}
		}(i)
	}
	wg.Wait()
}

func renderVersions(vs []version.Version) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.String()
	}
	return out
}

func renderRequirements(rs []requirement.Requirement) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.String()
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
