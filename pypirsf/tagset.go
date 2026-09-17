// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"fmt"
)

// tagsUncaptured is the per-slot control value meaning "no tag claim": these
// versions' tag set was never derived, so an empty tag list here must NOT be
// read as "publishes no wheels".
const tagsUncaptured = 0

// decodeTagSet reads one slot's tag claim. The control uvarint packs the tag
// count and the sdist bit together so the common case costs one byte:
//
//	0                              -> uncaptured, no refs follow
//	(len(tags)+1)<<1 | hasSdistBit -> captured
//
// A ref is the 1-based vocabulary id; ref 0 escapes to a literal triple, for a
// tag the vocabulary does not carry yet. The escape is not optional -- the
// vocabulary legitimately lags a freshly published platform tag.
func decodeTagSet(r *bytes.Reader, td *TagDict) (tags []string, hasSdist, captured bool, err error) {
	control, err := readUvarint(r)
	if err != nil {
		return nil, false, false, fmt.Errorf("pypirsf: reading tag control varint: %w", err)
	}
	if control == tagsUncaptured {
		return nil, false, false, nil
	}

	hasSdist = control&1 == 1
	count := (control >> 1) - 1
	if count > 0 {
		// A ref is at least 1 byte, so a crafted huge count cannot outrun the
		// bytes present.
		tags = make([]string, 0, capHint(count, r, 1))
	}
	for i := uint64(0); i < count; i++ {
		ref, err := readUvarint(r)
		if err != nil {
			return nil, false, false, fmt.Errorf("pypirsf: reading tag ref %d: %w", i, err)
		}
		if ref == tagsUncaptured {
			literal, err := readStr(r)
			if err != nil {
				return nil, false, false, fmt.Errorf("pypirsf: reading literal tag %d: %w", i, err)
			}
			tags = append(tags, literal)
			continue
		}
		triple, ok := td.Tag(ref)
		if !ok {
			// The refs and the vocabulary are separately guarded RSF fields, so
			// they can drift. Resolving to nothing is that drift showing up, and
			// dropping the tag would turn it into a quietly narrower tag set.
			return nil, false, false, fmt.Errorf("pypirsf: tag ref %d out of range (%d triples)", ref, td.Len())
		}
		tags = append(tags, triple)
	}
	return tags, hasSdist, true, nil
}

// decodeTagSection reads the tag section that trails the version index, one
// claim per pool slot in slot order, and writes it onto the pool entries.
//
// Absence is the pre-cutover blob, not an error: a blob written before wheel tags
// existed simply ends after its version index, and every slot is then
// uncaptured. A section that starts and then runs out is corruption and errors.
//
// Bytes after the section are ignored on purpose. Placing tags here rather than
// inside each pool entry is what let this change ship without a deps
// format-byte bump, and that only keeps working if each reader stops at the
// sections it knows -- so do not add a trailing-byte check.
func decodeTagSection(r *bytes.Reader, pool []VersionDeps, td *TagDict) error {
	if r.Len() == 0 {
		return nil
	}
	for slot := range pool {
		tags, hasSdist, captured, err := decodeTagSet(r, td)
		if err != nil {
			return fmt.Errorf("pypirsf: tag section slot %d: %w", slot, err)
		}
		pool[slot].WheelTags = tags
		pool[slot].HasSdist = hasSdist
		pool[slot].TagsCaptured = captured
	}
	return nil
}
