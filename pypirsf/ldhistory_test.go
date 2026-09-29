// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"sort"
	"testing"
)

// historyTransition is the test-side mirror of yankTransition, used to build
// sentinel payloads without reaching into the unexported decode types.
type historyTransition struct {
	key    string
	yanked bool
}

// buildYankHistoryDepsField builds a sentinel record's deps field per S2's
// wire format (rstudio/pypi-manifest internal/manifest/metadata/yankhistory.go):
// format-version byte, captured byte, then the package/version/transition
// tree.
func buildYankHistoryDepsField(captured bool, byPkg map[string]map[string][]historyTransition) string {
	var buf bytes.Buffer
	buf.WriteByte(1) // format version
	if captured {
		buf.WriteByte(1)
	} else {
		buf.WriteByte(0)
	}

	cnames := make([]string, 0, len(byPkg))
	for cname := range byPkg {
		cnames = append(cnames, cname)
	}
	sort.Strings(cnames)

	putUvarint(&buf, uint64(len(cnames)))
	for _, cname := range cnames {
		putStr(&buf, cname)

		versions := byPkg[cname]
		vnames := make([]string, 0, len(versions))
		for v := range versions {
			vnames = append(vnames, v)
		}
		sort.Strings(vnames)

		putUvarint(&buf, uint64(len(vnames)))
		for _, v := range vnames {
			putStr(&buf, v)
			trans := versions[v]
			putUvarint(&buf, uint64(len(trans)))
			for _, tr := range trans {
				putStr(&buf, tr.key)
				if tr.yanked {
					buf.WriteByte(1)
				} else {
					buf.WriteByte(0)
				}
			}
		}
	}

	return string(append([]byte{depsFormatStored}, buf.Bytes()...))
}

// sentinelRecord returns the yank-history sentinel as a PackageRecord, so
// tests write it with the real writer alongside real package records.
func sentinelRecord(field string) PackageRecord {
	return PackageRecord{CanonicalName: ldSentinelCname, ProjectName: ldSentinelCname, Deps: field}
}

// TestLDSentinelDrivesYanksCaptured proves YanksCaptured() answers from the
// sentinel's own captured marker byte, not merely the record's presence.
func TestLDSentinelDrivesYanksCaptured(t *testing.T) {
	names := []string{"werkzeug"}
	dictField := buildDepsdictField(names)
	flask := PackageRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "1.0.0", ReleaseDate: "\x00\x01", Summary: "s"}},
		Deps:          buildDepsField(t, map[string]VersionDeps{"1.0.0": {}}, names),
		Depsdict:      dictField,
	}

	t.Run("no sentinel at all", func(t *testing.T) {
		path := writeFixtureRSF(t, []PackageRecord{flask})
		f, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = f.Close() }()
		if f.YanksCaptured() {
			t.Error("YanksCaptured() = true with no sentinel record")
		}
	})

	t.Run("sentinel present but not captured", func(t *testing.T) {
		field := buildYankHistoryDepsField(false, map[string]map[string][]historyTransition{
			"flask": {"1.0.0": {{key: "2026080100", yanked: true}}},
		})
		path := writeFixtureRSF(t, []PackageRecord{flask, sentinelRecord(field)})
		f, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = f.Close() }()
		if f.YanksCaptured() {
			t.Error("YanksCaptured() = true with captured=false in the sentinel payload")
		}
	})

	t.Run("sentinel present and captured", func(t *testing.T) {
		field := buildYankHistoryDepsField(true, map[string]map[string][]historyTransition{
			"flask": {"1.0.0": {{key: "2026080100", yanked: true}}},
		})
		path := writeFixtureRSF(t, []PackageRecord{flask, sentinelRecord(field)})
		f, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = f.Close() }()
		if !f.YanksCaptured() {
			t.Error("YanksCaptured() = false with captured=true in the sentinel payload")
		}
	})
}

// TestLDSentinelHiddenFromListings proves the sentinel record is not a
// package: Len, Packages, and Has must not see it, and Deps must reject it
// like any other unknown name.
func TestLDSentinelHiddenFromListings(t *testing.T) {
	names := []string{"werkzeug"}
	flask := PackageRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "1.0.0", ReleaseDate: "\x00\x01", Summary: "s"}},
		Deps:          buildDepsField(t, map[string]VersionDeps{"1.0.0": {}}, names),
		Depsdict:      buildDepsdictField(names),
	}
	field := buildYankHistoryDepsField(true, map[string]map[string][]historyTransition{
		"flask": {"1.0.0": {{key: "2026080100", yanked: true}}},
	})
	path := writeFixtureRSF(t, []PackageRecord{flask, sentinelRecord(field)})
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()

	if got := f.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1 (sentinel must not count)", got)
	}
	for _, name := range f.Packages() {
		if name == ldSentinelCname {
			t.Fatal("Packages() lists the sentinel record")
		}
	}
	if f.Has(ldSentinelCname) {
		t.Error("Has() reports the sentinel record as a package")
	}
	if _, err := f.Deps(ldSentinelCname); err == nil {
		t.Error("Deps() on the sentinel name did not error")
	}
}

