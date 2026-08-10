// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/posit-dev/go-python-packaging/version"
)

// cutoff is the snapshot instant used throughout these tests. Files are placed
// deliberately before, exactly on, and after it.
var cutoff = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// distFile builds a DistFile with the fields the filter actually reads.
// Everything else is left at its zero value on purpose: a filter that started
// depending on another field fails here rather than passing on incidental data.
func distFile(name string, uploaded time.Time, yanked bool) DistFile {
	return DistFile{
		Filename:   name,
		Location:   "https://example.invalid/" + name,
		Kind:       DistKindWheel,
		UploadTime: uploaded,
		Yanked:     yanked,
	}
}

// versionStrings renders and SORTS a version list. Sorting is the point: the
// interface promises no order, so an assertion that depended on one would be
// testing the implementation rather than the contract.
func versionStrings(vs []version.Version) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.String())
	}
	slices.Sort(out)
	return out
}

func filenames(files []DistFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Filename)
	}
	slices.Sort(out)
	return out
}

// --- FilterPolicy ---

// A String method that panics is worse than most panics: fmt recovers it and
// substitutes %!s(PANIC=...), so the defect is swallowed at exactly the call
// sites -- log lines, error messages -- most likely to reach it. That is how
// rstudio/package-manager#19466's F14 stayed hidden. So the zero value must be
// safe, and it must say something true.
func TestFilterPolicyStringZeroValueIsSafe(t *testing.T) {
	var p FilterPolicy
	got := p.String()
	if got != "no filtering" {
		t.Fatalf("zero FilterPolicy.String() = %q, want %q", got, "no filtering")
	}
}

func TestFilterPolicyString(t *testing.T) {
	tests := []struct {
		name   string
		policy FilterPolicy
		want   string
	}{
		{"prereleases", FilterPolicy{ExcludePrereleases: true}, "exclude-prereleases"},
		{"yanked", FilterPolicy{ExcludeYanked: true}, "exclude-yanked"},
		{"date", FilterPolicy{SnapshotDate: cutoff}, "snapshot-date=2026-03-01T12:00:00Z"},
		{
			"all",
			FilterPolicy{ExcludePrereleases: true, ExcludeYanked: true, SnapshotDate: cutoff},
			"exclude-prereleases,exclude-yanked,snapshot-date=2026-03-01T12:00:00Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A zero policy must be a pass-through wrapper. This is why the fields are
// named Exclude* rather than Allow*: someone who wraps an index to compose it
// must not silently lose every pre-release.
func TestFilteredIndexZeroPolicyIsPassThrough(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").
		AddVersion("flask", "3.0.0").
		AddVersion("flask", "3.1.0b1").
		AddFiles("flask", "3.0.0", distFile("a.whl", cutoff.Add(48*time.Hour), true))

	f := NewFilteredIndex(inner, FilterPolicy{})

	vs, err := f.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if got, want := versionStrings(vs), []string{"3.0.0", "3.1.0b1"}; !slices.Equal(got, want) {
		t.Fatalf("Versions = %v, want %v", got, want)
	}

	files, err := f.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("Files returned %d files, want 1 (a yanked, post-cutoff file must survive a zero policy)", len(files))
	}
}

// --- pre-release policy ---

func TestFilteredIndexExcludesPrereleases(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").
		AddVersion("flask", "3.0.0").
		AddVersion("flask", "3.1.0b1").
		AddVersion("flask", "3.1.0rc2").
		AddVersion("flask", "3.2.0.dev1").
		AddVersion("flask", "3.0.0.post1")

	f := NewFilteredIndex(inner, FilterPolicy{ExcludePrereleases: true})

	vs, err := f.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	// A post-release is NOT a pre-release; a dev release IS one, per PEP 440.
	want := []string{"3.0.0", "3.0.0.post1"}
	if got := versionStrings(vs); !slices.Equal(got, want) {
		t.Fatalf("Versions = %v, want %v", got, want)
	}
}

// The pre-release filter must be a hard filter, enforced on Metadata and Files
// too. A policy that only filtered the listing would be bypassed by any caller
// holding a version from elsewhere -- a pin, a lockfile, another index.
func TestFilteredIndexPrereleaseIsEnforcedOnMetadataAndFiles(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").
		AddVersion("flask", "3.1.0b1").
		AddFiles("flask", "3.1.0b1", distFile("beta.whl", cutoff.Add(-time.Hour), false))

	f := NewFilteredIndex(inner, FilterPolicy{ExcludePrereleases: true})
	beta := mustVersion(t, "3.1.0b1")

	_, err := f.Metadata(ctx, NewPackageName("flask"), beta)
	if !errors.Is(err, ErrMetadataUnavailable) {
		t.Fatalf("Metadata error = %v, want ErrMetadataUnavailable", err)
	}
	// ⚠️ Not ErrPackageNotFound: the package WAS found. Only the version is
	// excluded, and a caller branching on not-found would report a missing
	// package for a present one (rstudio/package-manager#19466 F12).
	if errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("Metadata error = %v, must not be ErrPackageNotFound", err)
	}

	_, err = f.Files(ctx, NewPackageName("flask"), beta)
	if !errors.Is(err, ErrMetadataUnavailable) {
		t.Fatalf("Files error = %v, want ErrMetadataUnavailable", err)
	}
}

// An unknown package must stay ErrPackageNotFound even when the version would
// also have been excluded by policy. The policy check runs AFTER the inner index
// establishes that the package exists, precisely so the more specific error
// cannot be overwritten by the less specific one.
func TestFilteredIndexUnknownPackageStaysNotFound(t *testing.T) {
	ctx := context.Background()
	f := NewFilteredIndex(NewMockIndex("inner"), FilterPolicy{ExcludePrereleases: true})

	_, err := f.Metadata(ctx, NewPackageName("ghost"), mustVersion(t, "1.0b1"))
	if !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("Metadata error = %v, want ErrPackageNotFound", err)
	}

	_, err = f.Files(ctx, NewPackageName("ghost"), mustVersion(t, "1.0b1"))
	if !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("Files error = %v, want ErrPackageNotFound", err)
	}

	_, err = f.Versions(ctx, NewPackageName("ghost"))
	if !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("Versions error = %v, want ErrPackageNotFound", err)
	}
}

