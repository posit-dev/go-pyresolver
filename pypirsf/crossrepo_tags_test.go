// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"unsafe"
)

// The fixtures these tests read are BYTE COPIES of the producer's own, from
// rstudio/pypi-manifest internal/depsblob/testdata/ on main, and are
// sha256-identical to the copies rstudio/package-manager reads. That is the
// point: all three repos decode the same bytes and state the same meaning, so a
// one-sided encoding change cannot pass in all three. Re-copy them rather than
// regenerating them locally — a fixture this repo produced would only prove this
// decoder agrees with itself.
//
// See testdata/README.md for provenance.

// crossrepoExpected mirrors testdata/crossrepo_tags_expected.json, the
// producer's own statement of what its bytes mean. Decoding the expectations
// rather than restating them in Go is deliberate: if the producer's fixture
// changes, this file does not have to be the thing that notices.
type crossrepoExpected struct {
	Vocabulary []string `json:"vocabulary"`
	Versions   map[string]struct {
		RequiresDist  []string `json:"requires_dist"`
		WheelTags     []string `json:"wheel_tags"`
		HasSdist      bool     `json:"has_sdist"`
		WtagsCaptured bool     `json:"wtags_captured"`
	} `json:"versions"`
}

// crossrepoTagsFixture loads the cross-repo wheel-tag fixture: the raw
// pre-compression blob body as a deps field (stored, so it runs through the real
// decompress path rather than around it), the vocabulary parsed from the raw
// tagsdict field, and the producer's expectations.
func crossrepoTagsFixture(t *testing.T) (string, *TagDict, crossrepoExpected) {
	t.Helper()

	raw, err := os.ReadFile("testdata/crossrepo_tags_tagsdict.bin")
	if err != nil {
		t.Fatalf("reading crossrepo tagsdict: %v", err)
	}
	td, err := ParseTagsdictField(raw)
	if err != nil {
		t.Fatalf("ParseTagsdictField: %v", err)
	}

	blob, err := os.ReadFile("testdata/crossrepo_tags_blob.bin")
	if err != nil {
		t.Fatalf("reading crossrepo blob: %v", err)
	}

	expJSON, err := os.ReadFile("testdata/crossrepo_tags_expected.json")
	if err != nil {
		t.Fatalf("reading crossrepo expectations: %v", err)
	}
	var exp crossrepoExpected
	if err := json.Unmarshal(expJSON, &exp); err != nil {
		t.Fatalf("parsing crossrepo expectations: %v", err)
	}

	return string(append([]byte{depsFormatStored}, blob...)), td, exp
}

