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

// errIndex is a MetadataIndex that returns one fixed error from every method.
//
// MockIndex deliberately cannot produce ErrMetadataUnusable or an arbitrary I/O
// failure -- it models a well-formed in-memory index -- and composition is
// exactly where those two need to be driven, because MultiIndex has to decide
// which of several sources' errors to report. Hence a stub rather than a new
// MockIndex knob: widening the mock to emit errors it can never really produce
// would let a test assert a state no real index reaches.
type errIndex struct{ err error }

func (e errIndex) Versions(context.Context, PackageName) ([]version.Version, error) {
	return nil, e.err
}

func (e errIndex) Metadata(context.Context, PackageName, version.Version) (PackageMetadata, error) {
	return PackageMetadata{}, e.err
}

func (e errIndex) Files(context.Context, PackageName, version.Version) ([]DistFile, error) {
	return nil, e.err
}

var _ MetadataIndex = errIndex{}

// --- Versions ---

// Version availability is a UNION across sources. That is what makes a
// MultiIndex useful for "a local source plus upstream": each contributes the
// versions it has.
func TestMultiIndexVersionsUnionsSources(t *testing.T) {
	ctx := context.Background()
	m := NewMultiIndex(
		NewMockIndex("first").AddVersion("flask", "3.0.0"),
		NewMockIndex("second").AddVersion("flask", "3.1.0").AddVersion("flask", "2.0.0"),
	)

	vs, err := m.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	want := []string{"2.0.0", "3.0.0", "3.1.0"}
	if got := versionStrings(vs); !slices.Equal(got, want) {
		t.Fatalf("Versions = %v, want %v", got, want)
	}
}

// ⚠️ NO TWO RETURNED VERSIONS MAY COMPARE EQUAL, and PEP 440 equality is coarser
// than string equality, so two sources spelling one version "1.0" and "1.0.0"
// hold ONE version under two spellings. Returning both is not merely redundant:
// a resolver cannot choose between candidates that compare equal, so the choice
// silently falls to iteration order, and the two records can disagree about
// dependencies.
//
// The representative must come from the EARLIEST source, because Metadata walks
// the sources in the same order and so resolves that spelling to that source's
// record. If the two used different rules they would agree only by coincidence.
func TestMultiIndexVersionsCollapsesEqualSpellingsToTheEarliestSource(t *testing.T) {
	ctx := context.Background()
	m := NewMultiIndex(
		NewMockIndex("first").AddVersion("flask", "1.0", "werkzeug>=3.0"),
		NewMockIndex("second").AddVersion("flask", "1.0.0", "jinja2>=3.1"),
	)

	vs, err := m.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("Versions = %v, want exactly one representative of the equality class", versionStrings(vs))
	}
	if got := vs[0].String(); got != "1.0" {
		t.Fatalf("representative = %q, want %q (the earliest source's spelling)", got, "1.0")
	}

	// And the representative must resolve to the record MultiIndex treated as
	// authoritative -- the first source's, not the second's.
	meta, err := m.Metadata(ctx, NewPackageName("flask"), vs[0])
	if err != nil {
		t.Fatalf("Metadata for the representative: %v", err)
	}
	if meta.Origin != "first" {
		t.Fatalf("Origin = %q, want %q", meta.Origin, "first")
	}
}

// ErrPackageNotFound only when NO source knows the name. One source knowing it
// is enough, and a source that does not is answering rather than failing.
func TestMultiIndexVersionsNotFoundOnlyWhenNoSourceKnowsThePackage(t *testing.T) {
	ctx := context.Background()

	m := NewMultiIndex(
		NewMockIndex("first"), // knows nothing
		NewMockIndex("second").AddVersion("flask", "3.0.0"),
	)
	vs, err := m.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions with one knowing source: %v", err)
	}
	if got, want := versionStrings(vs), []string{"3.0.0"}; !slices.Equal(got, want) {
		t.Fatalf("Versions = %v, want %v", got, want)
	}

	_, err = NewMultiIndex(NewMockIndex("a"), NewMockIndex("b")).
		Versions(ctx, NewPackageName("flask"))
	if !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("Versions with no knowing source: err = %v, want ErrPackageNotFound", err)
	}
}

