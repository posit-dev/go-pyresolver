// SPDX-License-Identifier: Apache-2.0 OR MIT

package pypirsf

import (
	"bytes"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

const depsdictFormatByte = 0x01

// MaxDecompressedBytes bounds a single package blob's decompressed size.
//
// This closes the decompression-bomb vector: a small compressed field can
// otherwise expand to gigabytes. It matters more here than it does inside a
// server, because a standalone tool decodes a file the user downloaded, and
// nothing upstream has vouched for it.
const MaxDecompressedBytes = 64 << 20 // 64 MiB

// Dict is the global dependency dictionary from the RSF's first record: the
// ordered dep-name table plus the trained zstd dictionary.
//
// It owns a zstd decoder built once and reused for every package blob decoded
// against this file. zstd.Decoder.DecodeAll is safe for concurrent use, so one
// Dict can serve concurrent decodes.
type Dict struct {
	names []string
	zdict []byte
	dec   *zstd.Decoder
}

// ParseDepsdictField parses the first record's depsdict field and builds the
// shared decoder.
//
// Wire format: format byte 0x01, a uvarint name count, that many
// length-prefixed names, a uvarint dictionary length, then the zstd dictionary.
//
// The caller owns the result and should Close it when done with the file.
func ParseDepsdictField(field []byte) (*Dict, error) {
	r := bytes.NewReader(field)

	fb, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if fb != depsdictFormatByte {
		return nil, fmt.Errorf("pypirsf: bad depsdict format byte 0x%02x", fb)
	}

	nameCount, err := readUvarint(r)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, capHint(nameCount, r, 1))
	for i := uint64(0); i < nameCount; i++ {
		s, err := readStr(r)
		if err != nil {
			return nil, err
		}
		names = append(names, s)
	}

	zlen, err := readUvarint(r)
	if err != nil {
		return nil, err
	}
	if zlen > uint64(r.Len()) {
		return nil, fmt.Errorf("pypirsf: zdict length %d exceeds %d bytes remaining", zlen, r.Len())
	}
	zdict := make([]byte, zlen)
	if _, err := io.ReadFull(r, zdict); err != nil {
		return nil, err
	}

	opts := []zstd.DOption{
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(MaxDecompressedBytes),
	}
	if len(zdict) > 0 {
		opts = append(opts, zstd.WithDecoderDicts(zdict))
	}
	dec, err := zstd.NewReader(nil, opts...)
	if err != nil {
		return nil, err
	}

	return &Dict{names: names, zdict: zdict, dec: dec}, nil
}

// Names returns the ordered dep-name table.
//
// Exposed because the table doubles as a bounded set of the dependency names
// appearing across the corpus, which a consumer can use for integer-id
// comparison instead of string comparison. The returned slice is shared and
// must not be modified.
func (d *Dict) Names() []string {
	if d == nil {
		return nil
	}
	return d.names
}

// Close releases the shared decoder. Safe on a nil Dict.
func (d *Dict) Close() error {
	if d != nil && d.dec != nil {
		d.dec.Close()
	}
	return nil
}
