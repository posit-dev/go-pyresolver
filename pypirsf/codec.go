// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"errors"
	"fmt"
)

const (
	depsFormatZstd   = 0x01
	depsFormatStored = 0x02
)

// DecodePackage decodes one package's deps field into a version-to-dependencies
// map.
//
// Pass the Dict obtained from the file's first record via ParseDepsdictField. A
// nil Dict is accepted and works for stored (uncompressed) fields, but a
// zstd-compressed field without one is an error rather than a silent empty
// result.
//
// # The empty cases are distinct and both meaningful
//
// An empty field and a stored-but-empty blob both decode to an empty non-nil
// map: the package is present and nothing was captured for any version.
//
// Within the returned map, a version that IS a key with a zero VersionDeps
// declares no dependencies — authoritatively. A version that is ABSENT means no
// data was captured for it. A resolver must not conflate these: the first
// permits resolution to proceed with no edges, the second means the answer is
// unknown and choosing that version silently drops its subtree.
func DecodePackage(field string, d *Dict) (map[string]VersionDeps, error) {
	if field == "" {
		return map[string]VersionDeps{}, nil
	}

	blob, err := decompress(field, d)
	if err != nil {
		return nil, err
	}
	if blob == nil {
		return map[string]VersionDeps{}, nil
	}

	return unmarshalBlob(blob, d.Names())
}

// decompress strips the leading format byte and returns the blob body.
func decompress(field string, d *Dict) ([]byte, error) {
	// Callers reach this only with a non-empty field; guard anyway so a future
	// caller cannot turn a mistake into a panic.
	if field == "" {
		return nil, errors.New("pypirsf: empty deps field")
	}

	fb := field[0]
	payload := []byte(field[1:])

	switch fb {
	case depsFormatStored:
		return payload, nil
	case depsFormatZstd:
		if d == nil || d.dec == nil {
			return nil, errors.New("pypirsf: zstd deps field but no dictionary/decoder")
		}
		return d.dec.DecodeAll(payload, nil)
	default:
		return nil, fmt.Errorf("pypirsf: unknown deps format byte 0x%02x", fb)
	}
}