// A known package all of whose versions the policy excludes is an EMPTY SLICE
// and a nil error -- not ErrPackageNotFound. The interface makes that
// distinction load-bearing: an unknown name is probably a typo, a known name
// with no acceptable version is a constraint conflict worth explaining.
func TestFilteredIndexAllVersionsExcludedIsEmptyNotNotFound(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").AddVersion("flask", "3.1.0b1")
	f := NewFilteredIndex(inner, FilterPolicy{ExcludePrereleases: true})

	vs, err := f.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 0 {
		t.Fatalf("Versions = %v, want empty", versionStrings(vs))
	}
}

// --- snapshot date and yanked policy ---

func TestFilteredIndexSnapshotDateFiltersFiles(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").AddFiles("flask", "3.0.0",
		distFile("before.whl", cutoff.Add(-time.Hour), false),
		distFile("exactly.whl", cutoff, false),
		distFile("after.whl", cutoff.Add(time.Hour), false),
	)
	// The cutoff is INCLUSIVE: a file uploaded at exactly the snapshot instant
	// existed as of that instant.
	f := NewFilteredIndex(inner, FilterPolicy{SnapshotDate: cutoff})

	files, err := f.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	want := []string{"before.whl", "exactly.whl"}
	if got := filenames(files); !slices.Equal(got, want) {
		t.Fatalf("Files = %v, want %v", got, want)
	}
}

// An unrecorded upload time is not the same fact as an upload time before the
// cutoff, and admitting it would let a file published yesterday into a snapshot
// dated last year -- invisibly. Excluding it is the answer that shows up.
func TestFilteredIndexSnapshotDateExcludesUnknownUploadTime(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").AddFiles("flask", "3.0.0",
		distFile("dated.whl", cutoff.Add(-time.Hour), false),
		distFile("undated.whl", time.Time{}, false),
	)
	f := NewFilteredIndex(inner, FilterPolicy{SnapshotDate: cutoff})

	files, err := f.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got, want := filenames(files), []string{"dated.whl"}; !slices.Equal(got, want) {
		t.Fatalf("Files = %v, want %v", got, want)
	}
}

func TestFilteredIndexExcludesYankedFiles(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").AddFiles("flask", "3.0.0",
		distFile("good.whl", cutoff, false),
		distFile("bad.whl", cutoff, true),
	)
	f := NewFilteredIndex(inner, FilterPolicy{ExcludeYanked: true})

	files, err := f.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got, want := filenames(files), []string{"good.whl"}; !slices.Equal(got, want) {
		t.Fatalf("Files = %v, want %v", got, want)
	}
}