// crossrepoDict is the dep-name dictionary the fixture's blob references. The
// producer's fixture encodes "flask>=2.0" and "numpy>=1.26" as dictionary refs 1
// and 2, so the names must be in this order.
func crossrepoDict(t *testing.T) *Dict {
	t.Helper()

	var buf bytes.Buffer
	buf.WriteByte(depsdictFormatByte)
	putUvarint(&buf, 2)
	putStr(&buf, "flask")
	putStr(&buf, "numpy")
	putUvarint(&buf, 0) // no trained zstd dictionary

	d, err := ParseDepsdictField(buf.Bytes())
	if err != nil {
		t.Fatalf("ParseDepsdictField: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestCrossRepoTagsVocabularyRoundTrips is the vocabulary half of the wire
// handshake: ParseTagsdictField must recover exactly the triples the producer
// wrote, in ID order, because the ids are what the blob body references.
func TestCrossRepoTagsVocabularyRoundTrips(t *testing.T) {
	_, td, exp := crossrepoTagsFixture(t)

	if td.Len() != len(exp.Vocabulary) {
		t.Fatalf("vocabulary has %d triples, want %d", td.Len(), len(exp.Vocabulary))
	}
	for i, want := range exp.Vocabulary {
		got, ok := td.Tag(uint64(i + 1))
		if !ok {
			t.Fatalf("vocabulary id %d (%q) must resolve", i+1, want)
		}
		if got != want {
			t.Errorf("id %d = %q, want %q (ids are 1-based and in wire order)", i+1, got, want)
		}
	}
	if !td.Complete() {
		t.Error("the fixture snapshot declares itself complete")
	}

	// Id 0 is never a triple -- it is the literal escape -- and one past the end
	// must not resolve either.
	if _, ok := td.Tag(0); ok {
		t.Error("id 0 is the literal escape, not a triple")
	}
	if _, ok := td.Tag(uint64(len(exp.Vocabulary)) + 1); ok {
		t.Error("an id past the end of the vocabulary must not resolve")
	}

	// py2-none-any is in the vocabulary and used by no version. A decoder that
	// built its vocabulary from the tags it saw would miss it, and would then
	// resolve every later id to the wrong triple.
	if !contains(exp.Vocabulary, "py2-none-any") {
		t.Fatal("fixture invariant: the vocabulary carries an unused entry")
	}
	for ver, want := range exp.Versions {
		if contains(want.WheelTags, "py2-none-any") {
			t.Errorf("fixture invariant: %s must not use the unused vocabulary entry", ver)
		}
	}
}

// TestCrossRepoTagsBlobRoundTrips is this decoder's first end-to-end read of the
// wheel-tag encoding: the producer's bytes, through the real deps-blob decode
// path, must produce exactly the producer's stated per-version meaning.
//
// It asserts the surviving SET of tags per version, never a count. A decoder that
// dropped tags would satisfy a count-shaped assertion for as long as it dropped
// them consistently.
func TestCrossRepoTagsBlobRoundTrips(t *testing.T) {
	field, td, exp := crossrepoTagsFixture(t)

	got, err := DecodePackage(field, crossrepoDict(t), td)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}

	if len(got) != len(exp.Versions) {
		t.Fatalf("decoded %d versions, want %d", len(got), len(exp.Versions))
	}
	for ver, want := range exp.Versions {
		md, ok := got[ver]
		if !ok {
			t.Errorf("version %q missing from decode", ver)
			continue
		}
		if !equalStrings(want.RequiresDist, md.RequiresDist) {
			t.Errorf("%s RequiresDist = %v, want %v", ver, md.RequiresDist, want.RequiresDist)
		}
		if !sameSet(want.WheelTags, md.WheelTags) {
			t.Errorf("%s wheel tags = %v, want the set %v", ver, md.WheelTags, want.WheelTags)
		}
		if md.HasSdist != want.HasSdist {
			t.Errorf("%s HasSdist = %v, want %v", ver, md.HasSdist, want.HasSdist)
		}
		if md.TagsCaptured != want.WtagsCaptured {
			t.Errorf("%s TagsCaptured = %v, want %v", ver, md.TagsCaptured, want.WtagsCaptured)
		}
	}
}

// TestCrossRepoTagsPresenceStatesAreDistinct pins the three per-version states as
// genuinely different values rather than three spellings of "empty". This is the
// fail-open hazard the encoding exists to close: if uncaptured and no-wheels both
// decoded to an empty tag list, a consumer would filter out a version it knows
// nothing about.
func TestCrossRepoTagsPresenceStatesAreDistinct(t *testing.T) {
	field, td, _ := crossrepoTagsFixture(t)

	got, err := DecodePackage(field, crossrepoDict(t), td)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}

	// Non-empty tag set.
	if !equalStrings([]string{"py3-none-any"}, got["1.0.0"].WheelTags) {
		t.Errorf("1.0.0 tags = %v, want [py3-none-any]", got["1.0.0"].WheelTags)
	}
	if !got["1.0.0"].TagsCaptured {
		t.Error("1.0.0 must be captured")
	}

	// Empty set WITH sdist: captured, publishes no wheels, has a source dist.
	if len(got["3.0.0"].WheelTags) != 0 {
		t.Errorf("3.0.0 tags = %v, want none", got["3.0.0"].WheelTags)
	}
	if !got["3.0.0"].TagsCaptured {
		t.Error("3.0.0 was captured and has no wheels")
	}
	if !got["3.0.0"].HasSdist {
		t.Error("3.0.0 has an sdist")
	}

	// Empty set WITHOUT sdist: captured, and nothing installable at all.
	if len(got["4.0.0"].WheelTags) != 0 {
		t.Errorf("4.0.0 tags = %v, want none", got["4.0.0"].WheelTags)
	}
	if !got["4.0.0"].TagsCaptured {
		t.Error("4.0.0 was captured and has no wheels")
	}
	if got["4.0.0"].HasSdist {
		t.Error("4.0.0 has no sdist")
	}

	// Uncaptured: an empty tag list here means "unknown", not "no wheels".
	if len(got["5.0.0"].WheelTags) != 0 {
		t.Errorf("5.0.0 tags = %v, want none", got["5.0.0"].WheelTags)
	}
	if got["5.0.0"].TagsCaptured {
		t.Error("5.0.0 has no tag claim at all")
	}
	if got["5.0.0"].HasSdist {
		t.Error("an uncaptured slot claims no sdist either")
	}

	// 5.0.0 still has its dependencies: an uncaptured tag claim must not cost the
	// deps that share its slot.
	if !equalStrings([]string{"flask>=2.0"}, got["5.0.0"].RequiresDist) {
		t.Errorf("5.0.0 RequiresDist = %v, want [flask>=2.0]", got["5.0.0"].RequiresDist)
	}

	// The three states are distinct VALUES, not three spellings of one. Comparing
	// the decoded records directly is what makes that a claim about the type
	// rather than about the fields this test happened to read.
	if sameRecord(got["3.0.0"], got["4.0.0"]) {
		t.Error("no-wheels-with-sdist and no-wheels-without-sdist must differ")
	}
	if sameRecord(got["3.0.0"], got["5.0.0"]) {
		t.Error("no-wheels-with-sdist and uncaptured must differ")
	}
	if sameRecord(got["4.0.0"], got["5.0.0"]) {
		t.Error("no-wheels-without-sdist and uncaptured must differ")
	}
}

// TestCrossRepoTagsLiteralEscapeDecodes covers the escape for a triple the
// vocabulary does not carry. It is mandatory, not an optimization: the vocabulary
// legitimately lags a freshly published platform tag, and the fixture's 2.0.0
// mixes an escaped tag with a vocabulary ref in one set.
func TestCrossRepoTagsLiteralEscapeDecodes(t *testing.T) {
	field, td, _ := crossrepoTagsFixture(t)

	if _, ok := td.Tag(4); ok {
		t.Fatal("fixture invariant: the escaped tag is outside the vocabulary")
	}

	got, err := DecodePackage(field, crossrepoDict(t), td)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}
	want := []string{"cp313-cp313t-win_arm64", "py3-none-any"}
	if !sameSet(want, got["2.0.0"].WheelTags) {
		t.Errorf("2.0.0 tags = %v, want the set %v (an escaped literal and a ref must both survive)",
			got["2.0.0"].WheelTags, want)
	}
}

