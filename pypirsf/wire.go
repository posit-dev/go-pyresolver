// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// readUvarint reads a LEB128 varint. bytes.Reader is an io.ByteReader, and
// binary.ReadUvarint bounds itself, erroring past 10 bytes or on overflow.
func readUvarint(r *bytes.Reader) (uint64, error) {
	return binary.ReadUvarint(r)
}

// readStr reads a uvarint byte length followed by that many UTF-8 bytes.
//
// The length is checked against the bytes remaining before allocating: a string
// cannot be longer than what is left in the input, so a crafted huge length
// errors out instead of attempting a huge make().
func readStr(r *bytes.Reader) (string, error) {
	n, err := readUvarint(r)
	if err != nil {
		return "", err
	}
	if n > uint64(r.Len()) {
		return "", fmt.Errorf("pypirsf: string length %d exceeds %d bytes remaining", n, r.Len())
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// capHint returns a safe pre-allocation hint for a slice or map of count
// elements, where each element occupies at least minElemBytes on the wire.
//
// This is only a hint. Callers MUST still append element by element, reading
// each one, rather than trusting count — so a truncated stream fails on a read
// rather than on an oversized allocation.
func capHint(count uint64, r *bytes.Reader, minElemBytes int) int {
	if minElemBytes < 1 {
		minElemBytes = 1
	}
	maxCount := uint64(r.Len() / minElemBytes)
	if count > maxCount {
		count = maxCount
	}
	return int(count)
}
