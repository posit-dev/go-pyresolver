// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	rsf "github.com/rstudio/repository-snapshot-format"
)

// buildDepsField encodes a stored (uncompressed) deps blob for the given
// versions, using the same wire layout the producer writes.
//
// Stored rather than zstd so a fixture needs no trained dictionary: the format
// byte selects the path, and both paths reach the same decoder.
func buildDepsField(t *testing.T, byVersion map[string]VersionDeps, names []string) string {
	t.Helper()

	// Build a pool with one entry per version, in a stable order.
	versions := make([]string, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	// Sorted so the fixture is deterministic run to run.
	for i := 0; i < len(versions); i++ {
		for j := i + 1; j < len(versions); j++ {
			if versions[j] < versions[i] {
				versions[i], versions[j] = versions[j], versions[i]
			}
		}
	}

	nameIndex := make(map[string]int, len(names))
	for i, n := range names {
		nameIndex[n] = i
	}

	var body bytes.Buffer
	putUvarint(&body, uint64(len(versions)))
	for _, v := range versions {
		vd := byVersion[v]

		putStr(&body, vd.RequiresPython)

		putUvarint(&body, uint64(len(vd.RequiresDist)))
		for _, req := range vd.RequiresDist {
			// Emit an inline name so the fixture does not depend on a
			// dictionary. Control 0 means "name follows as a string".
			putUvarint(&body, 0)
			putStr(&body, req)
			putStr(&body, "")
		}

		putUvarint(&body, uint64(len(vd.ProvidesExtra)))
		for _, e := range vd.ProvidesExtra {
			putStr(&body, e)
		}
	}

	putUvarint(&body, uint64(len(versions)))
	for i, v := range versions {
		putStr(&body, v)
		putUvarint(&body, uint64(i))
	}

	return string(append([]byte{depsFormatStored}, body.Bytes()...))
}

// buildDepsdictField encodes a depsdict with the given names and no zstd
// dictionary.
func buildDepsdictField(names []string) string {
	var buf bytes.Buffer
	buf.WriteByte(depsdictFormatByte)
	putUvarint(&buf, uint64(len(names)))
	for _, n := range names {
		putStr(&buf, n)
	}
	putUvarint(&buf, 0) // no zstd dictionary
	return buf.String()
}

// writeFixtureRSF writes a real RSF file using the public library's
// reflection-driven writer over this package's own layout structs.
//
// Using the real writer rather than hand-assembling bytes is the point: it means
// the layout structs in record.go are exercised, and the framing (record sizes,
// the snapshots array header, the trailing additive fields) is produced by the
// same code that produces production files rather than by this test's idea of
// the format.
func writeFixtureRSF(t *testing.T, records []PackageRecord) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fixture.rsf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	// WriteObject reflects over the value; a pointer reports
	// "unknown field type 0x16" (reflect.Pointer) rather than being
	// dereferenced, so pass the struct itself.
	w := rsf.NewWriter(f)
	for i := range records {
		if _, err := w.WriteObject(records[i]); err != nil {
			t.Fatalf("writing record %d: %v", i, err)
		}
	}

	return path
}

// fixtureRecords builds a small but representative corpus: two packages with
// dependencies, one with none, and a snapshots array on each so the reader has
// to skip a real array rather than an absent one.
func fixtureRecords(t *testing.T) []PackageRecord {
	t.Helper()

	names := []string{"werkzeug", "jinja2"}
	dictField := buildDepsdictField(names)

	flask := PackageRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots: []SnapshotRecord{
			{Snapshot: "2026080100", Version: "3.0.0", ReleaseDate: "\x00\x01", Summary: "web framework"},
			{Snapshot: "2026080200", Version: "3.0.1", ReleaseDate: "\x00\x02", Summary: "web framework"},
		},
		Deps: buildDepsField(t, map[string]VersionDeps{
			"3.0.0": {
				RequiresDist:   []string{"werkzeug>=3.0", "jinja2>=3.1"},
				RequiresPython: ">=3.8",
				ProvidesExtra:  []string{"async", "dotenv"},
			},
			"3.0.1": {
				RequiresDist:   []string{"werkzeug>=3.0.1"},
				RequiresPython: ">=3.8",
			},
		}, names),
		// The global dictionary belongs on the FIRST record only.
		Depsdict: dictField,
	}

	werkzeug := PackageRecord{
		CanonicalName: "werkzeug",
		ProjectName:   "Werkzeug",
		Snapshots: []SnapshotRecord{
			{Snapshot: "2026080100", Version: "3.0.1", ReleaseDate: "\x00\x01", Summary: "wsgi utils"},
		},
		Deps: buildDepsField(t, map[string]VersionDeps{
			"3.0.1": {RequiresPython: ">=3.8"},
		}, names),
	}

	// A package with an empty deps field: present, nothing captured.
	nodeps := PackageRecord{
		CanonicalName: "nodeps",
		ProjectName:   "NoDeps",
		Snapshots: []SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "nothing"},
		},
	}

	return []PackageRecord{flask, werkzeug, nodeps}
}