// TestLDLatestStateAfterTransitions covers both halves of the ruling: a
// yanked-then-unyanked version is selected again, and a package with several
// versions and several transitions each decodes correctly.
func TestLDLatestStateAfterTransitions(t *testing.T) {
	names := []string{"werkzeug"}
	byVersion := map[string]VersionDeps{
		"1.0.0": {}, // never yanked
		"2.0.0": {}, // yanked, no unyank: latest = yanked
		"3.0.0": {}, // yanked then unyanked: latest = clean
		"4.0.0": {}, // yanked, unyanked, re-yanked: latest = yanked
	}
	flask := PackageRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "1.0.0", ReleaseDate: "\x00\x01", Summary: "s"}},
		Deps:          buildDepsField(t, byVersion, names),
		Depsdict:      buildDepsdictField(names),
	}
	field := buildYankHistoryDepsField(true, map[string]map[string][]historyTransition{
		"flask": {
			"2.0.0": {{key: "1000000000", yanked: true}},
			"3.0.0": {
				{key: "1000000000", yanked: true},
				{key: "1000000100", yanked: false},
			},
			"4.0.0": {
				{key: "1000000000", yanked: true},
				{key: "1000000100", yanked: false},
				{key: "1000000200", yanked: true},
			},
		},
	})
	path := writeFixtureRSF(t, []PackageRecord{flask, sentinelRecord(field)})
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()

	deps, err := f.Deps("flask")
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	want := map[string]bool{"1.0.0": false, "2.0.0": true, "3.0.0": false, "4.0.0": true}
	for ver, wantYanked := range want {
		if got := deps[ver].Yanked; got != wantYanked {
			t.Errorf("version %s: Yanked = %v, want %v", ver, got, wantYanked)
		}
	}
}

// TestLDReadsLastTransitionNotFirst is the mutation-proof test for reading
// the OLDEST element/transition instead of the latest: a version yanked then
// unyanked must read as clean, which only holds if the reader looks at the
// LAST transition rather than the first.
func TestLDReadsLastTransitionNotFirst(t *testing.T) {
	names := []string{"werkzeug"}
	flask := PackageRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "1.1.0", ReleaseDate: "\x00\x01", Summary: "s"}},
		Deps:          buildDepsField(t, map[string]VersionDeps{"1.1.0": {}}, names),
		Depsdict:      buildDepsdictField(names),
	}
	field := buildYankHistoryDepsField(true, map[string]map[string][]historyTransition{
		"flask": {"1.1.0": {
			{key: "1000000000", yanked: true},
			{key: "1000000100", yanked: false},
		}},
	})
	path := writeFixtureRSF(t, []PackageRecord{flask, sentinelRecord(field)})
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()

	deps, err := f.Deps("flask")
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if deps["1.1.0"].Yanked {
		t.Error("Yanked = true: reader used the first (oldest) transition instead of the last")
	}
}

// TestLDNoSentinelTreatsEveryVersionAsNotYanked is the per-package test the
// brief's "unknown vs none" rule requires. LD's payload carries no per-
// package or per-version unknown state -- only the file-wide captured bit
// (see decodeYankHistory's doc) -- so "unknown" here means: no sentinel
// record survives to say otherwise, and every version must read as clean.
func TestLDNoSentinelTreatsEveryVersionAsNotYanked(t *testing.T) {
	names := []string{"werkzeug"}
	flask := PackageRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "1.0.0", ReleaseDate: "\x00\x01", Summary: "s"}},
		Deps:          buildDepsField(t, map[string]VersionDeps{"1.0.0": {}}, names),
		Depsdict:      buildDepsdictField(names),
	}
	path := writeFixtureRSF(t, []PackageRecord{flask})
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()

	if f.YanksCaptured() {
		t.Fatal("YanksCaptured() = true with no sentinel in the file")
	}
	deps, err := f.Deps("flask")
	if err != nil {
		t.Fatalf("Deps: %v", err)
	}
	if deps["1.0.0"].Yanked {
		t.Error("Yanked = true with no yank data anywhere in the file")
	}
}