// A version survives a file-level policy only if at least one of its files
// does. This is what makes the date and yank policies visible in the version
// listing rather than only at the file level.
func TestFilteredIndexVersionsDropsVersionsWithNoSurvivingFiles(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").
		AddFiles("flask", "3.0.0", distFile("old.whl", cutoff.Add(-time.Hour), false)).
		AddFiles("flask", "3.1.0", distFile("new.whl", cutoff.Add(time.Hour), false)).
		AddFiles("flask", "3.2.0",
			distFile("newer.whl", cutoff.Add(2*time.Hour), false),
			distFile("also-old.whl", cutoff.Add(-2*time.Hour), false),
		)
	f := NewFilteredIndex(inner, FilterPolicy{SnapshotDate: cutoff})

	vs, err := f.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	// 3.1.0 has nothing left; 3.2.0 keeps one file so the version survives.
	if got, want := versionStrings(vs), []string{"3.0.0", "3.2.0"}; !slices.Equal(got, want) {
		t.Fatalf("Versions = %v, want %v", got, want)
	}
}

// A release the policy emptied answers ErrMetadataUnavailable rather than an
// empty list, because returning empty would assert something false -- that the
// release ships no files -- and would disagree with what Metadata says about the
// same version.
func TestFilteredIndexEmptiedByPolicyIsRefusedNotEmpty(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").
		AddFiles("flask", "3.1.0", distFile("late.whl", cutoff.Add(time.Hour), false))
	f := NewFilteredIndex(inner, FilterPolicy{SnapshotDate: cutoff})

	_, err := f.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.1.0"))
	if !errors.Is(err, ErrMetadataUnavailable) {
		t.Fatalf("Files for a release the policy emptied: err = %v, want ErrMetadataUnavailable", err)
	}
}

// ⚠️ ALL THREE METHODS must agree about a known version with ZERO files under an
// active file-level policy, and the agreed answer is "refused".
//
// This pins a real inconsistency, found in review of the first version of this
// code. Files used a `len(kept) == 0 && len(files) > 0` guard, so a version with
// no files at all fell through and answered empty-and-nil -- while
// hasAdmissibleFile found nothing admissible, so Metadata refused it and
// Versions dropped it. The same (pkg, ver) was "exists, ships no files" through
// one method and "excluded by policy" through the other two.
//
// Refusing is the side that had to win, for three reasons:
//
//   - It is what the policy MEANS. "Has at least one file uploaded at or before
//     the cutoff" and "has at least one un-yanked file" are both false when
//     there is no file. A release with nothing in it cannot be shown to have
//     existed at a date, and admitting it under ExcludeYanked would admit it on
//     no evidence whatsoever.
//   - Otherwise the policy is bypassable, which is the entire reason it is
//     enforced on all three methods rather than only on Versions. A caller
//     holding a version from a pin or a lockfile and calling Files IS that
//     bypass.
//   - The old guard made the answer depend on whether the INNER index happened
//     to hold files before filtering -- an implementation detail invisible to
//     the caller. Two releases with zero admissible files answered differently.
//
// What is given up is only the zero-file case UNDER an active file-level policy;
// see the companion test for the distinction that survives.
func TestFilteredIndexZeroFileVersionIsRefusedByAllThreeMethods(t *testing.T) {
	ctx := context.Background()

	for name, policy := range map[string]FilterPolicy{
		"snapshot date": {SnapshotDate: cutoff},
		"yanked":        {ExcludeYanked: true},
	} {
		t.Run(name, func(t *testing.T) {
			// "empty" is registered with metadata but NO files at all; "full"
			// exists alongside it so the package is not trivially versionless.
			inner := NewMockIndex("inner").
				AddVersion("flask", "3.0.0").
				AddFiles("flask", "3.1.0", distFile("ok.whl", cutoff.Add(-time.Hour), false))
			f := NewFilteredIndex(inner, policy)
			empty := mustVersion(t, "3.0.0")

			// Versions: dropped.
			vs, err := f.Versions(ctx, NewPackageName("flask"))
			if err != nil {
				t.Fatalf("Versions: %v", err)
			}
			if got, want := versionStrings(vs), []string{"3.1.0"}; !slices.Equal(got, want) {
				t.Fatalf("Versions = %v, want %v: the zero-file version must be dropped", got, want)
			}

			// Metadata: refused, and as ErrMetadataUnavailable -- the package
			// WAS found, so ErrPackageNotFound would be untrue on its face.
			_, err = f.Metadata(ctx, NewPackageName("flask"), empty)
			if !errors.Is(err, ErrMetadataUnavailable) {
				t.Fatalf("Metadata: err = %v, want ErrMetadataUnavailable", err)
			}
			if errors.Is(err, ErrPackageNotFound) {
				t.Fatalf("Metadata: err = %v, must not be ErrPackageNotFound", err)
			}

			// Files: refused too. This is the assertion the old guard failed.
			files, err := f.Files(ctx, NewPackageName("flask"), empty)
			if !errors.Is(err, ErrMetadataUnavailable) {
				t.Fatalf("Files: err = %v (files = %v), want ErrMetadataUnavailable: "+
					"a file-level policy cannot admit a version it has no file to verify",
					err, filenames(files))
			}
		})
	}
}

