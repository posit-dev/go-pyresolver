// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	rsf "github.com/rstudio/repository-snapshot-format"

	"github.com/posit-dev/go-pyresolver/pypirsf"
	"github.com/posit-dev/go-python-packaging/version"
)

// These cover what the parsed-version memo added: RSFIndex.Versions now hands
// every caller by-value copies of version.Version values held in a SHARED memo,
// so those values are read concurrently by every goroutine that resolves against
// one index.
//
// ⚠️ THIS FILE OVERLAPS index/shared_version_test.go HEAVILY, and the overlap is
// deliberate but not free. That file arrived with #44 and pins the same hazard
// one level down: that a Version parsed DIRECTLY is safe to share, which is gpp's
// guarantee. This one pins that RSFIndex.Versions actually hands that guarantee
// to concurrent callers -- the memo is the new thing, not the parse.
//
// The two fixture tables are the same four (shared, longer, packable) triples,
// chosen for the same reasons, and the two shape guards assert the same three
// properties. That is duplication, in one package, with nothing keeping them in
// step: an upstream packability change corrected in one table leaves the other
// asserting a stale path, and the failure mode is a green two-path test that
// covers one path. If a third file ever wants these fixtures, hoist the table and
// the guard into one place rather than copying them a third time.
//
// # The hazard this is shaped for
//
// Until go-python-packaging v0.6.0, sharing a parsed Version was a data race.
// rstudio/go-version's part.Parts.Normalize resliced the comparison key's release
// segment to drop trailing zeros, leaving len < cap, and part.Parts.Padding then
// appended into that spare capacity IN PLACE. A by-value copy of a Version copies
// the slice HEADER, so two goroutines comparing two copies wrote to the same
// backing array. v0.6.0 removes it twice over: a packable version never touches
// part.Parts at all, and the general path pads by COPYING (padParts).
//
// That fix is the whole licence for this memo, and this module's go.mod pin is
// what makes it true for this module's callers. These tests are what objects if
// that pin is ever walked back.
//
// ⚠️ THE HAZARD IS NARROW AND A CARELESS FIXTURE MISSES IT ENTIRELY. It fires
// only when both of these hold:
//
//  1. The shared operand carries spare capacity, which it acquires only where
//     trailing zeros were stripped. part.BigIntSliceToParts allocates exactly
//     len segments and Normalize reslices, so cap is the RAW segment count and
//     len is the stripped one. "3.11" has no spare capacity and is IMMUNE.
//     "3.11.0" strips to two segments with capacity for three and races.
//  2. The shared operand is the SHORTER of the pair, because that is the side
//     Padding grows.
//
// The padding DISTANCE does not have to fit in the spare capacity: Padding
// appends one element at a time, so the first append writes in place and only a
// later one reallocates. One stripped trailing zero is enough.
//
// The version keys below are chosen for that shape, and
// TestSharedMemoFixturesReachTheHazard asserts the shape rather than trusting
// this comment. A comment cannot stop someone adding "1.2.3" to the table later,
// and a green race run on an immune fixture says nothing at all.
//
// # What was verified against the old pin, where, and why you cannot just repeat it
//
// The COMPARISON half of every case here reported WARNING: DATA RACE with
// go-python-packaging pinned back to v0.5.0 -- 20 fresh processes per case, 80
// for 80.
//
// ⚠️ SCOPE, precisely, because "80 for 80" over-reads easily. That run was made
// at go-pyresolver 6c13230, on the version of this file that existed then: four
// fixtures, Compare and sort, and NO ReleaseKey. The ReleaseKey assertions were
// added later, when c006d47 made pep440set a second reader of a shared parsed
// Version, and they are NOT covered by that 80-for-80 -- they cannot be, since
// version.ReleaseKey does not exist before gpp v0.7.0. What backs the ReleaseKey
// half is gpp's own three-index slicing plus this test driving it under -race,
// not a demonstrated detection.
//
// ⚠️ AND THE RUN CANNOT BE REPEATED AS WRITTEN on the current base. Pinning back
// with `go mod edit -require=github.com/posit-dev/go-python-packaging@v0.5.0`
// fails to compile twice over: pep440set/bound.go and pep440set/verpos.go call
// version.ReleaseKey (four sites), and so does THIS FILE. Repeating it means
// reverting pep440set to its pre-c006d47 state AND stripping the ReleaseKey
// assertions below. That is the honest cost -- an instruction that looks
// executable and is not would be worse than saying so.
//
// ⚠️ Each of those runs was a SEPARATE PROCESS, and that is not a detail. Go's
// race detector deduplicates reports by stack within a process, so several
// subtests racing at the same line report once and stay quiet. Measured that way
// the same fixtures look flaky. They are not; the detector is deduplicating. One
// `go test -run .../<subtest> -count=1` per case, or the number is meaningless --
// I published a wrong claim from a single-process run before measuring this
// properly. See the note on what else objects, below.
//
// ⚠️ TestSharedMemoFixturesReachTheHazard FAILS under a v0.5.0 pin too, and for a
// reason unrelated to the race: v0.5.0 has no packed comparison path, so the
// packable case allocates and the guard says so. That is the guard working. Do
// not read it as a second race report.
//
// # Why WARM, and not just "run the existing concurrency test"
//
// index/rsfmemo_test.go's TestMemoIsSafeUnderConcurrentUse looks like it already
// covers this. It does not, and the way it fails to is worse than not covering it
// at all: every goroutine there calls Versions exactly once against a COLD memo,
// so most take the first-call path and build a plan nobody else holds, and
// whether any two goroutines ever share is a scheduling lottery.
//
// Measured on the tree with the defensive copy deleted, one fresh process per
// run: that test catches it 8 times in 20, this one 20 in 20. A test that notices
// a real defect two times in five is not a guard, it is a source of
// unreproducible CI failures. The memo here is warmed before any goroutine
// starts, so the sharing is guaranteed rather than raced for.
//
// # ⚠️ A SECOND reader of a shared parsed Version, as of c006d47
//
// Everything above is about Version.Compare, because until recently that was the
// only thing this module did with a parsed Version on a hot path. It is not any
// more: pep440set.verPos.init calls version.ReleaseKey on candidate versions, and
// those versions come from this memo, so ReleaseKey reads shared state too.
//
// It is safe -- gpp v0.7.0 clips the release slice it hands back (v.release[:n:n])
// for exactly this hazard -- but that is a property of the current pin rather
// than something this module gets to assume. The goroutines below therefore call
// ReleaseKey on the shared values as well as comparing them, so the second reader
// is exercised rather than reasoned about.

