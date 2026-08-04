// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	rsf "github.com/rstudio/repository-snapshot-format"
)

// Buffer sizes for the two read paths. Discarded bytes still stream through the
// buffer, so the scan buffer is generous; per-record reads touch far less.
const (
	scanBufferSize   = 256 << 10
	recordBufferSize = 64 << 10
)

// ErrNoDependencyData means the file's schema has no deps fields at all, so it
// predates dependency data being carried in the RSF. Reported at Open, because
// the alternative is every lookup failing for a reason the caller cannot act on.
var ErrNoDependencyData = errors.New("pypirsf: this RSF carries no dependency data")

// ErrPackageNotFound means the file has no record for that canonical name.
var ErrPackageNotFound = errors.New("pypirsf: package not found")

// File is a read-only view over a PyPI RSF file.
//
// Open scans the file once to build a name-to-offset table and to load the
// global dictionary; after that, Deps seeks straight to a package.
//
// # Concurrency
//
// A File is safe for concurrent use. Each lookup reads through its own
// io.SectionReader over the underlying file and its own rsf.Reader, sharing only
// the immutable schema — so no lookup mutates state another can observe. This is
// deliberate: the alternative, seeking one shared reader, requires resetting the
// buffered reader in lockstep with every seek, and getting that wrong reads
// stale bytes from the previous position rather than failing.
//
// # What this reader deliberately does not read
//
// It never traverses the per-package snapshots array. Everything hazardous about
// this format lives in there: subfield order has three historical layouts that
// must be detected from the file's own schema, and the per-element index block's
// width cannot be trusted from the schema because production files are written
// with the v1 index format and report the array as un-indexed even though the
// block is physically present. Skipping the array wholesale, which the format
// supports safely, avoids all of it.
//
// The consequence is that a File reports the versions for which dependencies
// were captured, rather than every version that ever existed. For a resolver
// that is the correct set anyway: a version whose dependencies are unknown
// cannot be resolved through.
type File struct {
	f    *os.File
	size int64

	schema rsf.Index
	dict   *Dict

	// offsets maps canonical name to the byte offset of that package's record.
	offsets map[string]int64
}

// Open reads path and indexes it.
//
// The single scan is what makes later lookups cheap. It reads each record's
// canonical name and skips the rest, so the cost is proportional to record
// count rather than to the file's size.
//
// The caller owns the result and must Close it.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	file := &File{
		f:       f,
		size:    info.Size(),
		offsets: make(map[string]int64),
	}

	if err := file.scan(); err != nil {
		_ = f.Close()
		return nil, err
	}

	return file, nil
}

// scan reads the schema, indexes every record by canonical name, and loads the
// global dictionary from the first record.
func (file *File) scan() error {
	buf := bufio.NewReaderSize(file.f, scanBufferSize)

	r := rsf.NewReader()
	schema, err := r.ReadIndex(buf)
	if err != nil {
		return fmt.Errorf("pypirsf: reading schema: %w", err)
	}
	file.schema = schema

	first := true
	for {
		recordStart := r.Pos()

		// The size value INCLUDES the size field itself, so the record ends at
		// recordStart + recordSize.
		recordSize, err := r.ReadSizeField(buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("pypirsf: reading record size at %d: %w", recordStart, err)
		}
		recordEnd := recordStart + recordSize

		if err := r.AdvanceTo(buf, "cname"); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("pypirsf: advancing to cname at %d: %w", recordStart, err)
		}
		cname, err := r.ReadStringField(buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("pypirsf: reading cname at %d: %w", recordStart, err)
		}
		file.offsets[cname] = int64(recordStart)

		// The global dictionary lives on the first record only. Read it here
		// rather than seeking back for it later.
		if first {
			first = false
			if err := file.loadDictLocked(r, buf); err != nil {
				return err
			}
		}

		// Skip whatever is left of this record. Passing rsf.Top resets the
		// reader's field position so the next iteration starts cleanly at the
		// following record's first field.
		remaining := recordEnd - r.Pos()
		if remaining < 0 {
			return fmt.Errorf("pypirsf: record at %d overran its declared size by %d bytes",
				recordStart, -remaining)
		}
		if err := r.Discard(remaining, buf, rsf.Top); err != nil {
			return fmt.Errorf("pypirsf: skipping to next record after %d: %w", recordStart, err)
		}
	}

	if file.dict == nil {
		return ErrNoDependencyData
	}
	return nil
}

