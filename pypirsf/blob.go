// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"fmt"
)

// decodeRequirement reads one requirement string.
//
// The encoding dictionary-compresses the leading package name: a nonzero
// control value is a 1-based index into the shared name table, and the
// remainder of the requirement follows as a string. A zero control means the
// name was not in the table and is stored inline.
func decodeRequirement(r *bytes.Reader, names []string) (string, error) {
	control, err := readUvarint(r)
	if err != nil {
		return "", err
	}

	if control >= 1 {
		idx := control - 1
		if idx >= uint64(len(names)) {
			return "", fmt.Errorf("pypirsf: dep-name id %d out of range (%d names)", idx, len(names))
		}
		remainder, err := readStr(r)
		if err != nil {
			return "", err
		}
		return names[idx] + remainder, nil
	}

	name, err := readStr(r)
	if err != nil {
		return "", err
	}
	remainder, err := readStr(r)
	if err != nil {
		return "", err
	}
	return name + remainder, nil
}

// decodeDepSet reads one dependency set: an interpreter constraint, a list of
// requirements, and a list of extras.
func decodeDepSet(r *bytes.Reader, names []string) (VersionDeps, error) {
	var vd VersionDeps

	rp, err := readStr(r)
	if err != nil {
		return vd, err
	}
	vd.RequiresPython = rp

	reqCount, err := readUvarint(r)
	if err != nil {
		return vd, err
	}
	if reqCount > 0 {
		reqs := make([]string, 0, capHint(reqCount, r, 2))
		for i := uint64(0); i < reqCount; i++ {
			s, err := decodeRequirement(r, names)
			if err != nil {
				return vd, err
			}
			reqs = append(reqs, s)
		}
		vd.RequiresDist = reqs
	}

	extraCount, err := readUvarint(r)
	if err != nil {
		return vd, err
	}
	if extraCount > 0 {
		extras := make([]string, 0, capHint(extraCount, r, 1))
		for i := uint64(0); i < extraCount; i++ {
			s, err := readStr(r)
			if err != nil {
				return vd, err
			}
			extras = append(extras, s)
		}
		vd.ProvidesExtra = extras
	}

	return vd, nil
}

// unmarshalBlob decodes an already-decompressed blob body.
//
// The body is a pool of distinct dependency sets followed by a version-to-pool
// mapping. Many versions of a package usually share one dependency set, so
// storing each set once and pointing at it is where most of the size reduction
// comes from.
func unmarshalBlob(b []byte, names []string) (map[string]VersionDeps, error) {
	r := bytes.NewReader(b)

	poolCount, err := readUvarint(r)
	if err != nil {
		return nil, err
	}
	pool := make([]VersionDeps, 0, capHint(poolCount, r, 3))
	for i := uint64(0); i < poolCount; i++ {
		ds, err := decodeDepSet(r, names)
		if err != nil {
			return nil, err
		}
		pool = append(pool, ds)
	}

	versionCount, err := readUvarint(r)
	if err != nil {
		return nil, err
	}
	out := make(map[string]VersionDeps, capHint(versionCount, r, 2))
	for i := uint64(0); i < versionCount; i++ {
		ver, err := readStr(r)
		if err != nil {
			return nil, err
		}
		idx, err := readUvarint(r)
		if err != nil {
			return nil, err
		}
		if idx >= uint64(len(pool)) {
			return nil, fmt.Errorf("pypirsf: pool index %d out of range (%d entries)", idx, len(pool))
		}
		out[ver] = pool[idx]
	}

	return out, nil
}
