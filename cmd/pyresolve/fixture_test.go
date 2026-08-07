// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	rsf "github.com/rstudio/repository-snapshot-format"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// --- fixture construction ---
//
// Mirrors index/rsfindex_test.go: it builds a real RSF with the real writer
// so these tests exercise the actual on-disk format, not a mock. Duplicated
// here rather than shared, since a test helper is not something either
// package should export.

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

// buildStoredDepsField encodes a stored (uncompressed) deps blob, so a
// fixture needs no trained zstd dictionary.
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

// writeFixtureRSF writes recs to a new RSF file in t.TempDir() and returns
// its path.
func writeFixtureRSF(t *testing.T, recs []pypirsf.PackageRecord) string {
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
	return path
}

// standardFixture builds a small multi-package RSF exercising: multiple
// captured versions with dependencies that chain to each other (for walk),
// a package with no captured deps, and an unparseable version key.
func standardFixture(t *testing.T) string {
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
				requiresDist:   []string{"werkzeug>=3.0"},
				requiresPython: ">=3.8",
				providesExtra:  []string{"Async"},
			},
			{
				version:        "3.0.1",
				requiresDist:   []string{"werkzeug>=3.0.1", `asgiref>=3.2 ; extra == "async"`},
				requiresPython: ">=3.8",
			},
			// A version key PEP 440 rejects; must not break the rest.
			{version: "not-a-version", requiresDist: []string{"werkzeug"}},
		}),
		Depsdict: buildDepsdictField(),
	}

	werkzeug := pypirsf.PackageRecord{
		CanonicalName: "werkzeug",
		ProjectName:   "Werkzeug",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "3.0.1", ReleaseDate: "\x00\x01", Summary: "wsgi"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "3.0.1", requiresDist: []string{"markupsafe>=2.1"}},
		}),
	}

	markupsafe := pypirsf.PackageRecord{
		CanonicalName: "markupsafe",
		ProjectName:   "MarkupSafe",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "2.1.5", ReleaseDate: "\x00\x01", Summary: "escaping"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{{version: "2.1.5"}}),
	}

	// A dependency that flask's chain never actually reaches directly, used
	// to prove unreferenced packages are absent from a walk.
	requests := pypirsf.PackageRecord{
		CanonicalName: "requests",
		ProjectName:   "requests",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "2.32.0", ReleaseDate: "\x00\x01", Summary: "http"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{{version: "2.32.0"}}),
	}

	nodeps := pypirsf.PackageRecord{
		CanonicalName: "nodeps",
		ProjectName:   "NoDeps",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
	}

	// A record whose Requires-Python cannot be parsed. Deliberately its own
	// package rather than another flask version: flask's highest version is
	// asserted to be 3.0.1 by TestDepsCmdOmittedVersionUsesHighest, so adding a
	// higher one here would silently retarget that test.
	//
	// ">= 3.8 or whatever" is representative of the real shape -- prose where a
	// specifier belongs -- and the point is that it is NOT empty. An absent
	// Requires-Python and an unreadable one are different facts, and both used
	// to print "(unconstrained)".
	badPython := pypirsf.PackageRecord{
		CanonicalName: "badpython",
		ProjectName:   "BadPython",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresPython: ">= 3.8 or whatever"},
		}),
	}

	return writeFixtureRSF(t, []pypirsf.PackageRecord{
		flask, werkzeug, markupsafe, requests, nodeps, badPython,
	})
}

// unusableMidChainFixture builds a graph where a package REACHED TRANSITIVELY
// carries a Requires-Dist entry PEP 508 rejects.
//
// The shape is what matters. "ubad" sits beside "ugood", which has its own child,
// so a traversal that aborts on the bad entry loses "uleaf" as well — work it had
// no reason to discard. That is the production failure in miniature: one malformed
// entry several hops from the root cost 507 root packages their entire walk.
func unusableMidChainFixture(t *testing.T) string {
	t.Helper()

	root := pypirsf.PackageRecord{
		CanonicalName: "uroot",
		ProjectName:   "URoot",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresDist: []string{"ugood", "ubad"}},
		}),
	}

	good := pypirsf.PackageRecord{
		CanonicalName: "ugood",
		ProjectName:   "UGood",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresDist: []string{"uleaf"}},
		}),
	}

	bad := pypirsf.PackageRecord{
		CanonicalName: "ubad",
		ProjectName:   "UBad",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresDist: []string{"!!! not a requirement"}},
		}),
	}

	leaf := pypirsf.PackageRecord{
		CanonicalName: "uleaf",
		ProjectName:   "ULeaf",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{{version: "1.0"}}),
	}

	return writeFixtureRSF(t, []pypirsf.PackageRecord{root, good, bad, leaf})
}

