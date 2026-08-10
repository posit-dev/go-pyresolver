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

// FilteredIndex over MultiIndex is the composition RFD 0001 Section 6 describes
// and the only one in which a file-level policy is expressible over an RSF. It
// had no coverage, and that gap hid three defects at once: a legitimate partial
// mirror hard-errored, ErrPackageNotFound became unreachable from
// MultiIndex.Files, and the "fails rather than answering" property several doc
// comments promised turned out to depend on the data.
//
// These tests exercise the composed stack rather than either type alone.

// partialMirror builds the RFD 0009 partial-mirror shape: a fileless source that
// carries dependency metadata for everything, plus a file-serving source that
// holds only some of it.
//
// The RSF fixture knows flask 3.0.0, 3.0.1 and 3.0.2 and serves no files at all.
func partialMirror(t *testing.T, mirrored ...string) *MultiIndex {
	t.Helper()

	mirror := NewMockIndex("mirror")
	for _, ver := range mirrored {
		mirror.AddFiles("flask", ver, distFile("flask-"+ver+".whl", cutoff.Add(-time.Hour), false))
	}
	return NewMultiIndex(openFixtureIndex(t), mirror)
}

// ⚠️ A partial mirror must NOT hard-error. The operator composed exactly what
// the documentation told them to -- a fileless RSF paired with a file-serving
// source -- so a failure whose message says "compose the policy over an index
// that serves files" would be telling them to do what they already did.
//
// This is reachable only through the composition: MultiIndex.Files emits
// ErrFilesUnavailable when a fileless source is present and the file-serving
// source simply did not have the package, so FilteredIndex cannot read that
// sentinel as a statement about the whole inner index.
func TestFilteredOverMultiIndexPartialMirrorDoesNotHardError(t *testing.T) {
	ctx := context.Background()

	// The mirror holds 3.0.0 only. 3.0.1 and 3.0.2 have no file evidence.
	f := NewFilteredIndex(partialMirror(t, "3.0.0"), FilterPolicy{SnapshotDate: cutoff})

	versions, err := f.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions on a partial mirror: %v\n"+
			"a legitimately composed partial mirror must not fail", err)
	}
	if got, want := versionStrings(versions), []string{"3.0.0"}; !slices.Equal(got, want) {
		t.Fatalf("Versions = %v, want %v", got, want)
	}

	// And the mirrored version resolves end to end: dependency metadata from the
	// RSF, file evidence from the mirror.
	meta, err := f.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata for the mirrored version: %v", err)
	}
	if len(meta.RequiresDist) == 0 {
		t.Fatal("Metadata carried no requirements; it should have come from the RSF")
	}
}

// ⚠️ THE CONSISTENCY THAT WAS BROKEN. "No file evidence for this version" must
// resolve the same way whether the file-serving source fails to recognise the
// package NAME or merely fails to recognise the VERSION.
//
// Those two states are the same semantic fact, and they used to be handled
// oppositely: an unrecognised name produced ErrFilesUnavailable from MultiIndex
// and a hard error from FilteredIndex, while an unrecognised version produced
// ErrMetadataUnavailable and a silent drop. Whether an operator got a fatal
// error or a quietly shorter version list depended on something with no bearing
// on the question.
func TestFilteredOverMultiIndexTreatsUnknownNameAndUnknownVersionAlike(t *testing.T) {
	ctx := context.Background()
	pkg := NewPackageName("flask")
	policy := FilterPolicy{SnapshotDate: cutoff}

	// (a) The mirror has never heard of flask at all.
	unknownName := NewFilteredIndex(partialMirror(t), policy)

	// (b) The mirror knows flask, but only a version the RSF does not carry, so
	// every version the RSF lists is unknown to the mirror.
	//
	// That extra version's own file is deliberately POST-cutoff, so the policy
	// drops it on its own merits. Otherwise it would join the union with valid
	// file evidence and survive -- correctly, but it would stop this test from
	// isolating the state it is about.
	mirror := NewMockIndex("mirror").
		AddFiles("flask", "99.0", distFile("flask-99.0.whl", cutoff.Add(time.Hour), false))
	unknownVersion := NewFilteredIndex(NewMultiIndex(openFixtureIndex(t), mirror), policy)

	for name, f := range map[string]*FilteredIndex{
		"mirror does not know the name":    unknownName,
		"mirror does not know the version": unknownVersion,
	} {
		t.Run(name, func(t *testing.T) {
			versions, err := f.Versions(ctx, pkg)
			if err != nil {
				t.Fatalf("Versions: %v, want nil error", err)
			}
			if len(versions) != 0 {
				t.Fatalf("Versions = %v, want empty", versionStrings(versions))
			}

			_, err = f.Metadata(ctx, pkg, mustVersion(t, "3.0.0"))
			if !errors.Is(err, ErrMetadataUnavailable) {
				t.Fatalf("Metadata: err = %v, want ErrMetadataUnavailable", err)
			}
			// ⚠️ Never ErrFilesUnavailable: some source in the composition DOES
			// serve files, so that claim is false about this inner index.
			if errors.Is(err, ErrFilesUnavailable) {
				t.Fatalf("Metadata: err = %v, must not claim the inner index serves no files", err)
			}
		})
	}
}

