// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"fmt"
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

// filelessIndex serves versions and metadata but no distribution files. That is
// RSFIndex's shape -- an RSF carries no filename, hash, upload time or yanked
// flag -- and it is what PPM's index will be.
//
// A MockIndex is wrapped rather than reimplemented so the version content is easy
// to vary. RSFIndex's own fixture cannot grow a pre-release-only package without
// editing a test file this change does not own, and the totality proof below
// needs exactly that shape.
type filelessIndex struct{ *MockIndex }

func (filelessIndex) Files(ctx context.Context, pkg PackageName, ver version.Version) ([]DistFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("fileless index: %q %s: carries dependency metadata only: %w",
		pkg, ver, ErrFilesUnavailable)
}

var _ MetadataIndex = filelessIndex{}

// ⚠️ A file-level policy over a wholly fileless index REFUSES. It must not
// quietly report every package as having no acceptable version, which downstream
// reads as a constraint conflict that does not exist.
//
// This reverses the behavior released as v0.3.0, and the reversal is the point.
// v0.3.0 dropped silently because ErrFilesUnavailable had been unreliable: a
// MultiIndex emitted it for a mere data condition, so a legitimate partial mirror
// hard-errored. But the fix for that landed in the SAME change -- MultiIndex now
// reserves the sentinel for a genuine capability statement, emitting it only when
// every source is fileless. The two fixes over-corrected past each other: one
// made the signal trustworthy while the other stopped trusting it.
//
// With the signal reliable, refusing is available again, and it is the right
// uniform rule. The partial-mirror case stays silent because it now arrives as
// ErrMetadataUnavailable, not because this method guesses.
func TestFilteredIndexFileLevelPolicyOverFilelessIndexRefuses(t *testing.T) {
	ctx := context.Background()
	pkg := NewPackageName("flask")

	for name, policy := range map[string]FilterPolicy{
		"yanked":               {ExcludeYanked: true},
		"date":                 {SnapshotDate: cutoff},
		"date and pre-release": {SnapshotDate: cutoff, ExcludePrereleases: true},
		"both file axes":       {SnapshotDate: cutoff, ExcludeYanked: true},
	} {
		t.Run(name, func(t *testing.T) {
			// RSFIndex itself, so this is pinned against the real fileless
			// implementation and not only against the stub.
			f := NewFilteredIndex(openFixtureIndex(t), policy)

			_, err := f.Versions(ctx, pkg)
			if !errors.Is(err, ErrFilesUnavailable) {
				t.Fatalf("Versions: err = %v, want ErrFilesUnavailable", err)
			}

			_, err = f.Metadata(ctx, pkg, mustVersion(t, "3.0.0"))
			if !errors.Is(err, ErrFilesUnavailable) {
				t.Fatalf("Metadata: err = %v, want ErrFilesUnavailable", err)
			}

			// Files was already loud, by passing the inner error through.
			_, err = f.Files(ctx, pkg, mustVersion(t, "3.0.0"))
			if !errors.Is(err, ErrFilesUnavailable) {
				t.Fatalf("Files: err = %v, want ErrFilesUnavailable", err)
			}
		})
	}
}