// memoShareCase is one fixture package whose two version keys form a
// (shared, longer) pair for the concurrent comparison below.
type memoShareCase struct {
	// pkg is the fixture package name, one per case so each pair sits in its
	// own memoized plan.
	pkg string

	// shared is the SHORTER key, the one padding would grow. It must end in a
	// zero release segment or the hazard is unreachable.
	shared string

	// longer has strictly more release segments than shared once trailing zeros
	// are stripped.
	longer string

	// packable records which of v0.6.0's two independent safety arguments this
	// case exercises. Asserted by allocation count, not assumed: see
	// TestSharedMemoFixturesReachTheHazard.
	packable bool

	// why records what this case covers that no other case covers.
	why string
}

var memoShareCases = []memoShareCase{
	{
		pkg:      "packableshare",
		shared:   "1.2.0",
		longer:   "1.2.3.4",
		packable: true,
		why: "The common case and the overwhelming majority of a real snapshot. " +
			"Comparison runs off the packed integer key and never reaches " +
			"part.Parts, so there is no slice to alias.",
	},
	{
		pkg:      "localshare",
		shared:   "1.2.0+shared",
		longer:   "1.2.3.4+other",
		packable: false,
		why: "A local version label disqualifies packing outright, so this pair " +
			"walks the general path and is safe only because padParts copies. " +
			"Roughly a quarter of distinct versions in a production snapshot " +
			"take that path, under an entirely separate safety argument, and a " +
			"table covering only packable versions would say nothing about it.",
	},
	{
		pkg:      "epochshare",
		shared:   "1!1.2.0",
		longer:   "1!1.2.3.4",
		packable: false,
		why: "A non-zero epoch is a second, independent disqualifier. It is here " +
			"because the local-label case would stop covering the fallback if " +
			"the packer ever learned to carry a local label, and one fixture " +
			"standing for a whole path is one fixture too few.",
	},
	{
		pkg:      "longshare",
		shared:   "1.2.3.4.5.6.7.0",
		longer:   "1.2.3.4.5.6.7.8.9",
		packable: false,
		why: "More than six release segments after stripping, the third " +
			"disqualifier, and the only case where the padding distance is " +
			"greater than a single segment.",
	},
}