// TestCrossRepoTagsSlotIdentityIsDepsAndTags is the widened slot contract, read
// through the only surface this package exposes. A slot is a (dependency set, tag
// list) pair, so 1.1.0 -- same dependencies as 1.0.0, different wheels -- must
// come back with ITS OWN tags. Under the old deps-only identity all three
// versions would share one slot and 1.1.0 would report 1.0.0's tags.
func TestCrossRepoTagsSlotIdentityIsDepsAndTags(t *testing.T) {
	field, td, _ := crossrepoTagsFixture(t)

	got, err := DecodePackage(field, crossrepoDict(t), td)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}

	if !equalStrings(got["1.0.0"].RequiresDist, got["1.1.0"].RequiresDist) {
		t.Fatalf("fixture invariant: 1.0.0 and 1.1.0 declare the same dependencies, got %v and %v",
			got["1.0.0"].RequiresDist, got["1.1.0"].RequiresDist)
	}
	if !equalStrings([]string{"py3-none-any"}, got["1.0.0"].WheelTags) {
		t.Errorf("1.0.0 tags = %v, want [py3-none-any]", got["1.0.0"].WheelTags)
	}
	if !equalStrings([]string{"cp39-cp39-manylinux_2_17_x86_64"}, got["1.1.0"].WheelTags) {
		t.Errorf("1.1.0 tags = %v, want [cp39-cp39-manylinux_2_17_x86_64] -- same deps must not mean same tags",
			got["1.1.0"].WheelTags)
	}
	// 1.0.1 shares both deps and tags with 1.0.0, so it reads identically. This is
	// the other half of the identity rule: widening it must not stop dedup working
	// when the tags DO match.
	if !sameRecord(got["1.0.0"], got["1.0.1"]) {
		t.Error("1.0.1 has the same deps and tags as 1.0.0 and must decode identically")
	}
}