// TestLDDoesNotMistakeDesignBForHistory proves a v0.13.0 design-B trailing
// yank section on record 0 does not drive YanksCaptured() and is not
// confused with the sentinel mechanism, since detection is by cname, not by
// trailing bytes. Mutation-proof: if File.scan still computed yanksCaptured
// from record 0's trailing bytes (the superseded design-B path) instead of
// the sentinel's own marker, this goes RED.
func TestLDDoesNotMistakeDesignBForHistory(t *testing.T) {
	names := []string{"werkzeug"}
	marked, err := EnsureYankSection(buildDepsField(t, map[string]VersionDeps{"1.0.0": {}}, names), &Dict{}, nil)
	if err != nil {
		t.Fatalf("EnsureYankSection: %v", err)
	}
	flask := PackageRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "1.0.0", ReleaseDate: "\x00\x01", Summary: "s"}},
		Deps:          marked,
		Depsdict:      buildDepsdictField(names),
	}
	path := writeFixtureRSF(t, []PackageRecord{flask})
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()

	if f.YanksCaptured() {
		t.Error("YanksCaptured() = true from a stray design-B marker with no sentinel record: " +
			"design-B must not enable filtering under the new layout")
	}
}

// TestLDHugeCountsAreCappedNotAllocated proves numPkgs and numVers go through
// capHint like every other count in this package, instead of sizing the map
// directly: a payload that declares a huge count but does not back it with
// real data must fail with a clean decode error, not attempt an oversized
// map allocation.
func TestLDHugeCountsAreCappedNotAllocated(t *testing.T) {
	t.Run("huge numPkgs", func(t *testing.T) {
		var buf bytes.Buffer
		buf.WriteByte(1) // format version
		buf.WriteByte(1) // captured
		putUvarint(&buf, 1<<62)
		// No package data follows: a real payload this large would be many
		// times the file's own size.
		if _, _, err := decodeYankHistory(buf.Bytes()); err == nil {
			t.Fatal("decodeYankHistory: want error for a numPkgs the payload cannot back, got nil")
		}
	})

	t.Run("huge numVers", func(t *testing.T) {
		var buf bytes.Buffer
		buf.WriteByte(1)        // format version
		buf.WriteByte(1)        // captured
		putUvarint(&buf, 1)     // numPkgs
		putStr(&buf, "flask")   // cname
		putUvarint(&buf, 1<<62) // numVers
		// No version data follows.
		if _, _, err := decodeYankHistory(buf.Bytes()); err == nil {
			t.Fatal("decodeYankHistory: want error for a numVers the payload cannot back, got nil")
		}
	})
}

// TestLDCorruptSentinelFailsOpen proves a sentinel record that exists but
// fails to decode (bad format version here) degrades to the same safe
// default as no sentinel at all, rather than failing Open() for the whole
// file: YanksCaptured() is false, HistoryError() reports why, and every real
// package still loads.
func TestLDCorruptSentinelFailsOpen(t *testing.T) {
	names := []string{"werkzeug"}
	flask := PackageRecord{
		CanonicalName: "flask",
		ProjectName:   "Flask",
		Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "1.0.0", ReleaseDate: "\x00\x01", Summary: "s"}},
		Deps:          buildDepsField(t, map[string]VersionDeps{"1.0.0": {}}, names),
		Depsdict:      buildDepsdictField(names),
	}

	var corrupt bytes.Buffer
	corrupt.WriteByte(99) // unsupported format version
	field := string(append([]byte{depsFormatStored}, corrupt.Bytes()...))

	path := writeFixtureRSF(t, []PackageRecord{flask, sentinelRecord(field)})
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open: want no error for a corrupt sentinel, got %v", err)
	}
	defer func() { _ = f.Close() }()

	if f.YanksCaptured() {
		t.Error("YanksCaptured() = true with a corrupt sentinel payload")
	}
	if f.HistoryError() == nil {
		t.Error("HistoryError() = nil, want the decode error to be recorded")
	}
	if f.Len() != 1 {
		t.Errorf("Len() = %d, want 1 (flask must still load)", f.Len())
	}
	deps, err := f.Deps("flask")
	if err != nil {
		t.Fatalf("Deps: want flask to still load, got %v", err)
	}
	if deps["1.0.0"].Yanked {
		t.Error("Yanked = true with no usable yank data")
	}
}