// The distinction that SURVIVES the rule above: with no file-level policy
// active, filtersFiles() short-circuits and Files passes the inner answer
// through verbatim. So "this release genuinely ships no files" -- which the
// interface documents as empty-and-nil, since a release can have every file
// deleted -- is still expressible, and a FilteredIndex does not invent a refusal
// for it.
//
// A pre-release policy is active here to prove the short-circuit is about the
// FILE axes specifically, not about the policy being empty.
func TestFilteredIndexEmptyFilesSurviveWithoutAFileLevelPolicy(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").AddVersion("flask", "3.0.0") // registered, zero files

	for name, policy := range map[string]FilterPolicy{
		"zero policy":             {},
		"pre-release policy only": {ExcludePrereleases: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := NewFilteredIndex(inner, policy)

			files, err := f.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
			if err != nil {
				t.Fatalf("Files: err = %v, want nil: a release with no files is empty-and-nil", err)
			}
			if len(files) != 0 {
				t.Fatalf("Files = %v, want empty", filenames(files))
			}

			// And the version is still listed -- nothing about it was refused.
			vs, err := f.Versions(ctx, NewPackageName("flask"))
			if err != nil {
				t.Fatalf("Versions: %v", err)
			}
			if got, want := versionStrings(vs), []string{"3.0.0"}; !slices.Equal(got, want) {
				t.Fatalf("Versions = %v, want %v", got, want)
			}
		})
	}
}

// Metadata must agree with Versions under a file-level policy, or a caller that
// obtained a version some other way could route around the policy.
func TestFilteredIndexMetadataEnforcesFileLevelPolicy(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("inner").
		AddVersion("flask", "3.0.0").
		AddVersion("flask", "3.1.0").
		AddFiles("flask", "3.0.0", distFile("ok.whl", cutoff, false)).
		AddFiles("flask", "3.1.0", distFile("yanked.whl", cutoff, true))
	f := NewFilteredIndex(inner, FilterPolicy{ExcludeYanked: true})

	if _, err := f.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0")); err != nil {
		t.Fatalf("Metadata for an admitted version: %v", err)
	}

	_, err := f.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.1.0"))
	if !errors.Is(err, ErrMetadataUnavailable) {
		t.Fatalf("Metadata for a fully-yanked version: err = %v, want ErrMetadataUnavailable", err)
	}
}

// A file-level policy over an index that serves no files admits nothing. That
// behavior, and why it is no longer a hard error, is pinned by
// TestFilteredIndexFileLevelPolicyOverFilelessIndexAdmitsNothing in
// composition_test.go, alongside the partial-mirror case that made the hard
// error untenable.

// The same index with only the pre-release policy active never calls Files, so
// it composes over RSFIndex. That is the whole reason the file-level policies
// are evaluated lazily.
func TestFilteredIndexPrereleaseOnlyPolicyWorksOverFilelessIndex(t *testing.T) {
	ctx := context.Background()
	f := NewFilteredIndex(openFixtureIndex(t), FilterPolicy{ExcludePrereleases: true})

	vs, err := f.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) == 0 {
		t.Fatal("Versions returned nothing; the fixture has non-prerelease versions")
	}
	if _, err := f.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0")); err != nil {
		t.Fatalf("Metadata: %v", err)
	}
}

// Origin identifies where the data came from. A filter did not produce the
// record, so it must not relabel it -- otherwise a MultiIndex under a filter
// becomes undebuggable, which is the one job RFD 0001 gives the field.
func TestFilteredIndexPreservesOrigin(t *testing.T) {
	ctx := context.Background()
	inner := NewMockIndex("upstream").AddVersion("flask", "3.0.0")
	f := NewFilteredIndex(inner, FilterPolicy{ExcludePrereleases: true})

	meta, err := f.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.Origin != "upstream" {
		t.Fatalf("Origin = %q, want %q", meta.Origin, "upstream")
	}
}

func TestFilteredIndexHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := NewFilteredIndex(NewMockIndex("inner").AddVersion("flask", "3.0.0"), FilterPolicy{})

	if _, err := f.Versions(ctx, NewPackageName("flask")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Versions: err = %v, want context.Canceled", err)
	}
	if _, err := f.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Metadata: err = %v, want context.Canceled", err)
	}
	if _, err := f.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Files: err = %v, want context.Canceled", err)
	}
}

func TestNewFilteredIndexRejectsNilInner(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewFilteredIndex(nil, ...) did not panic")
		}
	}()
	NewFilteredIndex(nil, FilterPolicy{})
}