// TestTagsFreeBlobDecodesAsUncaptured is the pre-cutover path: the tags-free
// golden blob ends after its version index, and a reader that now expects a
// trailing tag section must treat its absence as "no tag claim" rather than as
// corruption. Every RSF this resolver has ever read looks like this.
func TestTagsFreeBlobDecodesAsUncaptured(t *testing.T) {
	d := loadGoldenDict(t)
	blob, err := os.ReadFile("testdata/golden_blob.bin")
	if err != nil {
		t.Fatalf("reading golden blob: %v", err)
	}
	field := string(append([]byte{depsFormatStored}, blob...))

	got, err := DecodePackage(field, d, nil)
	if err != nil {
		t.Fatalf("a pre-cutover blob must still decode: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the golden blob decodes to versions")
	}
	for ver, md := range got {
		if md.TagsCaptured {
			t.Errorf("%s has no tag claim", ver)
		}
		if len(md.WheelTags) != 0 {
			t.Errorf("%s has no tags, got %v", ver, md.WheelTags)
		}
		if md.HasSdist {
			t.Errorf("%s claims no sdist", ver)
		}
	}

	// The deps side is untouched by the tag work.
	if !equalStrings([]string{"flask>=2.0", "unknownpkg>=1.0"}, got["1.0.0"].RequiresDist) {
		t.Errorf("1.0.0 RequiresDist = %v, want the golden pair", got["1.0.0"].RequiresDist)
	}
}

// TestTagsFreeBlobDecodesWithAVocabularyPresent is the mixed state during
// rollout: record 0 carries a tagsdict while a carried-forward package blob still
// has no tag section. The vocabulary must not make the reader expect one.
func TestTagsFreeBlobDecodesWithAVocabularyPresent(t *testing.T) {
	d := loadGoldenDict(t)
	blob, err := os.ReadFile("testdata/golden_blob.bin")
	if err != nil {
		t.Fatalf("reading golden blob: %v", err)
	}
	_, td, _ := crossrepoTagsFixture(t)

	got, err := DecodePackage(string(append([]byte{depsFormatStored}, blob...)), d, td)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}
	for ver, md := range got {
		if md.TagsCaptured {
			t.Errorf("%s has no tag claim even though the file has a vocabulary", ver)
		}
	}
}

