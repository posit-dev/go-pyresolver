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
// The body is a pool of distinct slots, followed by a version-to-slot mapping,
// optionally followed by a tag section. Many versions of a package usually share
// one slot, so storing each once and pointing at it is where most of the size
// reduction comes from.
//
// # A slot is (dependency set, tag list), not a dependency set
//
// Two versions with identical dependencies but different wheels occupy two
// slots. This is the producer's identity rule, and the decoder only has to not
// assume otherwise -- but see the loop below for why the tag section forces this
// function's shape.
// versionSlot pairs a version string with its slot index in the pool.
type versionSlot struct {
	ver  string
	slot uint64
}

// decodeBlobBody reads the pool and the version-to-slot index shared by every
// deps blob, leaving r positioned at whatever optional sections (tags, yank)
// follow.
func decodeBlobBody(b []byte, names []string) (*bytes.Reader, []VersionDeps, []versionSlot, error) {
	r := bytes.NewReader(b)

	poolCount, err := readUvarint(r)
	if err != nil {
		return nil, nil, nil, err
	}
	pool := make([]VersionDeps, 0, capHint(poolCount, r, 3))
	for i := uint64(0); i < poolCount; i++ {
		ds, err := decodeDepSet(r, names)
		if err != nil {
			return nil, nil, nil, err
		}
		pool = append(pool, ds)
	}

	versionCount, err := readUvarint(r)
	if err != nil {
		return nil, nil, nil, err
	}
	pairs := make([]versionSlot, 0, capHint(versionCount, r, 2))
	for i := uint64(0); i < versionCount; i++ {
		ver, err := readStr(r)
		if err != nil {
			return nil, nil, nil, err
		}
		idx, err := readUvarint(r)
		if err != nil {
			return nil, nil, nil, err
		}
		if idx >= uint64(len(pool)) {
			return nil, nil, nil, fmt.Errorf("pypirsf: pool index %d out of range (%d entries)", idx, len(pool))
		}
		pairs = append(pairs, versionSlot{ver: ver, slot: idx})
	}

	return r, pool, pairs, nil
}

func unmarshalBlob(b []byte, names []string, td *TagDict) (map[string]VersionDeps, error) {
	r, pool, pairs, err := decodeBlobBody(b, names)
	if err != nil {
		return nil, err
	}

	// ⚠️ The version index CANNOT be flattened into the result map before tags
	// and yanks are applied: both arrive after the index and are written onto
	// the pool (tags) or looked up by version-list position (yank), so a copy
	// taken earlier would predate them.
	if err := decodeTagSection(r, pool, td); err != nil {
		return nil, err
	}

	out := make(map[string]VersionDeps, len(pairs))
	for _, p := range pairs {
		out[p.ver] = pool[p.slot]
	}

	if r.Len() > 0 {
		yanked, err := decodeYankSection(r, len(pairs))
		if err != nil {
			return nil, err
		}
		for _, idx := range yanked {
			vd := out[pairs[idx].ver]
			vd.Yanked = true
			out[pairs[idx].ver] = vd
		}
	}

	return out, nil
}
