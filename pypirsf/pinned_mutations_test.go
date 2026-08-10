// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rsf "github.com/rstudio/repository-snapshot-format"
)

// The tests here pin decoder behaviour on input the producer would never write.
// They exist because this reader is pointed at a ~1 GB file it did not create:
// a truncated download, a partially-written snapshot or a future format revision
// all arrive as bytes that do not mean what the header says they do, and the
// difference between "returns an error" and "panics" or "returns garbage" is the
// difference between a diagnosable failure and a wrong answer.
//
// Each was verified by applying the mutation it names, watching the test fail,
// and reverting.

// blobWithRequirementControl builds a one-version blob whose single requirement
// carries an explicit dictionary control value.
//
// A control of n means "the (n-1)th name in the shared table"; the caller passes
// a value past the end of the table to exercise the bounds check.
func blobWithRequirementControl(control uint64) string {
	var body bytes.Buffer

	putUvarint(&body, 1) // one dependency set in the pool
	putStr(&body, "")    // no Requires-Python
	putUvarint(&body, 1) // one requirement
	putUvarint(&body, control)
	putStr(&body, ">=1.0") // the remainder that follows a dictionary reference
	putUvarint(&body, 0)   // no extras

	putUvarint(&body, 1) // one version
	putStr(&body, "1.0.0")
	putUvarint(&body, 0) // pointing at pool entry 0

	return string(append([]byte{depsFormatStored}, body.Bytes()...))
}

// TestDependencyNameIndexPastTheTableIsRejected pins the name-table bounds check.
//
// An index exactly equal to len(names) is the case that matters: it is what an
// off-by-one in either the producer or this check produces, and with the check
// loosened to `>` it indexes one past the end and panics inside a library call
// rather than returning an error the caller can classify.
//
// Mutation pinned: `idx >= uint64(len(names))` -> `idx > uint64(len(names))` in
// decodeRequirement.
func TestDependencyNameIndexPastTheTableIsRejected(t *testing.T) {
	d := loadGoldenDict(t)
	names := d.Names()
	if len(names) == 0 {
		t.Fatal("the golden dictionary has no names, so there is no boundary to test")
	}

	// control is 1-based, so len(names)+1 addresses index len(names): the first
	// slot past the end.
	_, err := DecodePackage(blobWithRequirementControl(uint64(len(names))+1), d)
	if err == nil {
		t.Fatal("a dependency-name id one past the end of the table decoded without error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error does not name the cause: %v", err)
	}

	// The last valid index must still decode, or the check is simply too strict
	// and the test above proves nothing.
	if _, err := DecodePackage(blobWithRequirementControl(uint64(len(names))), d); err != nil {
		t.Errorf("the last valid dependency-name id was rejected: %v", err)
	}
}

// TestPoolIndexPastTheEndIsRejected pins the same boundary for the
// version-to-pool mapping.
//
// Mutation pinned: `idx >= uint64(len(pool))` -> `idx > uint64(len(pool))` in
// unmarshalBlob.
func TestPoolIndexPastTheEndIsRejected(t *testing.T) {
	build := func(poolIdx uint64) string {
		var body bytes.Buffer
		putUvarint(&body, 1) // one dependency set in the pool
		putStr(&body, "")
		putUvarint(&body, 0) // no requirements
		putUvarint(&body, 0) // no extras
		putUvarint(&body, 1) // one version
		putStr(&body, "1.0.0")
		putUvarint(&body, poolIdx)
		return string(append([]byte{depsFormatStored}, body.Bytes()...))
	}

	_, err := DecodePackage(build(1), nil) // pool has exactly one entry, index 0
	if err == nil {
		t.Fatal("a pool index one past the end decoded without error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("error does not name the cause: %v", err)
	}

	if _, err := DecodePackage(build(0), nil); err != nil {
		t.Errorf("the only valid pool index was rejected: %v", err)
	}
}

// TestRecordlessFileIsReportedAsCarryingNoData pins the check that a file whose
// scan produced no dictionary is refused at Open.
//
// A file carrying a valid schema and zero records is what a download truncated
// early looks like, and it is indistinguishable at every later call from a file
// that simply does not have the package you asked for: every lookup answers
// ErrPackageNotFound, which sends the caller looking for the package rather than
// at the file. The other path to ErrNoDependencyData -- a schema with no deps
// fields at all -- is already covered; this is the one where the schema is fine
// and the records are gone.
//
// Mutation pinned: dropping the `file.dict == nil` check at the end of scan.
func TestRecordlessFileIsReportedAsCarryingNoData(t *testing.T) {
	path := writeFixtureRSF(t, fixtureRecords(t))
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	// Truncate at the first record rather than at a hardcoded offset, so this
	// keeps working when the schema changes size.
	src, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening fixture: %v", err)
	}
	r := rsf.NewReader()
	if _, err := r.ReadIndex(bufio.NewReader(src)); err != nil {
		_ = src.Close()
		t.Fatalf("reading schema: %v", err)
	}
	headerLen := int(r.Pos())
	if err := src.Close(); err != nil {
		t.Fatalf("closing fixture: %v", err)
	}
	if headerLen <= 0 || headerLen >= len(full) {
		t.Fatalf("schema length %d is not inside a %d-byte file", headerLen, len(full))
	}

	schemaOnly := filepath.Join(t.TempDir(), "schema-only.rsf")
	if err := os.WriteFile(schemaOnly, full[:headerLen], 0o644); err != nil {
		t.Fatalf("writing truncated fixture: %v", err)
	}

	file, err := Open(schemaOnly)
	if err == nil {
		t.Fatalf("a schema-only file opened successfully with %d records", file.Len())
	}
	if !errors.Is(err, ErrNoDependencyData) {
		t.Errorf("error = %v, want ErrNoDependencyData", err)
	}
}

