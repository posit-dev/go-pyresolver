// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	rsf "github.com/rstudio/repository-snapshot-format"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// --- fixture construction ---
//
// These build a real RSF with the real writer, mirroring pypirsf's own tests.
// Duplicated rather than exported from pypirsf because a test helper is not
// something a public library should ship, and the encoding is a handful of
// lines.

func putUvarint(buf *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	buf.Write(tmp[:n])
}

func putStr(buf *bytes.Buffer, s string) {
	putUvarint(buf, uint64(len(s)))
	buf.WriteString(s)
}

type fixtureVersion struct {
	version        string
	requiresDist   []string
	requiresPython string
	providesExtra  []string
}

// buildStoredDepsField encodes a stored (uncompressed) deps blob, so a fixture
// needs no trained zstd dictionary.
func buildStoredDepsField(versions []fixtureVersion) string {
	var body bytes.Buffer

	putUvarint(&body, uint64(len(versions)))
	for _, fv := range versions {
		putStr(&body, fv.requiresPython)

		putUvarint(&body, uint64(len(fv.requiresDist)))
		for _, req := range fv.requiresDist {
			putUvarint(&body, 0) // inline name, no dictionary reference
			putStr(&body, req)
			putStr(&body, "")
		}

		putUvarint(&body, uint64(len(fv.providesExtra)))
		for _, e := range fv.providesExtra {
			putStr(&body, e)
		}
	}

	putUvarint(&body, uint64(len(versions)))
	for i, fv := range versions {
		putStr(&body, fv.version)
		putUvarint(&body, uint64(i))
	}

	return string(append([]byte{0x02}, body.Bytes()...)) // 0x02 = stored
}

func buildDepsdictField() string {
	var buf bytes.Buffer
	buf.WriteByte(0x01)
	putUvarint(&buf, 0) // no names
	putUvarint(&buf, 0) // no zstd dictionary
	return buf.String()
}