// allKeysUnparseableFixture builds a package whose ONLY stored version key is one
// PEP 440 rejects, while carrying real dependency data underneath.
//
// This is the shape the standard fixture could not express. "nodeps" there has no
// dependency field at all, and "flask" has a bad key ALONGSIDE good ones — neither
// produces the state that matters here, which is an empty version list from a
// package that does have captured data. On a production snapshot `holygrail` is
// exactly this: one key, "0.2.1.Perceval", requiring sqlobject.
func allKeysUnparseableFixture(t *testing.T) string {
	t.Helper()

	rec := pypirsf.PackageRecord{
		CanonicalName: "onlybadkeys",
		ProjectName:   "OnlyBadKeys",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "0.2.1.Perceval", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "0.2.1.Perceval", requiresDist: []string{"sqlobject"}},
		}),
	}

	return writeFixtureRSF(t, []pypirsf.PackageRecord{rec})
}

// absentAndUncapturedFixture builds a root with two dependencies that fail in
// DIFFERENT ways: one has no record in this RSF at all, the other has a record
// but no captured dependency data.
//
// Extracted from TestWalkCmdDistinguishesAbsentFromUncaptured, which built it
// inline, so the reachable-set tests can use the same shape rather than growing
// a third near-duplicate. Both states are needed together: the whole point is
// that they are not interchangeable.
//
// The root is named "aroot" rather than "root" so it sorts ahead of its
// dependencies, which keeps the text-output assertions stable.
func absentAndUncapturedFixture(t *testing.T) string {
	t.Helper()

	root := pypirsf.PackageRecord{
		CanonicalName: "aroot",
		ProjectName:   "ARoot",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresDist: []string{"present-but-empty", "totally-absent"}},
		}),
		Depsdict: buildDepsdictField(),
	}
	// Present in the file, but no deps field at all, so no captured versions.
	presentButEmpty := pypirsf.PackageRecord{
		CanonicalName: "present-but-empty",
		ProjectName:   "PresentButEmpty",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "9.9", ReleaseDate: "\x00\x01", Summary: "x"},
		},
	}

	return writeFixtureRSF(t, []pypirsf.PackageRecord{root, presentButEmpty})
}

// noDepsDataFixture builds an RSF with a schema that has no dependency
// fields at all, exercising pypirsf.ErrNoDependencyData.
func noDepsDataFixture(t *testing.T) string {
	t.Helper()

	type legacyRecord struct {
		CanonicalName string `rsf:"cname"`
		ProjectName   string `rsf:"pname"`
	}

	path := filepath.Join(t.TempDir(), "legacy.rsf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating fixture: %v", err)
	}
	w := rsf.NewWriter(f)
	if _, err := w.WriteObject(legacyRecord{CanonicalName: "flask", ProjectName: "Flask"}); err != nil {
		t.Fatalf("writing record: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing fixture: %v", err)
	}
	return path
}

// directURLFixture builds a package requiring another BY URL, where a package of
// that same name also exists in the file with its own dependency.
//
// The collision is the whole point. PEP 508's "name @ url" form pins the
// requirement to that distribution, so the name is a local label rather than a
// lookup key. Walking into the index by name substitutes an unrelated project —
// and then follows ITS dependencies too, which is how one wrong edge becomes a
// wrong subtree. On a production snapshot 87 of 98 direct-reference labels collide
// with a real package, including ipython, marshmallow and vyper.
func directURLFixture(t *testing.T) string {
	t.Helper()

	root := pypirsf.PackageRecord{
		CanonicalName: "durroot",
		ProjectName:   "DURRoot",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "1.0", requiresDist: []string{
				"durplain",
				"durlabel @ git+https://github.com/example/Other@main",
			}},
		}),
	}

	// An ordinary dependency, to show the walk still follows those.
	plain := pypirsf.PackageRecord{
		CanonicalName: "durplain",
		ProjectName:   "DURPlain",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{{version: "1.0"}}),
	}

	// The impostor: same name as the URL requirement's label, unrelated project,
	// with a dependency of its own that must NOT be pulled in.
	impostor := pypirsf.PackageRecord{
		CanonicalName: "durlabel",
		ProjectName:   "DURLabel",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "9.9", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{
			{version: "9.9", requiresDist: []string{"durimpostordep"}},
		}),
	}

	impostorDep := pypirsf.PackageRecord{
		CanonicalName: "durimpostordep",
		ProjectName:   "DURImpostorDep",
		Snapshots: []pypirsf.SnapshotRecord{
			{Snapshot: "2026080100", Version: "1.0", ReleaseDate: "\x00\x01", Summary: "x"},
		},
		Deps: buildStoredDepsField([]fixtureVersion{{version: "1.0"}}),
	}

	return writeFixtureRSF(t, []pypirsf.PackageRecord{root, plain, impostor, impostorDep})
}