// A package known to a source but carrying no versions is an empty slice and a
// nil error, even when another source has never heard of it. Collapsing this to
// ErrPackageNotFound would turn a constraint conflict into a reported typo.
func TestMultiIndexVersionsKnownButEmptyIsNotNotFound(t *testing.T) {
	ctx := context.Background()
	m := NewMultiIndex(
		NewMockIndex("first").AddPackage("lonely"),
		NewMockIndex("second"),
	)

	vs, err := m.Versions(ctx, NewPackageName("lonely"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 0 {
		t.Fatalf("Versions = %v, want empty", versionStrings(vs))
	}
}

// A genuine failure must NOT be masked by another source's success. The union
// would be silently incomplete, which is the shape of a confident wrong answer.
func TestMultiIndexVersionsPropagatesRealErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("disk on fire")
	m := NewMultiIndex(
		NewMockIndex("first").AddVersion("flask", "3.0.0"),
		errIndex{err: fmt.Errorf("second: %w", boom)},
	)

	if _, err := m.Versions(ctx, NewPackageName("flask")); !errors.Is(err, boom) {
		t.Fatalf("Versions = %v, want the underlying failure to propagate", err)
	}
}

// --- Metadata ---

func TestMultiIndexMetadataFirstSourceWins(t *testing.T) {
	ctx := context.Background()
	m := NewMultiIndex(
		NewMockIndex("first").AddVersion("flask", "3.0.0"),
		NewMockIndex("second").AddVersion("flask", "3.0.0"),
	)

	meta, err := m.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	// Origin is the only way to tell which source answered, which per RFD 0001
	// Section 16 is the reason the field is carried at all.
	if meta.Origin != "first" {
		t.Fatalf("Origin = %q, want %q", meta.Origin, "first")
	}
}

// A source that cannot supply this version is not the end of the search: the
// next source gets asked. "Ordered sources" means first source that can ANSWER,
// not first source consulted.
func TestMultiIndexMetadataFallsThroughToALaterSource(t *testing.T) {
	ctx := context.Background()
	m := NewMultiIndex(
		NewMockIndex("first").AddVersion("flask", "2.0.0"), // knows flask, not 3.0.0
		NewMockIndex("second").AddVersion("flask", "3.0.0"),
	)

	meta, err := m.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.Origin != "second" {
		t.Fatalf("Origin = %q, want %q", meta.Origin, "second")
	}
}

// ⚠️ THE COMPOSITION TRAP. Source A knows the package but not the version;
// source B knows neither. The answer is ErrMetadataUnavailable, NOT
// ErrPackageNotFound: the package WAS found, and B's not-found is B's answer
// about B, not a fact about the composed index. A caller branching on
// ErrPackageNotFound here would report a missing package for a present one --
// which is precisely the defect rstudio/package-manager#19466's F12 chased
// through three different implementations.
func TestMultiIndexMetadataErrorTaxonomyAcrossSources(t *testing.T) {
	ctx := context.Background()
	unknownVer := mustVersion(t, "9.9.9")

	tests := []struct {
		name    string
		sources []MetadataIndex
		want    error
		notWant []error
	}{
		{
			name: "one source knows the package, none has the version",
			sources: []MetadataIndex{
				NewMockIndex("first").AddVersion("flask", "3.0.0"),
				NewMockIndex("second"),
			},
			want:    ErrMetadataUnavailable,
			notWant: []error{ErrPackageNotFound},
		},
		{
			name: "the knowing source is LAST",
			sources: []MetadataIndex{
				NewMockIndex("first"),
				NewMockIndex("second").AddVersion("flask", "3.0.0"),
			},
			want:    ErrMetadataUnavailable,
			notWant: []error{ErrPackageNotFound},
		},
		{
			name: "no source knows the package",
			sources: []MetadataIndex{
				NewMockIndex("first"),
				NewMockIndex("second"),
			},
			want:    ErrPackageNotFound,
			notWant: []error{ErrMetadataUnavailable},
		},
		{
			name:    "no sources at all",
			sources: nil,
			want:    ErrPackageNotFound,
			notWant: []error{ErrMetadataUnavailable},
		},
		{
			// A record that EXISTS and is malformed outranks no record at all:
			// it is the more specific truth, and folding it into "unavailable"
			// would lose the only fact that makes the failure actionable.
			name: "a malformed record outranks a missing one",
			sources: []MetadataIndex{
				NewMockIndex("first").AddVersion("flask", "3.0.0"),
				errIndex{err: fmt.Errorf("second: %w", ErrMetadataUnusable)},
			},
			want:    ErrMetadataUnusable,
			notWant: []error{ErrMetadataUnavailable, ErrPackageNotFound},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMultiIndex(tc.sources...)
			_, err := m.Metadata(ctx, NewPackageName("flask"), unknownVer)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Metadata err = %v, want %v", err, tc.want)
			}
			for _, bad := range tc.notWant {
				if errors.Is(err, bad) {
					t.Errorf("Metadata err = %v, must not wrap %v", err, bad)
				}
			}
		})
	}
}