// TestDecodeTagSectionRejectsTruncationAndDrift separates the two failure modes
// that must NOT be silent. A section that starts and runs out is corruption; a ref
// the vocabulary cannot resolve is the two RSF fields having drifted apart, which
// is the one hazard the append-only rule exists to prevent. Dropping either would
// serve a quietly narrower tag set, which reads as the feature working.
func TestDecodeTagSectionRejectsTruncationAndDrift(t *testing.T) {
	_, td, _ := crossrepoTagsFixture(t)

	// One pool slot, one version, then a tag section that promises two refs and
	// supplies one.
	var buf bytes.Buffer
	putUvarint(&buf, 1) // poolCount
	putStr(&buf, "")    // requires_python
	putUvarint(&buf, 0) // reqCount
	putUvarint(&buf, 0) // extraCount
	putUvarint(&buf, 1) // versionCount
	putStr(&buf, "1.0.0")
	putUvarint(&buf, 0) // slot 0
	base := buf.Bytes()

	truncated := append([]byte{depsFormatStored}, base...)
	truncated = append(truncated, 0x07, 0x01) // control: 2 tags + sdist, one ref
	_, err := DecodePackage(string(truncated), nil, td)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("reading tag ref 1")) {
		t.Errorf("a truncated tag section is corruption, not an absent one; got %v", err)
	}

	drifted := append([]byte{depsFormatStored}, base...)
	drifted = append(drifted, 0x05, 0x63) // control: 1 tag + sdist, ref 99
	_, err = DecodePackage(string(drifted), nil, td)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("tag ref 99 out of range")) {
		t.Errorf("a ref the vocabulary cannot resolve means the fields drifted; got %v", err)
	}

	// The same ref with NO vocabulary at all must also fail, rather than silently
	// yielding a tag-free package. A nil vocabulary is the pre-cutover state, and
	// a blob that references one is not in that state.
	_, err = DecodePackage(string(drifted), nil, nil)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("tag ref 99 out of range")) {
		t.Errorf("a ref with no vocabulary must error, not decode empty; got %v", err)
	}
}

// TestDecodeTagSectionIgnoresTrailingBytes protects the property that let this
// change ship without a deps format-byte bump: a reader stops after the sections
// it knows. If this reader ever rejected trailing bytes, the NEXT section appended
// after the tags would break it -- the same trap the tag section itself was placed
// to avoid.
func TestDecodeTagSectionIgnoresTrailingBytes(t *testing.T) {
	field, td, _ := crossrepoTagsFixture(t)

	got, err := DecodePackage(field+string([]byte{0xde, 0xad, 0xbe, 0xef}), crossrepoDict(t), td)
	if err != nil {
		t.Fatalf("bytes after the tag section must be ignored: %v", err)
	}
	clean, err := DecodePackage(field, crossrepoDict(t), td)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}

	if len(got) != len(clean) {
		t.Fatalf("trailing bytes changed the decode: %d versions vs %d", len(got), len(clean))
	}
	for ver, want := range clean {
		if !sameRecord(want, got[ver]) {
			t.Errorf("%s decoded differently with trailing bytes present", ver)
		}
	}
}