// TestUnknownFormatByteIsRejected pins that an unrecognized leading format byte
// fails rather than being read as a stored blob.
//
// The byte says how the rest is encoded, so guessing "stored" for a value this
// reader does not know decodes the body of some FUTURE format as though it were
// the current one. That does not reliably fail: uvarint-prefixed data mostly
// parses into something, so the likely outcome is a plausible-looking dependency
// map assembled out of the wrong bytes.
//
// Mutation pinned: returning the payload instead of an error in decompress's
// default branch.
func TestUnknownFormatByteIsRejected(t *testing.T) {
	stored := blobWithRequirementControl(0)
	// Same body, unknown format byte.
	future := "\x7f" + stored[1:]

	if _, err := DecodePackage(stored, nil); err != nil {
		t.Fatalf("the stored control blob must decode for this test to be about the format byte: %v", err)
	}

	got, err := DecodePackage(future, nil)
	if err == nil {
		t.Fatalf("format byte 0x7f decoded as stored, yielding %v", got)
	}
	if !strings.Contains(err.Error(), "unknown deps format byte") {
		t.Errorf("error does not name the cause: %v", err)
	}
}

// TestZstdFieldWithoutADictionaryIsRejected pins that a compressed field with no
// decoder fails loudly.
//
// This is reachable in production: the dictionary lives on the file's FIRST
// record, so a file whose depsdict field is empty while later records are
// zstd-compressed leaves every lookup without a decoder. Returning the
// compressed payload as though it were the blob body hands the caller a
// frame-header-prefixed byte string to parse as dependencies.
//
// Mutation pinned: returning the payload instead of an error when the dictionary
// or decoder is nil.
func TestZstdFieldWithoutADictionaryIsRejected(t *testing.T) {
	// Body content is irrelevant: the refusal must happen before any of it is
	// interpreted.
	field := string([]byte{depsFormatZstd}) + "\x28\xb5\x2f\xfd not really compressed"

	for name, d := range map[string]*Dict{"nil dictionary": nil, "empty dictionary": {}} {
		t.Run(name, func(t *testing.T) {
			got, err := DecodePackage(field, d)
			if err == nil {
				t.Fatalf("a zstd field decoded with no decoder, yielding %v", got)
			}
			if !strings.Contains(err.Error(), "no dictionary/decoder") {
				t.Errorf("error does not name the cause: %v", err)
			}
		})
	}
}

// --- a mutation in this package that is left unpinned, and why ---
//
// scan's "record overran its declared size" check. Reaching it needs a file whose
// record size FIELD disagrees with the record's contents, which the real writer
// cannot produce -- it computes that field -- so the fixture would have to be
// hand-patched bytes rather than producer output, and a byte offset patched by
// hand is a test that breaks on the next schema change for reasons unrelated to
// what it asserts. The check is cheap and its absence is a desynchronized read
// rather than a wrong answer, so it stays unpinned deliberately.