// A usable record in a later source is preferred over an earlier source's
// malformed one -- the later source CAN answer, and "first source that can
// answer" is the rule. The malformed record does not disappear from the record,
// though: it is only invisible when someone else succeeds, which is why the
// unusable-outranks-unavailable case above exists.
func TestMultiIndexMetadataPrefersAUsableRecordOverAMalformedOne(t *testing.T) {
	ctx := context.Background()
	m := NewMultiIndex(
		errIndex{err: fmt.Errorf("first: %w", ErrMetadataUnusable)},
		NewMockIndex("second").AddVersion("flask", "3.0.0"),
	)

	meta, err := m.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.Origin != "second" {
		t.Fatalf("Origin = %q, want %q", meta.Origin, "second")
	}
}

func TestMultiIndexMetadataPropagatesRealErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("connection reset")
	m := NewMultiIndex(
		errIndex{err: fmt.Errorf("first: %w", boom)},
		NewMockIndex("second").AddVersion("flask", "3.0.0"),
	)

	if _, err := m.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0")); !errors.Is(err, boom) {
		t.Fatalf("Metadata = %v, want the underlying failure to propagate rather than be masked", err)
	}
}

// --- Files ---

// ⚠️ An EMPTY file list with a nil error is a real answer, not a miss: the
// contract says so, because a release can have every file deleted. Treating it
// as "keep looking" would let a later mirror's stale files resurrect a release
// the authoritative source has emptied.
func TestMultiIndexFilesEmptyIsAnAnswerNotAMiss(t *testing.T) {
	ctx := context.Background()
	m := NewMultiIndex(
		NewMockIndex("first").AddVersion("flask", "3.0.0"), // known version, zero files
		NewMockIndex("second").AddFiles("flask", "3.0.0", distFile("stale.whl", cutoff, false)),
	)

	files, err := m.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("Files = %v, want empty: the first source answered", filenames(files))
	}
}

// ErrFilesUnavailable means "this source cannot answer, ask another" -- the one
// sentinel that is about the source's capability rather than the data. RSFIndex
// returns it unconditionally, so a MultiIndex pairing an RSF with a
// file-serving source is the intended composition.
func TestMultiIndexFilesSkipsSourcesThatServeNoFiles(t *testing.T) {
	ctx := context.Background()
	m := NewMultiIndex(
		openFixtureIndex(t), // an RSF: dependency metadata only, never files
		NewMockIndex("files").AddFiles("flask", "3.0.0", distFile("flask.whl", cutoff, false)),
	)

	files, err := m.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got, want := filenames(files), []string{"flask.whl"}; !slices.Equal(got, want) {
		t.Fatalf("Files = %v, want %v", got, want)
	}
}

