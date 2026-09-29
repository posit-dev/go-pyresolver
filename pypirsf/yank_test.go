// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"testing"
)

// buildDepsFieldWithYank builds the same fixture shape as buildDepsField, then
// appends a yank section (and, if withTagSection, a zero-byte "uncaptured tag
// claim" pad first, matching the byte-layout contract's rule that a tag
// section must be present whenever a yank section is).
func buildDepsFieldWithYank(t *testing.T, byVersion map[string]VersionDeps, names []string, yankedVersions []string, withTagSection bool) string {
	t.Helper()

	plain := buildDepsField(t, byVersion, names)
	versions := sortedVersionKeys(byVersion)

	yanked := make(map[string]bool, len(yankedVersions))
	for _, v := range yankedVersions {
		yanked[v] = true
	}

	var tail bytes.Buffer
	if withTagSection {
		for range versions {
			tail.WriteByte(tagsUncaptured)
		}
	}

	prev := -1
	var idxs []int
	for i, v := range versions {
		if yanked[v] {
			idxs = append(idxs, i)
		}
	}
	putUvarint(&tail, uint64(len(idxs)))
	for _, idx := range idxs {
		putUvarint(&tail, uint64(idx-prev-1))
		prev = idx
	}

	return plain + tail.String()
}

func TestYankRoundTrip(t *testing.T) {
	names := []string{"flask"}
	byVersion := map[string]VersionDeps{
		"1.0.0": {RequiresPython: ">=3.8"},
		"2.0.0": {RequiresPython: ">=3.9"},
		"3.0.0": {RequiresPython: ">=3.10"},
	}
	field := buildDepsFieldWithYank(t, byVersion, names, []string{"2.0.0"}, true)

	d, err := ParseDepsdictField([]byte(buildDepsdictField(names)))
	if err != nil {
		t.Fatalf("ParseDepsdictField: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	got, err := DecodePackage(field, d, nil)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}

	for ver, vd := range got {
		want := ver == "2.0.0"
		if vd.Yanked != want {
			t.Errorf("version %s: Yanked = %v, want %v", ver, vd.Yanked, want)
		}
	}
}

func TestYankNoMarkerMeansNothingCaptured(t *testing.T) {
	names := []string{"flask"}
	byVersion := map[string]VersionDeps{
		"1.0.0": {},
		"2.0.0": {},
	}
	// No trailing bytes at all: no tag section, no yank section.
	field := buildDepsField(t, byVersion, names)

	d, err := ParseDepsdictField([]byte(buildDepsdictField(names)))
	if err != nil {
		t.Fatalf("ParseDepsdictField: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	blob, err := decompress(field, d)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	captured, err := hasTrailingYankSection(blob, d.Names(), nil)
	if err != nil {
		t.Fatalf("hasTrailingYankSection: %v", err)
	}
	if captured {
		t.Fatal("hasTrailingYankSection = true for a field with no trailing bytes at all")
	}

	got, err := DecodePackage(field, d, nil)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}
	for ver, vd := range got {
		if vd.Yanked {
			t.Errorf("version %s: Yanked = true with no yank section present", ver)
		}
	}
}

// TestYankSectionDoesNotChangeTagDecode proves old-reader compatibility: a
// blob with a trailing yank section decodes identically, on the plain
// decodeTagSection path, to the same blob without one -- v0.12.0's decoder
// (which only reads that far) sees no difference.
func TestYankSectionDoesNotChangeTagDecode(t *testing.T) {
	names := []string{"flask"}
	byVersion := map[string]VersionDeps{
		"1.0.0": {RequiresPython: ">=3.8"},
		"2.0.0": {RequiresPython: ">=3.9"},
	}

	plain := buildDepsField(t, byVersion, names)
	withYank := buildDepsFieldWithYank(t, byVersion, names, []string{"2.0.0"}, true)

	d, err := ParseDepsdictField([]byte(buildDepsdictField(names)))
	if err != nil {
		t.Fatalf("ParseDepsdictField: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	plainBlob, err := decompress(plain, d)
	if err != nil {
		t.Fatalf("decompress plain: %v", err)
	}
	yankBlob, err := decompress(withYank, d)
	if err != nil {
		t.Fatalf("decompress withYank: %v", err)
	}

	_, poolPlain, _, err := decodeBlobBody(plainBlob, d.Names())
	if err != nil {
		t.Fatalf("decodeBlobBody plain: %v", err)
	}
	rPlain, poolPlain2, _, err := decodeBlobBody(plainBlob, d.Names())
	if err != nil {
		t.Fatalf("decodeBlobBody plain 2: %v", err)
	}
	_ = poolPlain
	if err := decodeTagSection(rPlain, poolPlain2, nil); err != nil {
		t.Fatalf("decodeTagSection plain: %v", err)
	}

	rYank, poolYank, _, err := decodeBlobBody(yankBlob, d.Names())
	if err != nil {
		t.Fatalf("decodeBlobBody withYank: %v", err)
	}
	if err := decodeTagSection(rYank, poolYank, nil); err != nil {
		t.Fatalf("decodeTagSection withYank: %v", err)
	}

	if len(poolPlain2) != len(poolYank) {
		t.Fatalf("pool length differs: %d vs %d", len(poolPlain2), len(poolYank))
	}
	for i := range poolPlain2 {
		if !sameRecord(poolPlain2[i], poolYank[i]) {
			t.Errorf("slot %d: tag decode differs with a yank section present", i)
		}
	}
}

func TestYankMalformedSectionErrors(t *testing.T) {
	names := []string{"flask"}
	byVersion := map[string]VersionDeps{
		"1.0.0": {},
		"2.0.0": {},
		"3.0.0": {},
	}
	d, err := ParseDepsdictField([]byte(buildDepsdictField(names)))
	if err != nil {
		t.Fatalf("ParseDepsdictField: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// base has a tag-section pad but deliberately no yank section of its own:
	// each subtest below supplies the (malformed) yank bytes as everything
	// trailing the tag section, since a well-formed empty yank section would
	// be consumed and any further bytes ignored, same as the tag section's own
	// trailing-bytes rule.
	base := buildDepsField(t, byVersion, names)
	for range sortedVersionKeys(byVersion) {
		base += string([]byte{tagsUncaptured})
	}

	t.Run("truncated mid-uvarint", func(t *testing.T) {
		// A yank count uvarint with the continuation bit set and nothing after.
		field := base + string([]byte{0x81})
		if _, err := DecodePackage(field, d, nil); err == nil {
			t.Fatal("expected a decode error for a truncated yank section")
		}
	})

	t.Run("index out of range", func(t *testing.T) {
		var tail bytes.Buffer
		putUvarint(&tail, 1)
		putUvarint(&tail, 10) // idx = 0+10+1 = 11, versionCount is 3
		field := base + tail.String()
		if _, err := DecodePackage(field, d, nil); err == nil {
			t.Fatal("expected a decode error for an out-of-range yank index")
		}
	})

	// The delta encoding is unsigned, so a normal read always has idx > prev --
	// "non-ascending" is only reachable through a delta large enough to
	// overflow int on the add, which is itself a form of corruption a decoder
	// must catch rather than silently wrap.
	t.Run("non-ascending via overflow", func(t *testing.T) {
		var tail bytes.Buffer
		putUvarint(&tail, 1)
		putUvarint(&tail, uint64(1)<<63)
		field := base + tail.String()
		if _, err := DecodePackage(field, d, nil); err == nil {
			t.Fatal("expected a decode error for an overflowing yank delta")
		}
	})
}

func TestEnsureYankSectionAddsMarkerOnce(t *testing.T) {
	names := []string{"flask"}
	byVersion := map[string]VersionDeps{
		"1.0.0": {RequiresPython: ">=3.8"},
		"2.0.0": {RequiresPython: ">=3.9"},
	}
	d, err := ParseDepsdictField([]byte(buildDepsdictField(names)))
	if err != nil {
		t.Fatalf("ParseDepsdictField: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// No tag section, no yank section at all.
	field := buildDepsField(t, byVersion, names)
	blob, err := decompress(field, d)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if captured, err := hasTrailingYankSection(blob, d.Names(), nil); err != nil || captured {
		t.Fatalf("precondition: expected no marker, got captured=%v err=%v", captured, err)
	}

	marked, err := EnsureYankSection(field, d, nil)
	if err != nil {
		t.Fatalf("EnsureYankSection: %v", err)
	}
	markedBlob, err := decompress(marked, d)
	if err != nil {
		t.Fatalf("decompress marked: %v", err)
	}
	captured, err := hasTrailingYankSection(markedBlob, d.Names(), nil)
	if err != nil {
		t.Fatalf("hasTrailingYankSection: %v", err)
	}
	if !captured {
		t.Fatal("EnsureYankSection did not add a detectable marker")
	}

	// The deps themselves must decode unchanged.
	got, err := DecodePackage(marked, d, nil)
	if err != nil {
		t.Fatalf("DecodePackage(marked): %v", err)
	}
	for ver, vd := range got {
		if vd.Yanked {
			t.Errorf("version %s: Yanked = true, EnsureYankSection must not yank anything", ver)
		}
		if vd.RequiresPython != byVersion[ver].RequiresPython {
			t.Errorf("version %s: RequiresPython changed by EnsureYankSection", ver)
		}
	}

	// Calling it again on an already-marked field must be a no-op.
	again, err := EnsureYankSection(marked, d, nil)
	if err != nil {
		t.Fatalf("EnsureYankSection (idempotent): %v", err)
	}
	if again != marked {
		t.Fatal("EnsureYankSection changed a field that already had a yank section")
	}
}

func TestMinimalYankMarkerField(t *testing.T) {
	field := MinimalYankMarkerField()
	blob, err := decompress(field, &Dict{})
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	captured, err := hasTrailingYankSection(blob, nil, nil)
	if err != nil {
		t.Fatalf("hasTrailingYankSection: %v", err)
	}
	if !captured {
		t.Fatal("MinimalYankMarkerField does not carry a detectable marker")
	}

	deps, err := DecodePackage(field, &Dict{}, nil)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("expected no versions, got %d", len(deps))
	}
}

// TestFileYanksCapturedNeedsRecordZeroMarker proves the v0.13.0 design-B
// record-0 marker is superseded: it never drives YanksCaptured() any more,
// with or without a sentinel record. See TestLDSentinelDrivesYanksCaptured for
// the mechanism that replaced it.
func TestFileYanksCapturedNeedsRecordZeroMarker(t *testing.T) {
	names := []string{"werkzeug", "jinja2"}
	dictField := buildDepsdictField(names)

	byVersion := map[string]VersionDeps{
		"3.0.0": {RequiresPython: ">=3.8"},
	}

	t.Run("no marker anywhere", func(t *testing.T) {
		flask := PackageRecord{
			CanonicalName: "flask",
			ProjectName:   "Flask",
			Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "3.0.0", ReleaseDate: "\x00\x01", Summary: "s"}},
			Deps:          buildDepsField(t, byVersion, names),
			Depsdict:      dictField,
		}
		path := writeFixtureRSF(t, []PackageRecord{flask})
		f, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = f.Close() }()

		if f.YanksCaptured() {
			t.Error("YanksCaptured() = true with no yank section in the file at all")
		}
	})

	t.Run("marker on record 0", func(t *testing.T) {
		marked, err := EnsureYankSection(buildDepsField(t, byVersion, names), &Dict{}, nil)
		if err != nil {
			t.Fatalf("EnsureYankSection: %v", err)
		}
		flask := PackageRecord{
			CanonicalName: "flask",
			ProjectName:   "Flask",
			Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "3.0.0", ReleaseDate: "\x00\x01", Summary: "s"}},
			Deps:          marked,
			Depsdict:      dictField,
		}
		path := writeFixtureRSF(t, []PackageRecord{flask})
		f, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = f.Close() }()

		if f.YanksCaptured() {
			t.Error("YanksCaptured() = true from a superseded design-B marker with no sentinel record")
		}
	})

	t.Run("marker only on a later record does not count", func(t *testing.T) {
		marked, err := EnsureYankSection(buildDepsField(t, byVersion, names), &Dict{}, nil)
		if err != nil {
			t.Fatalf("EnsureYankSection: %v", err)
		}
		flask := PackageRecord{
			CanonicalName: "flask",
			ProjectName:   "Flask",
			Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "3.0.0", ReleaseDate: "\x00\x01", Summary: "s"}},
			Deps:          buildDepsField(t, byVersion, names), // record 0: no marker
			Depsdict:      dictField,
		}
		werkzeug := PackageRecord{
			CanonicalName: "werkzeug",
			ProjectName:   "Werkzeug",
			Snapshots:     []SnapshotRecord{{Snapshot: "2026080100", Version: "3.0.1", ReleaseDate: "\x00\x01", Summary: "s"}},
			Deps:          marked,
		}
		path := writeFixtureRSF(t, []PackageRecord{flask, werkzeug})
		f, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = f.Close() }()

		if f.YanksCaptured() {
			t.Error("YanksCaptured() = true from a non-record-0 marker")
		}
	})
}