// ⚠️ THE TOTALITY PROOF, and the reason this is not the guard the reviewer
// rightly rejected as "firing only on favourable data".
//
// The refusal cannot fire when the per-version loop never runs -- a package with
// no versions, or one whose versions the version-level axis already excluded. The
// earlier review read that as the property being incomplete. It is not, and the
// distinction is what makes the design defensible:
//
//	IN THOSE CASES THE FILE AXES COULD NOT HAVE CHANGED THE ANSWER.
//
// An empty result is then the correct answer over ANY index, files or not, so
// there is nothing for a capability check to report. This test proves that by
// asserting the fileless index and a file-serving index with the SAME version
// content give the SAME answer. If a future change makes them diverge, the
// silence has become a real gap and this test says so.
//
// The invariant, stated once: a capability failure is reported wherever a
// file-level axis could have changed the answer, and nowhere else.
func TestFilteredIndexCapabilityRefusalIsTotal(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		policy FilterPolicy
		// build populates an index with identical version content in both the
		// fileless and file-serving cases. addFiles is called only for the
		// file-serving one.
		build    func(m *MockIndex)
		addFiles func(m *MockIndex)
		pkg      string
	}{
		{
			// No versions at all: the file axes have nothing to evaluate.
			name:     "known package with no versions",
			policy:   FilterPolicy{SnapshotDate: cutoff, ExcludeYanked: true},
			build:    func(m *MockIndex) { m.AddPackage("lonely") },
			addFiles: func(*MockIndex) {},
			pkg:      "lonely",
		},
		{
			// The version axis empties the list first, so the file axes are
			// never reached. This is the exact case the review raised.
			name:   "every version excluded by the version-level axis",
			policy: FilterPolicy{ExcludePrereleases: true, SnapshotDate: cutoff},
			build:  func(m *MockIndex) { m.AddVersion("flask", "3.0.0b1") },
			addFiles: func(m *MockIndex) {
				m.AddFiles("flask", "3.0.0b1", distFile("beta.whl", cutoff.Add(-time.Hour), false))
			},
			pkg: "flask",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fileless := NewMockIndex("fileless")
			tc.build(fileless)

			serving := NewMockIndex("serving")
			tc.build(serving)
			tc.addFiles(serving)

			refusing := NewFilteredIndex(filelessIndex{fileless}, tc.policy)
			answering := NewFilteredIndex(serving, tc.policy)

			refusedVersions, refusedErr := refusing.Versions(ctx, NewPackageName(tc.pkg))
			servedVersions, servedErr := answering.Versions(ctx, NewPackageName(tc.pkg))

			if refusedErr != nil || servedErr != nil {
				t.Fatalf("Versions errored where the file axes are not load-bearing:\n"+
					"  fileless: %v\n  file-serving: %v\n"+
					"neither should fail, because the answer does not depend on files here",
					refusedErr, servedErr)
			}

			// The proof: identical answers, so the silence is correct rather
			// than a missed refusal.
			if !slices.Equal(versionStrings(refusedVersions), versionStrings(servedVersions)) {
				t.Fatalf("fileless gave %v but file-serving gave %v; "+
					"they must agree wherever the file axes cannot change the answer",
					versionStrings(refusedVersions), versionStrings(servedVersions))
			}
			if len(refusedVersions) != 0 {
				t.Fatalf("Versions = %v, want empty", versionStrings(refusedVersions))
			}
		})
	}
}

// The coordinator's specific worry: whether ErrFilesUnavailable from a NESTED
// FilteredIndex reintroduces the ambiguity that made the sentinel untrustworthy.
//
// It does not, and the reason is compositional. A FilteredIndex over a fileless
// inner can never serve a file, so its own ErrFilesUnavailable is a true
// capability statement about itself. A MultiIndex above it then demotes that to
// weakest evidence, so it only escapes when every sibling is fileless too. The
// invariant holds at each layer rather than by luck.
func TestNestedFilteredIndexPreservesTheCapabilitySignal(t *testing.T) {
	ctx := context.Background()
	pkg := NewPackageName("flask")

	// A nested FilteredIndex whose own inner serves no files.
	nested := NewFilteredIndex(openFixtureIndex(t), FilterPolicy{ExcludePrereleases: true})

	t.Run("a file-serving sibling keeps the composition answerable", func(t *testing.T) {
		mirror := NewMockIndex("mirror").
			AddFiles("flask", "3.0.0", distFile("ok.whl", cutoff.Add(-time.Hour), false))
		f := NewFilteredIndex(NewMultiIndex(nested, mirror), FilterPolicy{SnapshotDate: cutoff})

		versions, err := f.Versions(ctx, pkg)
		if err != nil {
			t.Fatalf("Versions: %v, want nil -- a file-serving sibling is present", err)
		}
		if got, want := versionStrings(versions), []string{"3.0.0"}; !slices.Equal(got, want) {
			t.Fatalf("Versions = %v, want %v", got, want)
		}
	})

	t.Run("all-fileless siblings still refuse", func(t *testing.T) {
		f := NewFilteredIndex(
			NewMultiIndex(nested, openFixtureIndex(t)),
			FilterPolicy{SnapshotDate: cutoff},
		)

		if _, err := f.Versions(ctx, pkg); !errors.Is(err, ErrFilesUnavailable) {
			t.Fatalf("Versions: err = %v, want ErrFilesUnavailable", err)
		}
	})
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
