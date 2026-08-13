// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"sort"
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
// The Versions half currently cannot fail, because that memo holds keys and
// re-parses them, so what it returns was never in the cache. It is kept anyway:
// it is the test that turns red the day someone memoizes the parsed values, and
// that day is coming -- see the upstream defect described on Versions.

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

// The two failures that are facts about the record are memoized alongside the
// successes, so the second call must classify identically. A resolution that
// backtracks asks about the same rejected version many times; if the memo lost
// the sentinel the provider would stop reporting "try another version" and
// start aborting the resolve.
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

// Run with -race. Both memos are written under memoMu while the underlying
// blob cache is written under mu, and the two are taken in sequence rather than
// nested, so this also exercises the ordering.
func TestMemoIsSafeUnderConcurrentUse(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	pkgs := []string{"flask", "padded", "nodeps", "ambiguous", "canonpref", "broken"}

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
			// Mutate the returned slice from many goroutines at once. If
			// Versions handed back the memo itself this is a data race on the
			// cached slice, and -race reports it.
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