func TestMultiIndexFilesErrorTaxonomyAcrossSources(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		sources []MetadataIndex
		pkg     string
		ver     string
		want    error
		notWant []error
	}{
		{
			// Nobody serves files at all. The honest answer is about capability.
			name:    "every source serves no files",
			sources: []MetadataIndex{openFixtureIndex(t), openFixtureIndex(t)},
			pkg:     "flask", ver: "3.0.0",
			want:    ErrFilesUnavailable,
			notWant: []error{ErrPackageNotFound},
		},
		{
			// ⚠️ THE MIXED CASE. A file-serving source denied the name while a
			// fileless source was also present.
			//
			// The answer is ErrMetadataUnavailable, and NOT ErrFilesUnavailable:
			// a source that serves files WAS asked and did answer, so "no source
			// serves files" is false. Nor ErrPackageNotFound: the fileless source
			// cannot speak to existence through Files, so absence is not a claim
			// this index may make.
			//
			// This row previously asserted ErrFilesUnavailable and justified it
			// with "the package IS known, to the RSF" -- which was a FALSE
			// RATIONALIZATION. RSFIndex.Files returns ErrFilesUnavailable without
			// inspecting pkg or ver at all, so it says nothing about whether
			// flask is known, and the row passed identically for a package no
			// source had ever heard of. See the companion row below, which is the
			// case that rationale actually described.
			name: "a fileless source is present and the file-serving source denies the name",
			sources: []MetadataIndex{
				openFixtureIndex(t),
				NewMockIndex("empty"),
			},
			pkg: "flask", ver: "3.0.0",
			want:    ErrMetadataUnavailable,
			notWant: []error{ErrPackageNotFound, ErrFilesUnavailable},
		},
		{
			// ⚠️ Finding 6: the ONLY row that sets both sawUnavailable and
			// sawFilesUnavailable, so it is the only one that exercises the
			// precedence between them. Without it, swapping the two case arms in
			// the resolution switch leaves the whole table green.
			//
			// A source that knows the package and cannot serve this version's
			// files is strictly more informative than a source that serves no
			// files at all, so it wins.
			name: "a knowing file-serving source and a fileless source together",
			sources: []MetadataIndex{
				openFixtureIndex(t),
				NewMockIndex("files").AddFiles("flask", "3.0.0", distFile("a.whl", cutoff, false)),
			},
			pkg: "flask", ver: "9.9.9",
			want:    ErrMetadataUnavailable,
			notWant: []error{ErrFilesUnavailable, ErrPackageNotFound},
		},
		{
			// ErrPackageNotFound is REACHABLE, which it was not while a fileless
			// source's answer outranked everything: every source here can speak
			// to files and every one denies the name, so absence is a claim this
			// index can actually make. Without this row a caller could not tell a
			// typo from "nobody serves files".
			name: "every file-serving source denies the name",
			sources: []MetadataIndex{
				NewMockIndex("files-a").AddFiles("flask", "3.0.0", distFile("a.whl", cutoff, false)),
				NewMockIndex("files-b").AddFiles("django", "5.0", distFile("b.whl", cutoff, false)),
			},
			pkg: "ghost", ver: "1.0",
			want:    ErrPackageNotFound,
			notWant: []error{ErrMetadataUnavailable, ErrFilesUnavailable},
		},
		{
			// A file-serving source knows the package but not this version.
			// That is more informative than "no source serves files".
			name: "a file-serving source knows the package but not the version",
			sources: []MetadataIndex{
				NewMockIndex("files").AddFiles("flask", "3.0.0", distFile("a.whl", cutoff, false)),
				NewMockIndex("empty"),
			},
			pkg: "flask", ver: "9.9.9",
			want:    ErrMetadataUnavailable,
			notWant: []error{ErrPackageNotFound, ErrFilesUnavailable},
		},
		{
			name: "no source knows the package",
			sources: []MetadataIndex{
				NewMockIndex("a"),
				NewMockIndex("b"),
			},
			pkg: "ghost", ver: "1.0",
			want:    ErrPackageNotFound,
			notWant: []error{ErrMetadataUnavailable, ErrFilesUnavailable},
		},
		{
			name:    "no sources at all",
			sources: nil,
			pkg:     "ghost", ver: "1.0",
			want:    ErrPackageNotFound,
			notWant: []error{ErrFilesUnavailable},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMultiIndex(tc.sources...)
			_, err := m.Files(ctx, NewPackageName(tc.pkg), mustVersion(t, tc.ver))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Files err = %v, want %v", err, tc.want)
			}
			for _, bad := range tc.notWant {
				if errors.Is(err, bad) {
					t.Errorf("Files err = %v, must not wrap %v", err, bad)
				}
			}
		})
	}
}

