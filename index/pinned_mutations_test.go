// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
	rsf "github.com/rstudio/repository-snapshot-format"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// The tests here pin behaviours that a mutation of the source could change
// without any existing test noticing (rstudio/package-manager#19466's surviving
// mutations). Each one was verified by applying the mutation it describes,
// watching this test fail, and reverting.
//
// Some mutations in this package are NOT pinned here, deliberately, because no
// test can distinguish them. They are recorded at the bottom of this file so the
// next reader does not spend the afternoon rediscovering it.

// openIndexOverRecords writes recs to an RSF and returns an index over it.
//
// openFixtureIndex builds one specific corpus and is shared by most of this
// package's tests; a couple of properties need a corpus of their own, and
// growing that one fixture would retarget the tests already asserting against
// it.
func openIndexOverRecords(t *testing.T, recs []pypirsf.PackageRecord) *RSFIndex {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fixture.rsf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	w := rsf.NewWriter(f)
	for _, rec := range recs {
		if _, err := w.WriteObject(rec); err != nil {
			t.Fatalf("writing %s: %v", rec.CanonicalName, err)
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

	idx, err := NewRSFIndex(file, "test-rsf")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}
	return idx
}

// unequalRenderingClassFixture builds ONE package whose two stored keys are PEP
// 440-equal, disagree about which is canonical, AND render differently once
// parsed.
//
// That third property is what makes the representative rule observable through
// Versions at all, and it is why the existing "canonpref" fixture cannot see it:
// its keys are "1.0.0" and "01.0.0", which parse to the same value and therefore
// render identically no matter which one wins.
//
// Here the keys are "1.0" (canonical: it round-trips through normalization) and
// "01.0.0" (not canonical: it normalizes to "1.0.0"). They compare EQUAL under
// PEP 440, which pads the shorter release segment with zeros, but the versions
// they parse to render as "1.0" and "1.0.0". So the version Versions reports is
// direct evidence of which key it treated as the representative.
//
// ⚠️ The two halves of the rule disagree on this input, which is the point:
// "01.0.0" sorts FIRST lexicographically, so an implementation that has lost its
// canonical-spelling preference reports "1.0.0" instead of "1.0".
func unequalRenderingClassFixture(t *testing.T) *RSFIndex {
	t.Helper()

	rec := pypirsf.PackageRecord{
		CanonicalName: "rendersplit",
		ProjectName:   "RenderSplit",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "01.0.0", requiresDist: []string{"noncanonical"}},
			{version: "1.0", requiresDist: []string{"canonical"}},
		}),
		Depsdict: buildDepsdictField(),
	}

	return openIndexOverRecords(t, []pypirsf.PackageRecord{rec})
}

// TestClassKeysCompareEqualButRenderDifferently checks the fixture's premise
// before anything is concluded from it.
//
// If PEP 440 ever stopped treating "1.0" and "01.0.0" as the same version, the
// test below would still pass — for the wrong reason, since there would be no
// equality class left to choose a representative from. Asserting the premise is
// what keeps that from turning into a silent loss of coverage.
func TestClassKeysCompareEqualButRenderDifferently(t *testing.T) {
	short, err := version.Parse("1.0")
	if err != nil {
		t.Fatalf("Parse(1.0): %v", err)
	}
	padded, err := version.Parse("01.0.0")
	if err != nil {
		t.Fatalf("Parse(01.0.0): %v", err)
	}

	if !short.Equal(padded) {
		t.Fatalf("%q and %q must compare EQUAL under PEP 440 for the representative "+
			"rule to be observable through Versions", short, padded)
	}
	if short.String() == padded.String() {
		t.Fatalf("both keys render as %q, so which one won cannot be seen in Versions output",
			short)
	}
}

