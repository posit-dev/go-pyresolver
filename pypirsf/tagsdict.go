// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"fmt"
)

const tagsdictFormatByte = 0x01

// Completeness bytes for the tagsdict field. Complete means every version in
// every deps blob of this RSF carries a derived tag list, so an empty tag list
// is authoritative ("this version publishes no wheels"). Incomplete means a
// consumer must disable tag filtering for the WHOLE file, not per package: an
// empty tag list cannot be told apart from one that was never derived.
const (
	tagsIncomplete = 0x00
	tagsComplete   = 0x01
)

// TagDict is the wheel-tag vocabulary from RSF record 0: an ordered list of
// PEP 425 tag triples ("py3-none-any") where index i answers to the 1-based
// reference id i+1, plus the per-snapshot completeness flag.
//
// ⚠️ The vocabulary is APPEND-ONLY, and this decoder depends on the producer
// keeping it so. Tag references live in the deps blob body while the vocabulary
// lives in a separate RSF field with its own guard, so a renumbered vocabulary
// makes every carried-forward blob resolve its references to the WRONG triple,
// silently: the depsdict guard cannot see it, because depsdict did not change.
// The producer enforces this by requiring the prior field to be a byte PREFIX
// of the new one (rstudio/package-manager#20578).
//
// A nil *TagDict is the expected pre-cutover state, not an error: it resolves no
// ids and reports incomplete, so a consumer disables tag filtering rather than
// filtering on nothing.
type TagDict struct {
	triples  []string
	complete bool
}

// ParseTagsdictField parses record 0's tagsdict field. Format: 0x01 |
// completeness byte | length-prefixed triple repeated to the end of the field.
//
// The triples run to the end of the field with no count, which is what makes the
// producer's prefix-equality guard work: appending a triple appends bytes and
// rewrites nothing.
func ParseTagsdictField(field []byte) (*TagDict, error) {
	r := bytes.NewReader(field)
	fb, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if fb != tagsdictFormatByte {
		return nil, fmt.Errorf("pypirsf: bad tagsdict format byte 0x%02x", fb)
	}
	cb, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	// An unrecognized completeness byte is corruption, and corruption is a real
	// error here rather than "assume incomplete": silently degrading to
	// filtering-disabled would mask a producer bug as a benign capability gap.
	if cb != tagsComplete && cb != tagsIncomplete {
		return nil, fmt.Errorf("pypirsf: bad tagsdict completeness byte 0x%02x", cb)
	}

	// The smallest triple on the wire is 2 bytes: a length plus a byte of text.
	triples := make([]string, 0, capHint(uint64(r.Len()), r, 2))
	for r.Len() > 0 {
		s, err := readStr(r)
		if err != nil {
			return nil, err
		}
		// An empty triple would resolve a reference to "", a wrong answer with no
		// error, so reject the field instead. Duplicates are deliberately not
		// checked: this side only maps id -> triple, and a duplicate still
		// resolves correctly, so detecting it would cost a map for nothing.
		if s == "" {
			return nil, fmt.Errorf("pypirsf: tagsdict entry %d is empty", len(triples))
		}
		triples = append(triples, s)
	}
	return &TagDict{triples: triples, complete: cb == tagsComplete}, nil
}

// Tag returns the tag triple for a 1-based vocabulary id. A miss is not an error
// for the caller to swallow: the blob decoder turns it into one, since a
// reference the vocabulary cannot resolve means the two fields have drifted.
func (t *TagDict) Tag(id uint64) (string, bool) {
	if t == nil || id == 0 || id > uint64(len(t.triples)) {
		return "", false
	}
	return t.triples[id-1], true
}

// Complete reports whether every version in this file carries a derived tag
// list. When false, a consumer must disable tag filtering for the whole file --
// not per package, and not "filter where we have data". Filtering on a partial
// corpus rejects versions whose tags were simply never derived.
//
// A nil dictionary reports false, which is the safe direction.
func (t *TagDict) Complete() bool {
	return t != nil && t.complete
}

// Len returns the number of triples in the vocabulary.
func (t *TagDict) Len() int {
	if t == nil {
		return 0
	}
	return len(t.triples)
}
