// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// decodeYankSection reads the optional yank section that trails the tag
// section: a uvarint count followed by that many delta-encoded, strictly
// ascending indexes into the blob's version list.
//
// A truncated section, an index at or past versionCount, or a non-ascending
// delta all error rather than decode partially — a yank list a reader cannot
// trust in full must not be applied at all.
func decodeYankSection(r *bytes.Reader, versionCount int) ([]int, error) {
	n, err := readUvarint(r)
	if err != nil {
		return nil, fmt.Errorf("pypirsf: reading yank count: %w", err)
	}
	if n == 0 {
		return nil, nil
	}

	idxs := make([]int, 0, capHint(n, r, 1))
	prev := -1
	for i := uint64(0); i < n; i++ {
		d, err := readUvarint(r)
		if err != nil {
			return nil, fmt.Errorf("pypirsf: reading yank delta %d: %w", i, err)
		}
		idx := prev + int(d) + 1
		if idx <= prev || idx >= versionCount {
			return nil, fmt.Errorf("pypirsf: yank index %d out of range or non-ascending (prev %d, %d versions)", idx, prev, versionCount)
		}
		idxs = append(idxs, idx)
		prev = idx
	}
	return idxs, nil
}

// hasTrailingYankSection reports whether the blob has bytes remaining after
// its tag section — the marker (per the byte-layout contract) for "this
// file's record 0 carries complete yank data".
//
// This is only meaningful when checked against record 0's own blob; see
// File.YanksCaptured.
func hasTrailingYankSection(b []byte, names []string, td *TagDict) (bool, error) {
	r, pool, _, err := decodeBlobBody(b, names)
	if err != nil {
		return false, err
	}
	if err := decodeTagSection(r, pool, td); err != nil {
		return false, err
	}
	return r.Len() > 0, nil
}

// EnsureYankSection returns a deps field equivalent to field, guaranteed to
// have a yank section (count 0 if it had none) on its blob.
//
// If a yank section is already present, field is returned unchanged. This is
// the marker-writing helper: put record 0's deps through it to make the file
// report YanksCaptured true.
//
// The returned field is always re-emitted stored (uncompressed) rather than
// recompressed — this package has no zstd encoder, and a caller that wants the
// field recompressed already has a zstd implementation to do it with.
func EnsureYankSection(field string, d *Dict, td *TagDict) (string, error) {
	if field == "" {
		return field, nil
	}

	blob, err := decompress(field, d)
	if err != nil {
		return "", err
	}
	if blob == nil {
		return field, nil
	}

	r, pool, _, err := decodeBlobBody(blob, d.Names())
	if err != nil {
		return "", err
	}
	hadTagSection := r.Len() > 0
	if err := decodeTagSection(r, pool, td); err != nil {
		return "", err
	}
	if r.Len() > 0 {
		// Already has a yank section; nothing to do.
		return field, nil
	}

	nb := append([]byte(nil), blob...)
	if !hadTagSection {
		for range pool {
			nb = append(nb, tagsUncaptured)
		}
	}
	nb = binary.AppendUvarint(nb, 0)

	return string(append([]byte{depsFormatStored}, nb...)), nil
}

// MinimalYankMarkerField returns the smallest deps field that carries the
// yank marker: no pool entries, no versions, and a yank count of 0. Use it to
// put the marker on a record 0 that has no deps data at all.
func MinimalYankMarkerField() string {
	var b []byte
	b = binary.AppendUvarint(b, 0) // pool count
	b = binary.AppendUvarint(b, 0) // version count
	b = binary.AppendUvarint(b, 0) // yank count
	return string(append([]byte{depsFormatStored}, b...))
}