// --- cross-cutting ---

// ⚠️ The documented limitation, pinned so it cannot change silently. When two
// sources spell one version differently and the earliest source can list it but
// not supply its metadata, the composed lookup FAILS even though the later
// source holds a usable record for the same equality class -- because that
// source is asked for the earliest source's spelling and its own lookup is by
// string.
//
// Bridging this would cost a Versions call per Metadata miss on every source, to
// rescue a case a real corpus produces rarely. It is documented instead. If this
// test starts passing differently, the behavior changed and the doc must too.
func TestMultiIndexCrossSourceSpellingIsNotBridged(t *testing.T) {
	ctx := context.Background()
	m := NewMultiIndex(
		// Lists 1.0 but has no metadata for it.
		NewMockIndex("first").SetUnavailable("flask", "1.0"),
		// Holds the same version, spelled 1.0.0, WITH metadata.
		NewMockIndex("second").AddVersion("flask", "1.0.0", "werkzeug>=3.0"),
	)

	vs, err := m.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 1 || vs[0].String() != "1.0" {
		t.Fatalf("Versions = %v, want exactly [1.0]", versionStrings(vs))
	}

	_, err = m.Metadata(ctx, NewPackageName("flask"), vs[0])
	if !errors.Is(err, ErrMetadataUnavailable) {
		t.Fatalf("Metadata err = %v, want ErrMetadataUnavailable (the documented limitation)", err)
	}
}

// The composition RFD 0001 actually calls for: dependency metadata from an RSF,
// files from a file-serving source, with policy applied over the pair. This is
// the arrangement that makes a file-level policy expressible at all over an RSF,
// so it is worth one test that exercises both new types together.
func TestFilteredOverMultiIndexAppliesFilePolicyToAnRSFBackedIndex(t *testing.T) {
	ctx := context.Background()
	files := NewMockIndex("files").
		AddFiles("flask", "3.0.0", distFile("in.whl", cutoff.Add(-time.Hour), false)).
		AddFiles("flask", "3.0.1", distFile("out.whl", cutoff.Add(time.Hour), false))

	f := NewFilteredIndex(
		NewMultiIndex(openFixtureIndex(t), files),
		FilterPolicy{SnapshotDate: cutoff},
	)

	vs, err := f.Versions(ctx, NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	// The RSF fixture also carries 3.0.2, for which the file source has no
	// record at all, so the cutoff cannot admit it either.
	if got, want := versionStrings(vs), []string{"3.0.0"}; !slices.Equal(got, want) {
		t.Fatalf("Versions = %v, want %v", got, want)
	}

	// Dependency metadata still comes from the RSF, which is the point of the
	// arrangement.
	meta, err := f.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(meta.RequiresDist) == 0 {
		t.Fatal("Metadata carried no requirements; it should have come from the RSF")
	}
}

func TestMultiIndexHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m := NewMultiIndex(NewMockIndex("first").AddVersion("flask", "3.0.0"))

	if _, err := m.Versions(ctx, NewPackageName("flask")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Versions: err = %v, want context.Canceled", err)
	}
	if _, err := m.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Metadata: err = %v, want context.Canceled", err)
	}
	if _, err := m.Files(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Files: err = %v, want context.Canceled", err)
	}
}

func TestNewMultiIndexRejectsANilSource(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewMultiIndex with a nil source did not panic")
		}
	}()
	NewMultiIndex(NewMockIndex("ok"), nil)
}