// loadDictLocked reads the first record's depsdict, with the reader already
// positioned inside that record having just read cname.
func (file *File) loadDictLocked(r rsf.Reader, buf *bufio.Reader) error {
	if err := r.AdvanceTo(buf, "depsdict"); err != nil {
		if errors.Is(err, rsf.ErrNoSuchField) {
			// Pre-cutover file: no dependency fields in the schema at all.
			return ErrNoDependencyData
		}
		return fmt.Errorf("pypirsf: advancing to depsdict: %w", err)
	}

	raw, err := r.ReadStringField(buf)
	if err != nil {
		return fmt.Errorf("pypirsf: reading depsdict: %w", err)
	}
	if raw == "" {
		// The field exists but is empty. A stored (uncompressed) blob still
		// decodes without a dictionary, so this is not fatal on its own — but a
		// zstd blob will fail, and that failure is clearer at the call site
		// than a nil-dictionary panic here.
		file.dict = &Dict{}
		return nil
	}

	dict, err := ParseDepsdictField([]byte(raw))
	if err != nil {
		return fmt.Errorf("pypirsf: parsing depsdict: %w", err)
	}
	file.dict = dict

	return nil
}

// Close releases the file and the shared decoder.
func (file *File) Close() error {
	var dictErr error
	if file.dict != nil {
		dictErr = file.dict.Close()
	}
	fileErr := file.f.Close()

	if fileErr != nil {
		return fileErr
	}
	return dictErr
}

// Dict returns the global dependency dictionary read from the first record.
func (file *File) Dict() *Dict { return file.dict }

// Len reports how many package records the file contains.
func (file *File) Len() int { return len(file.offsets) }

// Packages returns every canonical name in the file, sorted.
//
// Sorted because the underlying map iteration order is random, and a tool that
// lists packages should not reorder its output between runs.
func (file *File) Packages() []string {
	out := make([]string, 0, len(file.offsets))
	for name := range file.offsets {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Has reports whether the file carries a record for cname.
func (file *File) Has(cname string) bool {
	_, ok := file.offsets[cname]
	return ok
}

// Deps returns the dependency metadata for every captured version of cname.
//
// cname must already be PEP 503-normalized; this package does not normalize, so
// that it depends on nothing but a zstd implementation. Callers holding a
// user-supplied name should normalize first.
//
// Returns ErrPackageNotFound if the file has no such record. A package present
// with no captured dependencies yields an empty non-nil map, which is a
// different answer and callers should treat it as one.
func (file *File) Deps(cname string) (map[string]VersionDeps, error) {
	offset, ok := file.offsets[cname]
	if !ok {
		return nil, fmt.Errorf("pypirsf: %q: %w", cname, ErrPackageNotFound)
	}

	// A fresh section reader and rsf.Reader per lookup, sharing only the
	// immutable schema. Nothing here mutates File, so lookups are concurrent.
	section := io.NewSectionReader(file.f, offset, file.size-offset)
	buf := bufio.NewReaderSize(section, recordBufferSize)

	r := rsf.NewReader()
	r.SetIndex(file.schema)

	if _, err := r.ReadSizeField(buf); err != nil {
		return nil, fmt.Errorf("pypirsf: %q: reading record size: %w", cname, err)
	}

	// Skips cname, pname, and the whole snapshots array. The array is skipped
	// wholesale by its own size header, which is why none of the array's
	// layout hazards apply here.
	if err := r.AdvanceTo(buf, "deps"); err != nil {
		if errors.Is(err, rsf.ErrNoSuchField) {
			return nil, ErrNoDependencyData
		}
		return nil, fmt.Errorf("pypirsf: %q: advancing to deps: %w", cname, err)
	}

	field, err := r.ReadStringField(buf)
	if err != nil {
		return nil, fmt.Errorf("pypirsf: %q: reading deps: %w", cname, err)
	}

	deps, err := DecodePackage(field, file.dict)
	if err != nil {
		return nil, fmt.Errorf("pypirsf: %q: %w", cname, err)
	}
	return deps, nil
}