func openFixture(t *testing.T) *File {
	t.Helper()

	path := writeFixtureRSF(t, fixtureRecords(t))
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestOpenIndexesEveryRecord(t *testing.T) {
	f := openFixture(t)

	if got := f.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}

	want := []string{"flask", "nodeps", "werkzeug"} // sorted
	got := f.Packages()
	if len(got) != len(want) {
		t.Fatalf("Packages() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Packages()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	for _, name := range want {
		if !f.Has(name) {
			t.Errorf("Has(%q) = false", name)
		}
	}
	if f.Has("absent") {
		t.Error("Has(\"absent\") = true")
	}
}

// TestDepsSkipsTheSnapshotsArrayCorrectly is the core correctness test: it
// proves the reader lands on the right field after skipping a real,
// multi-element snapshots array written by the real writer. An off-by-one in
// that skip desynchronizes silently, so the assertion is on decoded content
// rather than on an error being nil.
func TestDepsSkipsTheSnapshotsArrayCorrectly(t *testing.T) {
	f := openFixture(t)

	deps, err := f.Deps("flask")
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if len(deps) != 2 {
		t.Fatalf("got %d versions, want 2", len(deps))
	}

	v300, ok := deps["3.0.0"]
	if !ok {
		t.Fatal("version 3.0.0 missing")
	}
	if !equalStrings(v300.RequiresDist, []string{"werkzeug>=3.0", "jinja2>=3.1"}) {
		t.Errorf("3.0.0 RequiresDist = %v", v300.RequiresDist)
	}
	if v300.RequiresPython != ">=3.8" {
		t.Errorf("3.0.0 RequiresPython = %q, want >=3.8", v300.RequiresPython)
	}
	if !equalStrings(v300.ProvidesExtra, []string{"async", "dotenv"}) {
		t.Errorf("3.0.0 ProvidesExtra = %v", v300.ProvidesExtra)
	}

	v301, ok := deps["3.0.1"]
	if !ok {
		t.Fatal("version 3.0.1 missing")
	}
	if !equalStrings(v301.RequiresDist, []string{"werkzeug>=3.0.1"}) {
		t.Errorf("3.0.1 RequiresDist = %v", v301.RequiresDist)
	}
}

// TestDepsWorksForARecordAfterTheFirst guards against an indexing bug that only
// shows up past record 0 — the first record is where the dictionary lives and is
// read differently, so a reader can be right there and wrong everywhere else.
func TestDepsWorksForARecordAfterTheFirst(t *testing.T) {
	f := openFixture(t)

	deps, err := f.Deps("werkzeug")
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("got %d versions, want 1", len(deps))
	}
	if deps["3.0.1"].RequiresPython != ">=3.8" {
		t.Errorf("RequiresPython = %q, want >=3.8", deps["3.0.1"].RequiresPython)
	}
}

func TestDepsEmptyFieldIsEmptyNonNilMap(t *testing.T) {
	f := openFixture(t)

	deps, err := f.Deps("nodeps")
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if deps == nil {
		t.Fatal("want an empty non-nil map: present-with-nothing-captured is a different answer from not-found")
	}
	if len(deps) != 0 {
		t.Errorf("got %d versions, want 0", len(deps))
	}
}

func TestDepsUnknownPackage(t *testing.T) {
	f := openFixture(t)

	_, err := f.Deps("absent")
	if !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("got %v, want ErrPackageNotFound", err)
	}
}

func TestDictIsLoadedFromTheFirstRecord(t *testing.T) {
	f := openFixture(t)

	d := f.Dict()
	if d == nil {
		t.Fatal("Dict() is nil")
	}
	if got := d.Names(); !equalStrings(got, []string{"werkzeug", "jinja2"}) {
		t.Errorf("Dict().Names() = %v, want [werkzeug jinja2]", got)
	}
}

// TestConcurrentDepsLookups exists to be run under -race. Lookups share the
// immutable schema and the open file but must not share mutable reader state.
func TestConcurrentDepsLookups(t *testing.T) {
	f := openFixture(t)

	names := []string{"flask", "werkzeug", "nodeps"}

	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			name := names[i%len(names)]
			deps, err := f.Deps(name)
			if err != nil {
				t.Errorf("Deps(%q): %v", name, err)
				return
			}
			// flask must always decode both versions; a torn read would show
			// up as a short or garbled result rather than as an error.
			if name == "flask" && len(deps) != 2 {
				t.Errorf("Deps(flask) returned %d versions, want 2", len(deps))
			}
		}(i)
	}
	wg.Wait()
}

func TestOpenRejectsMissingFile(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.rsf")); err == nil {
		t.Error("expected an error for a missing file")
	}
}

// TestOpenReportsFilesWithoutDependencyData covers the pre-cutover case: a
// well-formed RSF whose schema has no deps fields. Reported at Open, because a
// caller can act on that once but cannot act on every lookup failing.
func TestOpenReportsFilesWithoutDependencyData(t *testing.T) {
	type legacySnapshot struct {
		Deleted  bool   `rsf:"deleted"`
		Snapshot string `rsf:"snapshot,skip,fixed:10"`
		Version  string `rsf:"version"`
	}
	type legacyRecord struct {
		CanonicalName string           `rsf:"cname"`
		ProjectName   string           `rsf:"pname"`
		Snapshots     []legacySnapshot `rsf:"snapshots,index:snapshot"`
	}

	path := filepath.Join(t.TempDir(), "legacy.rsf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	w := rsf.NewWriter(f)
	rec := legacyRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots:     []legacySnapshot{{Snapshot: "2026080100", Version: "3.0.0"}},
	}
	if _, err := w.WriteObject(rec); err != nil {
		t.Fatalf("writing legacy record: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing fixture: %v", err)
	}

	if _, err := Open(path); !errors.Is(err, ErrNoDependencyData) {
		t.Errorf("got %v, want ErrNoDependencyData", err)
	}
}

func TestCloseIsIdempotentEnough(t *testing.T) {
	path := writeFixtureRSF(t, fixtureRecords(t))
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// A second Close reports the file error rather than panicking.
	if err := f.Close(); err == nil {
		t.Log("second Close returned nil; acceptable, just not relied upon")
	}
}