// openFixtureIndex writes a small RSF and returns an RSFIndex over it.
func openFixtureIndex(t *testing.T) *RSFIndex {
	t.Helper()

	flask := pypirsf.PackageRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "3.0.0", ReleaseDate: "\x00\x01", Summary: "web"},
			{Snapshot: "2026080200", Version: "3.0.1", ReleaseDate: "\x00\x02", Summary: "web"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{
				version:        "3.0.0",
				requiresDist:   []string{"werkzeug>=3.0", `asgiref>=3.2 ; extra == "async"`},
				requiresPython: ">=3.8",
				providesExtra:  []string{"Async", "dot_env"},
			},
			{version: "3.0.1", requiresDist: []string{"werkzeug>=3.0.1"}, requiresPython: ">=3.8"},
			// A version whose interpreter constraint cannot be parsed.
			{version: "3.0.2", requiresPython: "not a specifier"},
			// A version key PEP 440 rejects.
			{version: "not-a-version", requiresDist: []string{"werkzeug"}},
		}),
		Depsdict: buildDepsdictField(),
	}

	broken := pypirsf.PackageRecord{
		CanonicalName: "broken",
		ProjectName:   "Broken",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresDist: []string{"!!! not a requirement"}},
		}),
	}

	// Equivalent-but-differently-spelled version key.
	padded := pypirsf.PackageRecord{
		CanonicalName: "padded",
		ProjectName:   "Padded",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{{version: "1.0", requiresPython: ">=3.9"}}),
	}

	nodeps := pypirsf.PackageRecord{
		CanonicalName: "nodeps",
		ProjectName:   "NoDeps",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
	}

	path := filepath.Join(t.TempDir(), "fixture.rsf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	w := rsf.NewWriter(f)
	for _, rec := range []pypirsf.PackageRecord{flask, broken, padded, nodeps} {
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

// --- tests ---

func TestNewRSFIndexRejectsNilFile(t *testing.T) {
	if _, err := NewRSFIndex(nil, ""); err == nil {
		t.Error("expected an error for a nil file")
	}
}

func TestRSFIndexVersionsSkipsUnparseableKeys(t *testing.T) {
	idx := openFixtureIndex(t)

	versions, err := idx.Versions(context.Background(), NewPackageName("flask"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}

	got := make([]string, 0, len(versions))
	for _, v := range versions {
		got = append(got, v.String())
	}
	sort.Strings(got)

	// "not-a-version" is dropped; the three real versions survive. One bad key
	// must not make the rest of the package unreachable.
	want := []string{"3.0.0", "3.0.1", "3.0.2"}
	if len(got) != len(want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("versions[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRSFIndexUnknownPackage(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	if _, err := idx.Versions(ctx, NewPackageName("absent")); !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("Versions: got %v, want ErrPackageNotFound", err)
	}
	if _, err := idx.Metadata(ctx, NewPackageName("absent"), mustVersion(t, "1.0")); !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("Metadata: got %v, want ErrPackageNotFound", err)
	}
}

func TestRSFIndexMetadataParsesRequirements(t *testing.T) {
	idx := openFixtureIndex(t)

	meta, err := idx.Metadata(context.Background(), NewPackageName("Flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}

	if meta.Name != "flask" {
		t.Errorf("Name = %q, want the normalized %q", meta.Name, "flask")
	}
	if meta.Origin != "test-rsf" {
		t.Errorf("Origin = %q, want %q", meta.Origin, "test-rsf")
	}
	if len(meta.RequiresDist) != 2 {
		t.Fatalf("got %d requirements, want 2", len(meta.RequiresDist))
	}
	if meta.RequiresDist[0].Name != "werkzeug" {
		t.Errorf("first requirement name = %q, want werkzeug", meta.RequiresDist[0].Name)
	}
	if meta.RequiresDist[0].Specifiers.String() != ">=3.0" {
		t.Errorf("first requirement specifiers = %q", meta.RequiresDist[0].Specifiers)
	}
	// The conditional dependency must keep its marker, or a resolver would
	// apply it unconditionally and over-install.
	if meta.RequiresDist[1].Marker.IsEmpty() {
		t.Error("the extra-conditional requirement lost its environment marker")
	}
	if meta.RequiresPython.String() != ">=3.8" {
		t.Errorf("RequiresPython = %q, want >=3.8", meta.RequiresPython)
	}
}

// TestRSFIndexNormalizesProvidedExtras covers the PEP 685 normalization that
// makes pkg[Test-Suite] match a declared "test_suite".
func TestRSFIndexNormalizesProvidedExtras(t *testing.T) {
	idx := openFixtureIndex(t)

	meta, err := idx.Metadata(context.Background(), NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}

	want := []string{"async", "dot-env"} // "Async" and "dot_env" normalized
	if len(meta.ProvidesExtra) != len(want) {
		t.Fatalf("ProvidesExtra = %v, want %v", meta.ProvidesExtra, want)
	}
	for i := range want {
		if meta.ProvidesExtra[i] != want[i] {
			t.Errorf("ProvidesExtra[%d] = %q, want %q", i, meta.ProvidesExtra[i], want[i])
		}
	}
}

// TestRSFIndexUnparseableRequirementIsFatal pins the deliberate asymmetry: a
// requirement that cannot be parsed fails the lookup rather than being dropped.
// Dropping it would hand the resolver an incomplete dependency set and produce a
// confidently wrong answer.
func TestRSFIndexUnparseableRequirementIsFatal(t *testing.T) {
	idx := openFixtureIndex(t)

	_, err := idx.Metadata(context.Background(), NewPackageName("broken"), mustVersion(t, "1.0"))
	if err == nil {
		t.Fatal("expected an error for an unparseable requirement")
	}
	if errors.Is(err, ErrMetadataUnavailable) || errors.Is(err, ErrPackageNotFound) {
		t.Errorf("a parse failure must not masquerade as a sentinel condition: %v", err)
	}
}

// TestRSFIndexUnparseableRequiresPythonIsLenient is the other side of that
// asymmetry: an unreadable interpreter constraint over-admits a candidate, which
// surfaces at install time, whereas an unreadable requirement would change the
// resolution silently.
func TestRSFIndexUnparseableRequiresPythonIsLenient(t *testing.T) {
	idx := openFixtureIndex(t)

	meta, err := idx.Metadata(context.Background(), NewPackageName("flask"), mustVersion(t, "3.0.2"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.RequiresPython.String() != "" {
		t.Errorf("RequiresPython = %q, want unset", meta.RequiresPython)
	}

	// ⚠️ The assertion above is NOT sufficient, and on its own it defended the
	// defect. It asks what the value RENDERS AS, never what it ADMITS — and an
	// empty version.Specifiers admits NOTHING, because Check iterates its groups
	// and returns false when there are none. So "lenient" was implemented as its
	// exact opposite, with a passing test beside it. Assert the policy.
	for _, interpreter := range []string{"2.7", "3.8", "3.11", "3.13", "4.0"} {
		if !meta.SupportsPython(mustVersion(t, interpreter)) {
			t.Errorf("SupportsPython(%s) = false; an unreadable Requires-Python must admit "+
				"EVERY interpreter, which is the whole point of it being non-fatal", interpreter)
		}
	}
}

// TestAbsentRequiresPythonAdmitsEveryInterpreter covers the case that is far more
// common than the unparseable one and reaches the same trap by a different route.
//
// A version that declares no Requires-Python at all never enters the parsing
// branch, so the field keeps its zero value — the same empty specifier set that
// admits nothing when Check is called on it directly. In a production PyPI
// snapshot this is over two million versions, so getting it wrong rejects a
// quarter of the corpus.
func TestAbsentRequiresPythonAdmitsEveryInterpreter(t *testing.T) {
	idx := openFixtureIndex(t)

	var zero PackageMetadata
	for _, interpreter := range []string{"2.7", "3.11", "4.0"} {
		if !zero.SupportsPython(mustVersion(t, interpreter)) {
			t.Errorf("zero-value PackageMetadata.SupportsPython(%s) = false, want true: "+
				"no declared constraint means no constraint", interpreter)
		}
	}

	// And the same through a real lookup, so the guarantee is not only a property
	// of the zero value in isolation.
	meta, err := idx.Metadata(context.Background(), NewPackageName("flask"), mustVersion(t, "3.0.2"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if !meta.SupportsPython(mustVersion(t, "3.11")) {
		t.Error("SupportsPython(3.11) = false for a version with no usable Requires-Python")
	}
}

// TestSupportsPythonStillEnforcesARealConstraint is the other half: leniency must
// not have been bought by making SupportsPython always true. A declared,
// parseable constraint has to actually exclude an interpreter outside it.
func TestSupportsPythonStillEnforcesARealConstraint(t *testing.T) {
	idx := openFixtureIndex(t)

	meta, err := idx.Metadata(context.Background(), NewPackageName("padded"), mustVersion(t, "1.0.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if got := meta.RequiresPython.String(); got != ">=3.9" {
		t.Fatalf("precondition: RequiresPython = %q, want >=3.9", got)
	}

	if !meta.SupportsPython(mustVersion(t, "3.11")) {
		t.Error("SupportsPython(3.11) = false for >=3.9")
	}
	if meta.SupportsPython(mustVersion(t, "3.8")) {
		t.Error("SupportsPython(3.8) = true for >=3.9 — the constraint is not being enforced")
	}
}

// TestRSFIndexMatchesEquivalentVersionSpelling covers a document key that is PEP
// 440-equal to the request but spelled differently.
func TestRSFIndexMatchesEquivalentVersionSpelling(t *testing.T) {
	idx := openFixtureIndex(t)

	meta, err := idx.Metadata(context.Background(), NewPackageName("padded"), mustVersion(t, "1.0.0"))
	if err != nil {
		t.Fatalf("Metadata for the equivalent spelling 1.0.0: %v", err)
	}
	if meta.RequiresPython.String() != ">=3.9" {
		t.Errorf("RequiresPython = %q, want >=3.9", meta.RequiresPython)
	}
}

// TestRSFIndexUncapturedVersionIsUnavailableNotNotFound matters because the two
// sentinels lead a resolver to different behavior: not-found invites giving up on
// the package, unavailable means try another version.
func TestRSFIndexUncapturedVersionIsUnavailableNotNotFound(t *testing.T) {
	idx := openFixtureIndex(t)

	_, err := idx.Metadata(context.Background(), NewPackageName("flask"), mustVersion(t, "99.0"))
	if !errors.Is(err, ErrMetadataUnavailable) {
		t.Errorf("got %v, want ErrMetadataUnavailable", err)
	}
	if errors.Is(err, ErrPackageNotFound) {
		t.Error("a known package with an uncaptured version must not report ErrPackageNotFound")
	}
}

func TestRSFIndexPackageWithNoCapturedDeps(t *testing.T) {
	idx := openFixtureIndex(t)

	versions, err := idx.Versions(context.Background(), NewPackageName("nodeps"))
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("got %d versions, want 0", len(versions))
	}
}

// TestRSFIndexFilesAlwaysUnavailable pins that Files reports a distinct sentinel
// rather than an empty slice. An empty slice would say "this version ships no
// files", which is a different and wrong claim.
func TestRSFIndexFilesAlwaysUnavailable(t *testing.T) {
	idx := openFixtureIndex(t)

	files, err := idx.Files(context.Background(), NewPackageName("flask"), mustVersion(t, "3.0.0"))
	if !errors.Is(err, ErrFilesUnavailable) {
		t.Errorf("got %v, want ErrFilesUnavailable", err)
	}
	if files != nil {
		t.Errorf("files = %v, want nil", files)
	}
	// The two unavailability sentinels must stay distinct: one says "ask another
	// source", the other says "choose another version".
	if errors.Is(err, ErrMetadataUnavailable) {
		t.Error("ErrFilesUnavailable must not also satisfy ErrMetadataUnavailable")
	}
}

func TestRSFIndexHonorsContextCancellation(t *testing.T) {
	idx := openFixtureIndex(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := idx.Versions(ctx, NewPackageName("flask")); !errors.Is(err, context.Canceled) {
		t.Errorf("Versions: got %v, want context.Canceled", err)
	}
	if _, err := idx.Metadata(ctx, NewPackageName("flask"), mustVersion(t, "3.0.0")); !errors.Is(err, context.Canceled) {
		t.Errorf("Metadata: got %v, want context.Canceled", err)
	}
}

// TestRSFIndexConcurrentUse exists to be run under -race: the decode cache is
// shared mutable state behind the interface's concurrency promise.
func TestRSFIndexConcurrentUse(t *testing.T) {
	idx := openFixtureIndex(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			pkg := NewPackageName("flask")
			if _, err := idx.Versions(ctx, pkg); err != nil {
				t.Errorf("Versions: %v", err)
				return
			}
			meta, err := idx.Metadata(ctx, pkg, mustVersion(t, "3.0.0"))
			if err != nil {
				t.Errorf("Metadata: %v", err)
				return
			}
			// A torn read would show up as a short requirement list.
			if len(meta.RequiresDist) != 2 {
				t.Errorf("got %d requirements, want 2", len(meta.RequiresDist))
			}
		}(i)
	}
	wg.Wait()
}

func TestRSFIndexExposesCorpusSize(t *testing.T) {
	idx := openFixtureIndex(t)

	if got := idx.Len(); got != 4 {
		t.Errorf("Len() = %d, want 4", got)
	}
	if got := idx.Packages(); len(got) != 4 {
		t.Errorf("Packages() returned %d names, want 4", len(got))
	}
}

func TestRSFIndexDefaultOrigin(t *testing.T) {
	flask := pypirsf.PackageRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots:     []pypirsf.SnapshotRecord{{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01"}},
		Deps:          buildStoredDepsField([]fixtureVersion{{version: "1.0"}}),
		Depsdict:      buildDepsdictField(),
	}

	path := filepath.Join(t.TempDir(), "one.rsf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	if _, err := rsf.NewWriter(f).WriteObject(flask); err != nil {
		t.Fatalf("writing record: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing fixture: %v", err)
	}

	file, err := pypirsf.Open(path)
	if err != nil {
		t.Fatalf("pypirsf.Open: %v", err)
	}
	defer func() { _ = file.Close() }()

	idx, err := NewRSFIndex(file, "")
	if err != nil {
		t.Fatalf("NewRSFIndex: %v", err)
	}

	meta, err := idx.Metadata(context.Background(), NewPackageName("flask"), mustVersion(t, "1.0"))
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.Origin != "rsf" {
		t.Errorf("Origin = %q, want the default %q", meta.Origin, "rsf")
	}
}
