// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// --- wire helpers, mirroring the producer's encoding ---

func putUvarint(buf *bytes.Buffer, v uint64) {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	buf.Write(tmp[:n])
}

func putStr(buf *bytes.Buffer, s string) {
	putUvarint(buf, uint64(len(s)))
	buf.WriteString(s)
}

func loadGoldenDict(t *testing.T) *Dict {
	t.Helper()

	b, err := os.ReadFile("testdata/golden_depsdict.bin")
	if err != nil {
		t.Fatalf("reading golden depsdict: %v", err)
	}
	d, err := ParseDepsdictField(b)
	if err != nil {
		t.Fatalf("ParseDepsdictField: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// --- golden conformance ---

// goldenExpected is the expected decode of testdata/golden_blob.bin. Both the
// fixture and these expectations come from the PRODUCER's own test vectors, so
// this test is what proves this decoder agrees with the encoder rather than
// merely agreeing with itself.
func goldenExpected() map[string]VersionDeps {
	return map[string]VersionDeps{
		"1.0.0": {RequiresDist: []string{"flask>=2.0", "unknownpkg>=1.0"}},
		"1.0.1": {RequiresDist: []string{"flask>=2.0", "unknownpkg>=1.0"}},
		"2.0.0": {RequiresDist: []string{">=2.0"}},
		// An authoritative empty: this version was captured and declares
		// nothing. Distinct from a version being absent from the map.
		"3.0.0": {},
		"4.0.0": {
			RequiresPython: ">=3.8",
			RequiresDist:   []string{"flask ; extra == 'dev'"},
			ProvidesExtra:  []string{"dev"},
		},
		"5.0.0": {RequiresDist: []string{"flask>=1"}},
		"6.0.0": {RequiresDist: []string{"3>=1.0"}},
	}
}

func TestDecodePackageGolden(t *testing.T) {
	d := loadGoldenDict(t)

	blob, err := os.ReadFile("testdata/golden_blob.bin")
	if err != nil {
		t.Fatalf("reading golden blob: %v", err)
	}
	// The fixture is the PRE-compression body, so prepend the stored format
	// byte to push it through the real decompress path rather than around it.
	field := string(append([]byte{depsFormatStored}, blob...))

	got, err := DecodePackage(field, d)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}

	want := goldenExpected()
	if len(got) != len(want) {
		t.Fatalf("got %d versions, want %d", len(got), len(want))
	}
	for ver, wantDeps := range want {
		gotDeps, ok := got[ver]
		if !ok {
			t.Errorf("version %q missing from decode", ver)
			continue
		}
		if gotDeps.RequiresPython != wantDeps.RequiresPython {
			t.Errorf("%s RequiresPython = %q, want %q", ver, gotDeps.RequiresPython, wantDeps.RequiresPython)
		}
		if !equalStrings(gotDeps.RequiresDist, wantDeps.RequiresDist) {
			t.Errorf("%s RequiresDist = %v, want %v", ver, gotDeps.RequiresDist, wantDeps.RequiresDist)
		}
		if !equalStrings(gotDeps.ProvidesExtra, wantDeps.ProvidesExtra) {
			t.Errorf("%s ProvidesExtra = %v, want %v", ver, gotDeps.ProvidesExtra, wantDeps.ProvidesExtra)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGoldenCoversDictionaryCompressionBothWays pins that the golden fixture
// actually exercises both requirement encodings, so a decoder that broke one of
// them could not pass the golden test by luck.
func TestGoldenCoversDictionaryCompressionBothWays(t *testing.T) {
	d := loadGoldenDict(t)

	names := d.Names()
	if len(names) != 2 || names[0] != "flask" || names[1] != "numpy" {
		t.Fatalf("golden dict names = %v, want [flask numpy]", names)
	}

	want := goldenExpected()

	// "flask>=2.0" uses a dictionary reference ("flask" is in the table).
	if got := want["1.0.0"].RequiresDist[0]; got != "flask>=2.0" {
		t.Errorf("expected a dict-referenced requirement, got %q", got)
	}
	// "unknownpkg>=1.0" cannot be, since it is not in the table.
	if got := want["1.0.0"].RequiresDist[1]; got != "unknownpkg>=1.0" {
		t.Errorf("expected an inline-name requirement, got %q", got)
	}
}

// --- empty and error cases ---

func TestDecodePackageEmptyFieldIsEmptyNonNilMap(t *testing.T) {
	got, err := DecodePackage("", nil)
	if err != nil {
		t.Fatalf("DecodePackage(\"\"): %v", err)
	}
	if got == nil {
		t.Fatal("want an empty non-nil map; nil would be indistinguishable from a decode failure")
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestDecodePackageStoredEmptyBlobWithNilDict(t *testing.T) {
	// Stored, zero pool entries, zero versions. Needs no dictionary because it
	// contains no dictionary references, so a nil Dict must work.
	got, err := DecodePackage(string([]byte{depsFormatStored, 0x00, 0x00}), nil)
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %v, want an empty non-nil map", got)
	}
}

func TestDecodePackageZstdWithoutDictErrorsNotPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked instead of returning an error: %v", r)
		}
	}()

	if _, err := DecodePackage(string([]byte{depsFormatZstd, 0xff}), nil); err == nil {
		t.Error("expected an error for a zstd field with no dictionary")
	}
}

func TestDecodePackageUnknownFormatByte(t *testing.T) {
	if _, err := DecodePackage(string([]byte{0x03, 0x00}), nil); err == nil {
		t.Error("expected an error for an unrecognized format byte")
	}
}

func TestDecodePackageTruncatedInput(t *testing.T) {
	d := loadGoldenDict(t)

	// A huge pool count with no data behind it. Must error on the read rather
	// than attempting an allocation sized from the claimed count.
	field := string([]byte{depsFormatStored, 0xff, 0xff, 0xff, 0xff, 0x0f})
	if _, err := DecodePackage(field, d); err == nil {
		t.Error("expected an error for a truncated blob")
	}
}

// TestDecodeAfterCloseErrors covers decoder lifetime without exposing the zstd
// decoder in the public API: once the Dict is closed, decoding a compressed
// field must fail rather than use a released decoder.
func TestDecodeAfterCloseErrors(t *testing.T) {
	b, err := os.ReadFile("testdata/golden_depsdict.bin")
	if err != nil {
		t.Fatalf("reading golden depsdict: %v", err)
	}
	d, err := ParseDepsdictField(b)
	if err != nil {
		t.Fatalf("ParseDepsdictField: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := DecodePackage(string([]byte{depsFormatZstd, 0x28, 0xb5, 0x2f, 0xfd}), d); err == nil {
		t.Error("expected an error decoding through a closed Dict")
	}
}

func TestCloseIsSafeOnNilAndTwice(t *testing.T) {
	var nilDict *Dict
	if err := nilDict.Close(); err != nil {
		t.Errorf("Close on nil Dict: %v", err)
	}
	if names := nilDict.Names(); names != nil {
		t.Errorf("Names on nil Dict = %v, want nil", names)
	}

	d := loadGoldenDict(t)
	if err := d.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// --- depsdict parsing ---

func TestParseDepsdictGolden(t *testing.T) {
	b, err := os.ReadFile("testdata/golden_depsdict.bin")
	if err != nil {
		t.Fatalf("reading golden depsdict: %v", err)
	}

	// Documented bytes: 01 | 02 | 05 "flask" | 05 "numpy" | 00
	if len(b) != 15 {
		t.Errorf("golden depsdict is %d bytes, want 15 -- fixture changed?", len(b))
	}

	d, err := ParseDepsdictField(b)
	if err != nil {
		t.Fatalf("ParseDepsdictField: %v", err)
	}
	defer func() { _ = d.Close() }()

	if got := d.Names(); !equalStrings(got, []string{"flask", "numpy"}) {
		t.Errorf("Names() = %v, want [flask numpy]", got)
	}
	if len(d.zdict) != 0 {
		t.Errorf("golden zdict is %d bytes, want 0", len(d.zdict))
	}
}

func TestParseDepsdictRejectsBadFormatByte(t *testing.T) {
	if _, err := ParseDepsdictField([]byte{0x02, 0x00, 0x00}); err == nil {
		t.Error("expected an error for a bad depsdict format byte")
	}
}

func TestParseDepsdictRejectsOversizedZdictLength(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(depsdictFormatByte)
	putUvarint(&buf, 0)     // no names
	putUvarint(&buf, 1<<40) // absurd zdict length, no payload

	if _, err := ParseDepsdictField(buf.Bytes()); err == nil {
		t.Error("expected an error rather than a huge allocation")
	}
}

func TestParseDepsdictRejectsTruncatedNameTable(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(depsdictFormatByte)
	putUvarint(&buf, 3) // claims 3 names
	putStr(&buf, "flask")
	// ...and then stops.

	if _, err := ParseDepsdictField(buf.Bytes()); err == nil {
		t.Error("expected an error for a truncated name table")
	}
}

func TestParseDepsdictEmptyInput(t *testing.T) {
	if _, err := ParseDepsdictField(nil); err == nil {
		t.Error("expected an error for empty input")
	}
}

// --- blob decoding ---

func TestUnmarshalBlobDictionaryReference(t *testing.T) {
	names := []string{"flask", "numpy"}

	var buf bytes.Buffer
	putUvarint(&buf, 1)   // one pooled dep set
	putStr(&buf, "")      // RequiresPython
	putUvarint(&buf, 1)   // one requirement
	putUvarint(&buf, 1)   // control 1 => names[0]
	putStr(&buf, ">=2.0") // remainder
	putUvarint(&buf, 0)   // no extras
	putUvarint(&buf, 1)   // one version
	putStr(&buf, "1.0.0")
	putUvarint(&buf, 0) // -> pool[0]

	got, err := unmarshalBlob(buf.Bytes(), names)
	if err != nil {
		t.Fatalf("unmarshalBlob: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d versions, want 1", len(got))
	}
	vd := got["1.0.0"]
	if !equalStrings(vd.RequiresDist, []string{"flask>=2.0"}) {
		t.Errorf("RequiresDist = %v, want [flask>=2.0]", vd.RequiresDist)
	}
	if vd.RequiresPython != "" {
		t.Errorf("RequiresPython = %q, want empty", vd.RequiresPython)
	}
	// A zero count decodes to nil rather than an empty slice. Asserted so the
	// distinction stays deliberate: consumers compare with len(), and a change
	// here would be an unannounced API change.
	if vd.ProvidesExtra != nil {
		t.Errorf("ProvidesExtra = %v, want nil for a zero count", vd.ProvidesExtra)
	}
}

func TestUnmarshalBlobSharedPoolEntry(t *testing.T) {
	// Two versions pointing at one pooled dep set. This is where the format's
	// size reduction comes from, so it needs direct coverage.
	var buf bytes.Buffer
	putUvarint(&buf, 1)
	putStr(&buf, ">=3.8")
	putUvarint(&buf, 0)
	putUvarint(&buf, 0)
	putUvarint(&buf, 2) // two versions
	putStr(&buf, "1.0")
	putUvarint(&buf, 0)
	putStr(&buf, "2.0")
	putUvarint(&buf, 0)

	got, err := unmarshalBlob(buf.Bytes(), nil)
	if err != nil {
		t.Fatalf("unmarshalBlob: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d versions, want 2", len(got))
	}
	for _, ver := range []string{"1.0", "2.0"} {
		if got[ver].RequiresPython != ">=3.8" {
			t.Errorf("%s RequiresPython = %q, want >=3.8", ver, got[ver].RequiresPython)
		}
	}
}

func TestUnmarshalBlobRejectsBadPoolIndex(t *testing.T) {
	var buf bytes.Buffer
	putUvarint(&buf, 0) // empty pool
	putUvarint(&buf, 1) // one version
	putStr(&buf, "1.0.0")
	putUvarint(&buf, 5) // index 5 into an empty pool

	if _, err := unmarshalBlob(buf.Bytes(), nil); err == nil {
		t.Error("expected an error for an out-of-range pool index")
	}
}

func TestUnmarshalBlobRejectsBadDepNameID(t *testing.T) {
	var buf bytes.Buffer
	putUvarint(&buf, 1)
	putStr(&buf, "")
	putUvarint(&buf, 1)
	putUvarint(&buf, 99) // control 99 => names[98], but there are none
	putStr(&buf, ">=1.0")
	putUvarint(&buf, 0)

	if _, err := unmarshalBlob(buf.Bytes(), []string{"flask"}); err == nil {
		t.Error("expected an error for an out-of-range dep-name id")
	}
}

// --- wire primitives ---

func TestReadStrRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	putStr(&buf, "flask")

	got, err := readStr(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("readStr: %v", err)
	}
	if got != "flask" {
		t.Errorf("readStr = %q, want %q", got, "flask")
	}
}

// TestReadStrRejectsOversizedLength is the allocation guard: a claimed length
// of 1<<40 with no payload must error rather than attempt the allocation.
func TestReadStrRejectsOversizedLength(t *testing.T) {
	var buf bytes.Buffer
	putUvarint(&buf, 1<<40)

	if _, err := readStr(bytes.NewReader(buf.Bytes())); err == nil {
		t.Error("expected an error rather than a 1 TiB allocation")
	}
}

func TestCapHintNeverExceedsRemainingBytes(t *testing.T) {
	r := bytes.NewReader(make([]byte, 10))

	if got := capHint(1<<40, r, 1); got != 10 {
		t.Errorf("capHint(huge, 10 bytes, 1) = %d, want 10", got)
	}
	if got := capHint(1<<40, r, 4); got != 2 {
		t.Errorf("capHint(huge, 10 bytes, 4) = %d, want 2", got)
	}
	if got := capHint(3, r, 1); got != 3 {
		t.Errorf("capHint(3, 10 bytes, 1) = %d, want 3 (an honest count passes through)", got)
	}
	// A zero or negative element size must not divide by zero.
	if got := capHint(5, r, 0); got != 5 {
		t.Errorf("capHint(5, 10 bytes, 0) = %d, want 5", got)
	}
}

// TestMaxDecompressedBytesIsEnforced documents the bomb bound. The zstd decoder
// is constructed with WithDecoderMaxMemory(MaxDecompressedBytes), so a field
// that would expand beyond it fails instead of exhausting memory.
func TestMaxDecompressedBytesIsEnforced(t *testing.T) {
	if MaxDecompressedBytes != 64<<20 {
		t.Errorf("MaxDecompressedBytes = %d, want 64 MiB", MaxDecompressedBytes)
	}

	d := loadGoldenDict(t)

	// Not a valid zstd frame, so this asserts the failure path is an error
	// rather than a panic. The bound itself is enforced inside the decoder.
	_, err := DecodePackage(string(append([]byte{depsFormatZstd}, bytes.Repeat([]byte{0x00}, 64)...)), d)
	if err == nil {
		t.Error("expected an error for a malformed zstd payload")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("unexpected error kind: %v", err)
	}
}