// openShareFixtureIndex writes an RSF holding one package per memoShareCase and
// returns an index over it.
//
// Separate from openFixtureIndex rather than added to it: those packages are
// asserted on by name and by count across half a dozen files, and a fixture that
// serves two purposes ends up serving neither. Built with the same helpers.
func openShareFixtureIndex(t *testing.T) *RSFIndex {
	t.Helper()

	path := filepath.Join(t.TempDir(), "share.rsf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	w := rsf.NewWriter(f)
	for _, c := range memoShareCases {
		rec := pypirsf.PackageRecord{
			CanonicalName: c.pkg,
			ProjectName:   c.pkg,
			Snapshots: []pypirsf.SnapshotRecord{
				{Snapshot: "2026080100", Version: c.shared, ReleaseDate: "\x00\x01", Summary: "x"},
			},
			Deps: buildStoredDepsField([]fixtureVersion{
				{version: c.shared, requiresPython: ">=3.9"},
				{version: c.longer, requiresPython: ">=3.9"},
			}),
			Depsdict: buildDepsdictField(),
		}
		if _, err := w.WriteObject(rec); err != nil {
			t.Fatalf("writing %s: %v", c.pkg, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing fixture: %v", err)
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("pypirsf.Open: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	idx, err := NewRSFIndex(file, "share-rsf")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}
	return idx
}

// strippedReleaseLen returns how many release segments s has once the epoch, the
// pre/post/dev suffix and the local label are removed and trailing zeros are
// stripped -- which is what the comparison key does, and where the spare capacity
// the hazard needs comes from.
//
// It reads the FIXTURE STRING rather than asking the library, deliberately. What
// is being guarded is that the test data still has the shape the hazard needs,
// which is a question about the data and not about the code under test. The
// fixtures are simple enough that this does not have to be a PEP 440 parser.
func strippedReleaseLen(s string) int {
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
	segs := strings.Split(s, ".")
	for len(segs) > 1 && segs[len(segs)-1] == "0" {
		segs = segs[:len(segs)-1]
	}
	return len(segs)
}

// endsInZeroSegment reports whether s's release ends in a "0" segment, which is
// what leaves spare capacity behind when the comparison key strips it.
func endsInZeroSegment(s string) bool {
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
	return strings.HasSuffix(s, ".0")
}

// TestSharedMemoFixturesReachTheHazard is the anti-vacuity guard for
// TestSharedMemoizedVersionsAreRaceFree. A race test on immune fixtures passes
// while checking nothing, and nothing in the race test itself would say so.
//
// It asserts three things a green -race run cannot:
//
//  1. Every shared key ends in a zero release segment, so it carries the spare
//     capacity without which the hazard is unreachable.
//
//  2. Every shared key is strictly the SHORTER operand, so it is the side
//     padding would have grown.
//
//  3. Each case takes the comparison path it claims to. The packed path
//     allocates NOTHING and the general path allocates, so allocation is a
//     public-API discriminator between v0.6.0's two separate safety arguments:
//     "the fallback is covered too" is measured rather than recalled from a
//     version string and a memory of the rules, and it fails loudly if a fixture
//     silently changes paths on an upstream bump, which is how a two-path test
//     decays into a one-path test.
//
//     ⚠️ The general path's allocations are NOT padParts copying, and an earlier
//     draft of this comment said they were. Measured: a general-path pair whose
//     release lengths already match, so padParts does nothing, still allocates 17
//     per comparison against the padding pair's 18. They come from key.compare
//     building a part.Parts and boxing each field into an interface. The
//     discriminator is sound -- zero allocations still means the packed path --
//     but do not read the count as a measure of the copy.
func TestSharedMemoFixturesReachTheHazard(t *testing.T) {
	var packable, unpackable int

	for _, c := range memoShareCases {
		t.Run(c.pkg, func(t *testing.T) {
			if !endsInZeroSegment(c.shared) {
				t.Fatalf("shared key %q does not end in a zero release segment, so it carries "+
					"no spare capacity and cannot exercise the padding hazard; see the note "+
					"at the top of this file (%s)", c.shared, c.why)
			}
			ls, ll := strippedReleaseLen(c.shared), strippedReleaseLen(c.longer)
			if ls >= ll {
				t.Fatalf("shared key %q strips to %d release segments and %q to %d: shared must "+
					"be strictly SHORTER or it is not the operand that gets padded",
					c.shared, ls, c.longer, ll)
			}

			short := mustVersion(t, c.shared)
			long := mustVersion(t, c.longer)
			allocs := testing.AllocsPerRun(100, func() {
				_ = short.Compare(long)
			})

			switch {
			case c.packable && allocs != 0:
				t.Errorf("%q vs %q allocated %.0f per comparison; a packable pair compares off "+
					"the packed integer key and must allocate nothing. This case no longer "+
					"covers the packed path.", c.shared, c.longer, allocs)
				packable++
			case !c.packable && allocs == 0:
				t.Errorf("%q vs %q allocated nothing per comparison, so it took the PACKED path "+
					"and not the padParts fallback it is here to cover (%s)",
					c.shared, c.longer, c.why)
				unpackable++
			case c.packable:
				packable++
			default:
				unpackable++
			}
		})
	}

	// Both arguments must actually be exercised. Without this, deleting every
	// unpackable case leaves a suite that is green and covers half the fix.
	if packable == 0 || unpackable == 0 {
		t.Errorf("the table covers %d packable and %d unpackable cases; v0.6.0 makes TWO "+
			"separate safety arguments and both need a case", packable, unpackable)
	}
}

// TestSharedMemoizedVersionsAreRaceFree shares one memoized parse across
// goroutines and compares it, which is exactly what a concurrent resolver does
// through the index now that Versions answers from a parsed memo.
//
// The memo is WARMED first. That is the whole difference between this and
// TestMemoIsSafeUnderConcurrentUse: after the warm-up every Versions call is a
// copy of one stored slice, so every goroutine holds a by-value copy of the SAME
// parsed version.Version and the aliasing is guaranteed rather than raced for.
//
// ⚠️ RUN UNDER -race. Without it this test asserts NOTHING about the hazard: the
// racing writes store the same bytes at the same address, so the answers stay
// right and no other check can see them. Its entire value is in the detector.
//
// CI runs it: .github/workflows/ci.yml has a `Race` step as of #44. That is what
// makes this file worth anything on a pull request, and it is recent -- before
// #44 the workflow ran plain `go test ./...`, under which these tests are a green
// no-op. Measured, on a tree with the defensive copy deleted:
// TestMemoIsSafeUnderConcurrentUse catches it 8 times in 20 under -race and 0
// times in 20 without it. If that step is ever removed, this file stops being a
// guard and nothing else in the module will say so.
//
// Confirmed to report WARNING: DATA RACE with go-python-packaging pinned back to
// v0.5.0, for all four cases, 20 fresh processes each -- 20 out of 20 every time.
// Without that confirmation this test is a hypothesis. See the note at the top of
// this file for where that was run and why it cannot simply be repeated.
//
// ⚠️ WHAT ELSE OBJECTS, corrected twice. An earlier draft said this was the only
// test in the module that objects to a v0.5.0 pin. That was wrong, and wrong
// because it came from a SINGLE process -- the exact trap the note at the top of
// this file warns other people about. Measured properly at 6c13230, 20 fresh
// processes each, defensive copy intact:
//
//	TestSharedMemoizedVersionsAreRaceFree      20/20
//	TestMemoIsSafeUnderConcurrentUse            8/20
//	TestVersionsMemoIsNotAliasedByTheCaller      0/20
//
// ⚠️ shared_version_test.go OBJECTS TOO, and a second draft of this list omitted
// it. TestSharedParsedVersionIsRaceFree and TestSupportsPythonSharedTargetIsRaceFree
// arrived with #44 for the same hazard one level down -- a Version parsed
// directly rather than served from this memo -- and their own doc records the
// same v0.5.0 result. They are not redundant with this file and this file is not
// redundant with them: they pin gpp's guarantee, this pins that RSFIndex.Versions
// hands that guarantee to concurrent callers.
//
// resolver/concurrency_test.go, provider and candidate pass. ⚠️ pep440set passed
// at 6c13230 and does not COMPILE under a v0.5.0 pin on the current base, which
// is a different statement about a different tree; see mock.go, whose note is
// about the earlier one.
//
// The 8/20 is consistent with, not contrary to, the account of that test above:
// it shares memoized values only when the scheduler happens to let two goroutines
// past the first-call path, so it reaches the hazard sometimes. A test that
// reports a real data race in 8 runs out of 20 is not a second guard, it is a
// flake generator -- which is the argument for this test existing, not against
// it.
//
// ⚠️ Verifying that yourself needs `-timeout` raised: under a v0.5.0 pin the
// provider and candidate suites take about 50 s each under -race, and running
// several packages in one `go test` invocation on a busy machine trips the
// default 10-minute bound. A timeout there is not a race report.
func TestSharedMemoizedVersionsAreRaceFree(t *testing.T) {
	const goroutines = 8

	idx := openShareFixtureIndex(t)
	ctx := context.Background()

	for _, c := range memoShareCases {
		t.Run(c.pkg, func(t *testing.T) {
			pkg := NewPackageName(c.pkg)

			// Warm the memo, and check that it is warm rather than assuming so.
			// If the plan were absent the goroutines below would each build
			// their own and share nothing, which is the failure mode this test
			// exists to avoid in the first place.
			warm, err := idx.Versions(ctx, pkg)
			if err != nil {
				t.Fatalf("Versions(%s): %v", pkg, err)
			}
			if len(warm) != 2 {
				t.Fatalf("fixture %s gave %d versions, want 2 (%v)", pkg, len(warm), renderVersions(warm))
			}
			idx.memoMu.RLock()
			plan, ok := idx.versionList[pkg]
			idx.memoMu.RUnlock()
			if !ok {
				t.Fatalf("the memo holds no plan for %s after a Versions call, so the "+
					"goroutines below would not share anything", pkg)
			}
			if len(plan.versions) != len(plan.order) {
				t.Fatalf("plan for %s holds %d parsed versions against %d keys; the two are "+
					"parallel by construction", pkg, len(plan.versions), len(plan.order))
			}
			// The returned slice must be a copy, or the goroutines below would
			// be racing on the slice rather than on the values inside it, and
			// this test would be measuring the wrong thing.
			if &warm[0] == &plan.versions[0] {
				t.Fatalf("Versions returned the memo's own slice for %s; this test needs a "+
					"copy so that what is shared is the parsed VALUES", pkg)
			}

			// The answer every goroutine must agree on. Computed once, before
			// any concurrency, from values the memo has not yet handed out
			// twice.
			want := plan.versions[0].Compare(plan.versions[1])
			if want == 0 {
				t.Fatalf("fixture %s: %q and %q compare equal, so this case cannot detect a "+
					"corrupted pad", pkg, c.shared, c.longer)
			}
			// The same answer through the second reader. Not asserted equal to
			// want: a ReleaseKey drops the pre/post/dev/local components, so two
			// versions that differ only there share a release key. These fixtures
			// differ in the release segment, so the two agree -- and that is
			// checked here rather than assumed, because a fixture added later
			// might not.
			wantRel := plan.versions[0].ReleaseKey().Compare(plan.versions[1].ReleaseKey())
			if wantRel == 0 {
				t.Fatalf("fixture %s: %q and %q share a release key, so the ReleaseKey check "+
					"below cannot detect a corrupted release segment", pkg, c.shared, c.longer)
			}

			var (
				wg    sync.WaitGroup
				start = make(chan struct{})
			)
			for range goroutines {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start

					vers, err := idx.Versions(ctx, pkg)
					if err != nil {
						t.Errorf("Versions(%s): %v", pkg, err)
						return
					}

					// Repeated because the write the detector is looking for
					// happens inside Compare's padding step, and one comparison
					// per goroutine gives it a narrow window.
					for range 200 {
						if got := vers[0].Compare(vers[1]); got != want {
							t.Errorf("%s: %q.Compare(%q) = %d, want %d -- a shared release "+
								"segment was padded from under this goroutine",
								pkg, c.shared, c.longer, got, want)
							return
						}

						// The OTHER reader of a shared parsed Version, as of
						// c006d47: pep440set.verPos.init takes this of every
						// candidate version, and its candidates come from this
						// memo. Driven here so that reader is under the detector
						// too, rather than resting on the argument that gpp
						// clips what it returns.
						//
						// Compared through ReleaseKey.Compare, which is the only
						// exported way to observe one: the two keys must order
						// the same way the versions do, every time.
						if got := vers[0].ReleaseKey().Compare(vers[1].ReleaseKey()); got != wantRel {
							t.Errorf("%s: ReleaseKey(%q).Compare(ReleaseKey(%q)) = %d, want %d "+
								"-- a shared release segment moved under this goroutine",
								pkg, c.shared, c.longer, got, wantRel)
							return
						}
					}

					// And sort the returned slice, which is what
					// cmd/pyresolve's `versions` subcommand does. Under a
					// missing defensive copy every goroutine would be sorting
					// one shared array.
					sort.Sort(sort.Reverse(version.SortedVersions(vers)))
				}()
			}
			close(start)
			wg.Wait()

			// The memo must be untouched by all of that.
			after, err := idx.Versions(ctx, pkg)
			if err != nil {
				t.Fatalf("Versions(%s) after the concurrent phase: %v", pkg, err)
			}
			if got, want := renderVersions(after), renderVersions(warm); !equalStrings(got, want) {
				t.Errorf("the memo changed under concurrent callers:\n got %v\nwant %v", got, want)
			}
		})
	}
}

// TestVersionsNeverReturnsTheMemosSlice checks slice identity on BOTH paths.
//
// TestVersionsMemoIsNotAliasedByTheCaller covers the warm path behaviourally, by
// mutating and re-reading. This covers the FIRST call as well, which that test
// cannot reach: the first call holds the very slice it just stored, so returning
// it directly would leave exactly one caller per package able to corrupt the memo
// -- a bug that reproduces once per process and never again.
func TestVersionsNeverReturnsTheMemosSlice(t *testing.T) {
	idx := openShareFixtureIndex(t)
	ctx := context.Background()
	pkg := NewPackageName(memoShareCases[0].pkg)

	first, err := idx.Versions(ctx, pkg)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}

	idx.memoMu.RLock()
	plan := idx.versionList[pkg]
	idx.memoMu.RUnlock()
	if len(plan.versions) == 0 {
		t.Fatal("the memo holds no parsed versions, so there is no identity to check")
	}
	if &first[0] == &plan.versions[0] {
		t.Error("the FIRST call returned the memo's own slice; a caller sorting it corrupts " +
			"the cache for every later caller")
	}

	second, err := idx.Versions(ctx, pkg)
	if err != nil {
		t.Fatalf("Versions (second call): %v", err)
	}
	if &second[0] == &plan.versions[0] {
		t.Error("a warm call returned the memo's own slice")
	}
	if &second[0] == &first[0] {
		t.Error("two calls returned the same slice")
	}
}

// TestVersionPlanHalvesStayParallel enforces the invariant everything above rests
// on: plan.versions[i] is version.Parse(plan.order[i]), same length, same order.
//
// ⚠️ Until this existed, that invariant was held up by a comment ("keep the two
// appends adjacent") and by the two appends happening to sit next to each other
// in computeVersionOrder. Nothing checked it. A plan built by any other path --
// a future partial construction, or a versionPlan{order: ...} literal -- would
// make Versions return an empty NON-NIL slice, which reads as "this package has
// no versions", with no error and nothing red. That is the worst available
// failure mode: a wrong answer that is indistinguishable from a right one.
//
// Element-wise, not just by length, because a length check passes on two lists
// that have drifted out of order -- which is the shape a dedup or sort change
// would produce.
func TestVersionPlanHalvesStayParallel(t *testing.T) {
	for _, idx := range []*RSFIndex{openShareFixtureIndex(t), openFixtureIndex(t)} {
		for _, name := range idx.file.Packages() {
			pkg := NewPackageName(name)
			if _, err := idx.Versions(context.Background(), pkg); err != nil {
				t.Fatalf("Versions(%s): %v", pkg, err)
			}

			idx.memoMu.RLock()
			plan := idx.versionList[pkg]
			idx.memoMu.RUnlock()

			if len(plan.versions) != len(plan.order) {
				t.Fatalf("%s: plan holds %d parsed versions against %d keys",
					pkg, len(plan.versions), len(plan.order))
			}
			for i, key := range plan.order {
				want, err := version.Parse(key)
				if err != nil {
					t.Fatalf("%s: stored key %q in the order does not parse: %v", pkg, key, err)
				}
				if !plan.versions[i].Equal(want) {
					t.Errorf("%s: plan.versions[%d] is %s but plan.order[%d] is %q; the two halves "+
						"of a versionPlan must be parallel, and Versions serves from the first "+
						"while Metadata searches the second", pkg, i, plan.versions[i], i, key)
				}
			}
		}
	}
}

// TestVersionsIsEmptyNotNilWhenNothingParses pins the shape Versions has always
// answered with, which the memo must not quietly change.
//
// A package whose every stored key PEP 440 rejects comes back as an empty NON-NIL
// slice. It was non-nil because the pre-memo code built it with make; it stays
// non-nil because the copy does too. slices.Clone would propagate nil here and a
// caller branching on nil would start seeing a different answer.
func TestVersionsIsEmptyNotNilWhenNothingParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unparseable.rsf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	w := rsf.NewWriter(f)
	rec := pypirsf.PackageRecord{
		CanonicalName: "allbroken",
		ProjectName:   "AllBroken",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "not-a-version", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "not-a-version"},
			{version: "also-not-a-version"},
		}),
		Depsdict: buildDepsdictField(),
	}
	if _, err := w.WriteObject(rec); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing fixture: %v", err)
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("pypirsf.Open: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	idx, err := NewRSFIndex(file, "unparseable-rsf")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}

	for _, call := range []string{"first", "memoized"} {
		vers, err := idx.Versions(context.Background(), NewPackageName("allbroken"))
		if err != nil {
			t.Fatalf("Versions (%s call): %v", call, err)
		}
		if vers == nil {
			t.Errorf("the %s call returned a nil slice; Versions has always answered an "+
				"empty NON-NIL slice for a package whose every key is unparseable", call)
		}
		if len(vers) != 0 {
			t.Errorf("the %s call returned %d versions, want 0", call, len(vers))
		}
	}
}