// TestVersionsReportsTheCanonicalKeysVersion pins WHICH member of an equality
// class Versions reports, through the only channel that can show it: the
// rendered version.
//
// The existing dedupe tests establish that a class collapses to one version and
// that Metadata resolves the same record. Neither can see this, because both are
// blind to a representative choice that does not change the rendering — the
// canonical-preference half of the rule can be deleted and they still pass.
//
// Mutation pinned: labelling every stored key as canonical (`canonical: true` in
// Versions), which is equivalent to dropping the preference and deciding the
// class lexicographically.
func TestVersionsReportsTheCanonicalKeysVersion(t *testing.T) {
	idx := unequalRenderingClassFixture(t)

	vers, err := idx.Versions(context.Background(), NewPackageName("rendersplit"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vers) != 1 {
		t.Fatalf("Versions returned %d versions, want 1 (the class must collapse): %v", len(vers), vers)
	}
	if got := vers[0].String(); got != "1.0" {
		t.Errorf("Versions reported %q; want %q, the version of the canonically spelled key "+
			"(%q sorts first lexicographically, so %q means the canonical preference is gone)",
			got, "1.0", "01.0.0", "1.0.0")
	}
}

// TestVersionsAndMetadataAgreeWhenRenderingsDiffer is the coherence half of the
// same property, on the fixture that can actually expose it.
//
// The representative Versions hands out has to resolve, through Metadata, to the
// record the collapse treated as authoritative. Here the two keys carry
// DIFFERENT dependencies, so a disagreement is visible as a dependency name
// rather than as an error — the failure mode that raises nothing at all.
func TestVersionsAndMetadataAgreeWhenRenderingsDiffer(t *testing.T) {
	idx := unequalRenderingClassFixture(t)
	ctx := context.Background()

	vers, err := idx.Versions(ctx, NewPackageName("rendersplit"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vers) != 1 {
		t.Fatalf("Versions returned %d versions, want 1: %v", len(vers), vers)
	}

	meta, err := idx.Metadata(ctx, NewPackageName("rendersplit"), vers[0])
	if err != nil {
		t.Fatalf("Metadata(%s): %v", vers[0], err)
	}
	if len(meta.RequiresDist) != 1 {
		t.Fatalf("Metadata returned %d requirements, want 1: %v", len(meta.RequiresDist), meta.RequiresDist)
	}
	if got := meta.RequiresDist[0].Name; got != "canonical" {
		t.Errorf("Metadata for the representative %s resolved the %q record; the version "+
			"Versions reported and the record Metadata found must come from the same stored key",
			vers[0], got)
	}
}

// TestUnparseableVersionKeysAreSorted pins the ordering UnparseableVersionKeys
// documents.
//
// It matters more than a cosmetic sort: the keys are collected by iterating a
// map, Go randomizes that order, and this value is printed by `pyresolve
// versions` and read by humans comparing two runs. Unsorted, the same file
// reports its keys in a different order every invocation.
//
// Six keys are used so an accidentally-sorted map iteration is not a plausible
// explanation for a pass: there are 720 orderings and only one of them is sorted.
//
// Mutation pinned: removing sort.Strings from UnparseableVersionKeys.
func TestUnparseableVersionKeysAreSorted(t *testing.T) {
	keys := []string{
		"0.2.1.Perceval",
		"0-0-1",
		"0.3.9-1-gc1f9c92",
		"0.5.0-2-gea64e46",
		"0.0.1-",
		"2.0.0.Charlemagne",
	}
	fvs := make([]fixtureVersion, 0, len(keys))
	for _, k := range keys {
		fvs = append(fvs, fixtureVersion{version: k, requiresDist: []string{"sqlobject"}})
	}

	idx := openIndexOverRecords(t, []pypirsf.PackageRecord{{
		CanonicalName: "manybadkeys",
		ProjectName:   "ManyBadKeys",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: keys[0], ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps:     buildStoredDepsField(fvs),
		Depsdict: buildDepsdictField(),
	}})

	bad, err := idx.UnparseableVersionKeys(context.Background(), NewPackageName("manybadkeys"))
	if err != nil {
		t.Fatalf("UnparseableVersionKeys: %v", err)
	}
	if len(bad) != len(keys) {
		t.Fatalf("got %d unparseable keys, want %d: %v", len(bad), len(keys), bad)
	}
	if !sort.StringsAreSorted(bad) {
		t.Errorf("UnparseableVersionKeys returned %v, which is not sorted; the order is "+
			"map iteration order and changes between runs", bad)
	}
}

// TestEmptySpecifierSetAdmitsEveryInterpreter records WHY SupportsPython's
// empty-set shortcut cannot be pinned by a test of this package.
//
// Deleting the shortcut changes nothing today: go-python-packaging made an empty
// specifier set admit every version in v0.3.0, so the fallthrough to Check gives
// the same answer. The shortcut is a guard against that regressing — it was
// written when Check returned false for an empty set, inverting the meaning of
// the zero value, which is over two million versions in a production snapshot.
//
// So this test pins the UPSTREAM behaviour the guard is redundant with, rather
// than pretending to pin the branch. If it ever fails, the guard is load-bearing
// again and SupportsPython is the only reason callers are unaffected.
func TestEmptySpecifierSetAdmitsEveryInterpreter(t *testing.T) {
	target, err := version.Parse("3.11")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var empty version.Specifiers
	if !empty.Check(target) {
		t.Errorf("go-python-packaging: an empty specifier set rejected %s. PackageMetadata."+
			"SupportsPython's empty-set shortcut is now the only thing keeping an absent "+
			"Requires-Python from excluding every interpreter", target)
	}
}

// --- mutations in this package that no test can pin, and why ---
//
// Recorded rather than papered over with a test that appears to cover them.
//
//  1. Versions' cross-class ordering (LessThan -> GreaterThan in the sort
//     comparator). MetadataIndex.Versions promises NO order, and MockIndex has a
//     test asserting it does not sort, so a test pinning ascending order here
//     would contradict the interface it implements. The SET is unchanged: an
//     equality class stays contiguous under either direction, so dedup and the
//     representative choice are unaffected.
//
//     ⚠️ It forms a MUTUALLY MASKING PAIR with `pyresolve versions`' own
//     sort.Sort call. Reversing this comparator is invisible because the command
//     re-sorts; removing the command's sort is invisible because this comparator
//     already returns ascending order. Measured: each alone leaves the suite
//     green, and applying BOTH fails TestVersionsCmdSortedAscending. The printed
//     order is pinned, which is the layer that promises one; neither half can be
//     pinned separately without asserting an order the interface disclaims.
//
//  2. RSFIndex.deps' cache-race branch (keeping the map that landed first when
//     two goroutines decode the same package). Both maps are decodes of the same
//     bytes, so they are equal; only their identity differs, and every exported
//     method copies out of them. The branch exists so all callers share ONE
//     object, which is a memory property rather than an observable answer.
//
//  3. SupportsPython's empty-set shortcut. See
//     TestEmptySpecifierSetAdmitsEveryInterpreter above.