// A file-level policy over a wholly fileless index admits NOTHING, and does so
// by returning an empty list rather than by failing.
//
// ⚠️ This pins a deliberate reversal. Earlier versions of this code hard-errored
// here, on the reasoning that silently admitting nothing would report every
// package as having no acceptable version -- a constraint conflict that does not
// exist. That reasoning was sound but the property was not achievable:
//
//   - It misfired on a legitimate partial mirror (see above), where the same
//     sentinel arrives for an entirely different reason.
//   - It was data-dependent. If the pre-release axis emptied the version list
//     first, or the package had no versions, the file axis was never consulted
//     and the same misconfiguration returned an empty list anyway. So the
//     "always fails" promise held only for favourable data.
//   - FilteredIndex cannot actually distinguish "the operator wired up a
//     fileless index" from "this package is absent from the file source". Both
//     are the absence of file evidence.
//
// So the rule is now uniform and predictable, and the hazard is documented
// instead of half-guarded. Verifying that a file-level policy is composed over a
// file-serving source is the caller's job, which the docs now say plainly.
func TestFilteredIndexFileLevelPolicyOverFilelessIndexAdmitsNothing(t *testing.T) {
	ctx := context.Background()

	for name, policy := range map[string]FilterPolicy{
		"yanked": {ExcludeYanked: true},
		"date":   {SnapshotDate: cutoff},
		// Finding 3's shape: the version axis empties the list first. This used
		// to return empty-and-nil while the rows above hard-errored, for the
		// same misconfiguration.
		"date and pre-release": {SnapshotDate: cutoff, ExcludePrereleases: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := NewFilteredIndex(openFixtureIndex(t), policy)

			versions, err := f.Versions(ctx, NewPackageName("flask"))
			if err != nil {
				t.Fatalf("Versions: err = %v, want nil error", err)
			}
			if len(versions) != 0 {
				t.Fatalf("Versions = %v, want empty", versionStrings(versions))
			}

			_, err = f.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
			if !errors.Is(err, ErrMetadataUnavailable) {
				t.Fatalf("Metadata: err = %v, want ErrMetadataUnavailable", err)
			}
		})
	}
}