// TestParseTagsdictFieldRejectsCorruption keeps a corrupt vocabulary a real
// error. Degrading to "assume incomplete" would mask a producer bug as a benign
// capability gap, and the consumer would quietly stop filtering.
func TestParseTagsdictFieldRejectsCorruption(t *testing.T) {
	tests := []struct {
		name  string
		field []byte
		want  string
	}{
		{"bad format byte", []byte{0x02, 0x01}, "bad tagsdict format byte"},
		{"bad completeness byte", []byte{0x01, 0x07}, "bad tagsdict completeness byte"},
		{"empty triple", []byte{0x01, 0x01, 0x00}, "tagsdict entry 0 is empty"},
		{"truncated triple", []byte{0x01, 0x01, 0x05, 'p', 'y'}, "exceeds"},
		{"no completeness byte", []byte{0x01}, "EOF"},
		{"empty field", []byte{}, "EOF"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			td, err := ParseTagsdictField(tc.field)
			if err == nil {
				t.Fatalf("want an error containing %q, got vocabulary of %d", tc.want, td.Len())
			}
			if !bytes.Contains([]byte(err.Error()), []byte(tc.want)) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestParseTagsdictFieldIncompleteSnapshot pins that an incomplete snapshot still
// parses and still resolves ids -- it is the completeness flag, not the
// vocabulary, that tells a consumer to disable filtering for the whole file. This
// is the state production is in today.
func TestParseTagsdictFieldIncompleteSnapshot(t *testing.T) {
	var buf bytes.Buffer
	putStr(&buf, "py3-none-any")
	field := append([]byte{tagsdictFormatByte, tagsIncomplete}, buf.Bytes()...)

	td, err := ParseTagsdictField(field)
	if err != nil {
		t.Fatalf("ParseTagsdictField: %v", err)
	}
	if td.Complete() {
		t.Error("an incomplete snapshot must report incomplete, so filtering stays off file-wide")
	}
	got, ok := td.Tag(1)
	if !ok || got != "py3-none-any" {
		t.Errorf("Tag(1) = %q, %v; an incomplete snapshot still resolves the ids it has", got, ok)
	}
}

// TestNilTagDictIsTheExpectedPreCutoverState pins the nil posture: a missing
// vocabulary is the state every RSF is in before the producer cuts over, so every
// method must answer rather than panic.
func TestNilTagDictIsTheExpectedPreCutoverState(t *testing.T) {
	var td *TagDict

	if td.Len() != 0 {
		t.Errorf("nil vocabulary Len = %d, want 0", td.Len())
	}
	if td.Complete() {
		t.Error("no vocabulary means tag filtering is off")
	}
	if _, ok := td.Tag(1); ok {
		t.Error("a nil vocabulary resolves no ids")
	}
}

// TestCrossRepoTagsStringsDoNotAliasTheBlob keeps tag strings independent of the
// decoded blob. A tag that sliced the blob would pin the whole decompressed body
// in memory for as long as any consumer held the tag -- and this decoder's callers
// cache decoded records per package.
func TestCrossRepoTagsStringsDoNotAliasTheBlob(t *testing.T) {
	field, td, exp := crossrepoTagsFixture(t)
	blob := []byte(field[1:])

	got, err := unmarshalBlob(blob, crossrepoDict(t).Names(), td)
	if err != nil {
		t.Fatalf("unmarshalBlob: %v", err)
	}

	checked := 0
	for ver, md := range got {
		for _, tag := range md.WheelTags {
			if stringAliases(tag, blob) {
				t.Errorf("%s tag %q aliases the decoded blob", ver, tag)
			}
			checked++
		}
	}
	// Guards the test itself: a decoder that returned no tags at all would
	// otherwise pass by checking nothing. The expected total comes from the
	// producer's fixture so it cannot drift away from the bytes.
	want := 0
	for _, v := range exp.Versions {
		want += len(v.WheelTags)
	}
	if checked != want {
		t.Errorf("checked %d tag strings, want %d across the fixture's versions", checked, want)
	}
}

// stringAliases reports whether s's bytes live inside buf. Distinct allocations
// cannot overlap, so a data pointer inside buf proves the string was cut from it
// rather than copied out. An empty string has no meaningful data pointer.
func stringAliases(s string, buf []byte) bool {
	if len(s) == 0 || len(buf) == 0 {
		return false
	}
	lo := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	bufLo := uintptr(unsafe.Pointer(unsafe.SliceData(buf)))
	return lo >= bufLo && lo < bufLo+uintptr(len(buf))
}

// sameSet reports whether a and b hold the same elements, ignoring order. Tag
// order within a set is not something the format promises, so asserting the set
// is asserting what the producer actually stated.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}

// sameRecord compares two decoded records field by field, including the tag
// fields, so "these two states are distinct" is a claim about the whole value.
func sameRecord(a, b VersionDeps) bool {
	return a.RequiresPython == b.RequiresPython &&
		equalStrings(a.RequiresDist, b.RequiresDist) &&
		equalStrings(a.ProvidesExtra, b.ProvidesExtra) &&
		equalStrings(a.WheelTags, b.WheelTags) &&
		a.HasSdist == b.HasSdist &&
		a.TagsCaptured == b.TagsCaptured
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
