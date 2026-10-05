// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"fmt"
)

// YankHistoryRecordName is the reserved canonical name the yank-history record
// is stored under. '~' (0x7e) is not valid in a PEP 503-normalized name, so it
// cannot collide with a real package, and it sorts after every real name.
//
// It is written as the LAST physical record in the file (never record 0), so
// it is never mistaken for the v0.13.0 design-B marker, which that reader
// only ever checked on record 0. See rstudio/package-manager#21025 S1's
// report for the full byte layout.
const YankHistoryRecordName = "~ppm-yank-history-v1"

const ldSentinelCname = YankHistoryRecordName

// yankTransition is one recorded change of a version's yanked state, keyed by
// the RSF snapshot key it happened at.
type yankTransition struct {
	key    string
	yanked bool
}

// YankHistory is the decoded payload of the yank-history sentinel record:
// every version that was EVER yanked at least once, with its transitions in
// ascending key order. A (cname, version) pair absent entirely means it was
// never yanked.
//
// Open applies the latest state only (see applyLatest); callers that need the
// state as of a snapshot use YankedAt.
type YankHistory struct {
	byPkg    map[string]map[string][]yankTransition
	captured bool
}

// DecodeYankHistory decodes the deps field of the yank-history sentinel record
// (format byte plus payload, the same input form as DecodePackage). d is needed
// only when the field is zstd-compressed.
func DecodeYankHistory(field string, d *Dict) (*YankHistory, error) {
	blob, err := decompress(field, d)
	if err != nil {
		return nil, err
	}
	h, captured, err := decodeYankHistory(blob)
	if err != nil {
		return nil, err
	}
	h.captured = captured
	return h, nil
}

// Captured reports whether the producer checked every element in the file, so
// that a version absent from the history is a trustworthy "never yanked". A nil
// history is not captured.
func (h *YankHistory) Captured() bool { return h != nil && h.captured }

// YankedAt reports whether the version was yanked as of snapshot key: the state
// of its last transition with key <= the given key. No history, or no
// transition at or before key, means not yanked. Keys are compared as strings,
// as the producer does (rstudio/pypi-manifest YankedAt); they are 10-digit Unix
// timestamps, so string and numeric order agree. cname is the canonical name;
// version is the string exactly as the deps blob keys it.
func (h *YankHistory) YankedAt(cname, version, key string) bool {
	if h == nil {
		return false
	}
	yanked := false
	for _, tr := range h.byPkg[cname][version] {
		if tr.key > key {
			break
		}
		yanked = tr.yanked
	}
	return yanked
}

// YankedLatest reports the version's latest state: that of its last
// transition, or false when it has no history. Same naming rules as YankedAt.
func (h *YankHistory) YankedLatest(cname, version string) bool {
	if h == nil {
		return false
	}
	trans := h.byPkg[cname][version]
	return len(trans) > 0 && trans[len(trans)-1].yanked
}

// yankHistoryFormatVersion is the sentinel payload's first byte. Matches
// rstudio/pypi-manifest's producer (internal/manifest/metadata/yankhistory.go,
// the S2 unit of #21025) -- this package is the consumer of that wire format,
// not its own inventor.
const yankHistoryFormatVersion = 1

// decodeYankHistory decodes an already-decompressed sentinel deps blob body.
// DecodeYankHistory is the exported entry point.
//
// Layout (all integers uvarint, all strings uvarint-length prefixed), per S2:
//
//	byte(formatVersion)
//	byte(captured)  -- 0 or 1, see the captured return value
//	uvarint(numPackages)
//	  per package:
//	    cname
//	    uvarint(numVersionsWithHistory)
//	      per version:
//	        version
//	        uvarint(numTransitions)
//	          per transition, ascending key order: key, 1 byte (0=unyanked, 1=yanked)
//
// captured is the producer's own backfill-complete marker: true means every
// non-deleted element in the file has been checked, so absence of a version
// from the history is a trustworthy "never yanked" rather than "not yet
// checked". This is the file's real "unknown vs none" signal -- LD's payload
// has no per-package or per-version unknown state, only this one file-wide
// bit.
func decodeYankHistory(blob []byte) (h *YankHistory, captured bool, err error) {
	r := bytes.NewReader(blob)

	formatVersion, err := r.ReadByte()
	if err != nil {
		return nil, false, err
	}
	if formatVersion != yankHistoryFormatVersion {
		return nil, false, fmt.Errorf("pypirsf: yank history: unsupported format version %d", formatVersion)
	}
	capturedByte, err := r.ReadByte()
	if err != nil {
		return nil, false, err
	}
	captured = capturedByte != 0

	numPkgs, err := readUvarint(r)
	if err != nil {
		return nil, false, err
	}
	h = &YankHistory{byPkg: make(map[string]map[string][]yankTransition, capHint(numPkgs, r, 2))}

	for p := uint64(0); p < numPkgs; p++ {
		cname, err := readStr(r)
		if err != nil {
			return nil, false, err
		}
		numVers, err := readUvarint(r)
		if err != nil {
			return nil, false, err
		}
		vm := make(map[string][]yankTransition, capHint(numVers, r, 2))
		for v := uint64(0); v < numVers; v++ {
			version, err := readStr(r)
			if err != nil {
				return nil, false, err
			}
			numTrans, err := readUvarint(r)
			if err != nil {
				return nil, false, err
			}
			trans := make([]yankTransition, 0, capHint(numTrans, r, 2))
			for i := uint64(0); i < numTrans; i++ {
				key, err := readStr(r)
				if err != nil {
					return nil, false, err
				}
				flag, err := r.ReadByte()
				if err != nil {
					return nil, false, err
				}
				trans = append(trans, yankTransition{key: key, yanked: flag == 0x01})
			}
			vm[version] = trans
		}
		h.byPkg[cname] = vm
	}

	if r.Len() != 0 {
		return nil, false, fmt.Errorf("pypirsf: yank history: %d trailing bytes after decode", r.Len())
	}

	return h, captured, nil
}

// applyLatest sets Yanked on every version of deps that this history has a
// record for, to the state of its LAST transition -- the latest state, per
// the Q2 ruling. A version with no transitions recorded is left untouched
// (never yanked).
func (h *YankHistory) applyLatest(cname string, deps map[string]VersionDeps) {
	if h == nil {
		return
	}
	pkgHist, ok := h.byPkg[cname]
	if !ok {
		return
	}
	for ver, trans := range pkgHist {
		if len(trans) == 0 {
			continue
		}
		vd, present := deps[ver]
		if !present {
			// No captured dependency data for this version; nothing to flag.
			continue
		}
		vd.Yanked = trans[len(trans)-1].yanked
		deps[ver] = vd
	}
}