// ⚠️ Finding 5: a cross-source spelling difference does not merely cost a
// Metadata lookup, it silently EMPTIES the version list under a file-level
// policy. Versions collapses the equality class to the earliest source's
// spelling, and the file-serving source is then asked for a spelling its own
// string-keyed lookup does not hold.
//
// Pinned rather than fixed, for the reason given on MultiIndex: bridging it
// costs a Versions call per source per lookup. What matters is that the
// documented limitation states this consequence, since "one version resolves
// oddly" and "the package appears to have no usable versions" are very
// different things to debug.
func TestFilteredOverMultiIndexCrossSourceSpellingEmptiesTheVersionList(t *testing.T) {
	ctx := context.Background()

	// The first source spells it 1.0; the file-serving source spells the same
	// version 1.0.0 and has a perfectly good file for it.
	deps := NewMockIndex("deps").AddVersion("flask", "1.0", "werkzeug>=3.0")
	mirror := NewMockIndex("mirror").
		AddFiles("flask", "1.0.0", distFile("flask-1.0.0.whl", cutoff.Add(-time.Hour), false))

	m := NewMultiIndex(deps, mirror)

	// Unfiltered, the version is there under the earliest source's spelling.
	versions, err := m.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if got, want := versionStrings(versions), []string{"1.0"}; !slices.Equal(got, want) {
		t.Fatalf("unfiltered Versions = %v, want %v", got, want)
	}

	// Under a file-level policy the whole list empties, because 1.0 finds no
	// file evidence under that spelling.
	f := NewFilteredIndex(m, FilterPolicy{SnapshotDate: cutoff})
	versions, err = f.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions under a file policy: %v, want nil error", err)
	}
	if len(versions) != 0 {
		t.Fatalf("Versions = %v; want empty -- if this now finds the version, "+
			"the limitation was fixed and MultiIndex's documentation must be updated",
			versionStrings(versions))
	}
}

// A per-source FilteredIndex inside a MultiIndex is a natural shape -- different
// policy per source -- and MultiIndex must tolerate the sentinels such a source
// produces rather than aborting the whole lookup (finding 4).
func TestMultiIndexToleratesAFilteredSource(t *testing.T) {
	ctx := context.Background()
	pkg := NewPackageName("flask")

	// A fileless source under a file-level policy: every lookup is refused.
	strict := NewFilteredIndex(openFixtureIndex(t), FilterPolicy{ExcludeYanked: true})
	// A healthy source that does have the version.
	good := NewMockIndex("good").
		AddVersion("flask", "3.0.0", "werkzeug>=3.0").
		AddFiles("flask", "3.0.0", distFile("ok.whl", cutoff, false))

	m := NewMultiIndex(strict, good)

	versions, err := m.Versions(ctx, pkg)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if got, want := versionStrings(versions), []string{"3.0.0"}; !slices.Equal(got, want) {
		t.Fatalf("Versions = %v, want %v", got, want)
	}

	meta, err := m.Metadata(ctx, pkg, mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.Origin != "good" {
		t.Fatalf("Origin = %q, want %q", meta.Origin, "good")
	}

	files, err := m.Files(ctx, pkg, mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got, want := filenames(files), []string{"ok.whl"}; !slices.Equal(got, want) {
		t.Fatalf("Files = %v, want %v", got, want)
	}
}

// Guard against the composed stack quietly losing a real failure.
func TestFilteredOverMultiIndexPropagatesRealErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("connection reset")

	f := NewFilteredIndex(
		NewMultiIndex(errIndexOn{err: boom}, NewMockIndex("other")),
		FilterPolicy{SnapshotDate: cutoff},
	)

	if _, err := f.Versions(ctx, NewPackageName("flask")); !errors.Is(err, boom) {
		t.Fatalf("Versions = %v, want the underlying failure to propagate", err)
	}
}

// errIndexOn is errIndex but answering Versions successfully, so a failure can be
// injected at the Files stage that FilteredIndex reaches only via its policy.
type errIndexOn struct{ err error }

func (e errIndexOn) Versions(context.Context, PackageName) ([]version.Version, error) {
	v, err := version.Parse("1.0")
	if err != nil {
		return nil, err
	}
	return []version.Version{v}, nil
}

func (e errIndexOn) Metadata(context.Context, PackageName, version.Version) (PackageMetadata, error) {
	return PackageMetadata{}, e.err
}

func (e errIndexOn) Files(context.Context, PackageName, version.Version) ([]DistFile, error) {
	return nil, e.err
}

var _ MetadataIndex = errIndexOn{}
