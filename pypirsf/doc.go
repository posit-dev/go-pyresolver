// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package pypirsf reads PyPI dependency metadata out of a Repository Snapshot
// Format (RSF) file.
//
// It carries two things: the PyPI record layout, and the decoder for the
// per-package dependency blob plus the global dictionary that blob is encoded
// against.
//
// # Why this lives here and not in the format library
//
// github.com/rstudio/repository-snapshot-format is deliberately generic — it
// knows about records, fields, and indexes, and nothing about any particular
// package ecosystem. A PyPI record schema is not format knowledge, it is Python
// packaging knowledge, so it belongs with the code that understands Python
// packaging.
//
// # Decode only
//
// This package decodes; it does not write. RSF files are produced by a separate
// pipeline, and a reader that could also write would invite a second,
// divergent producer.
//
// # Hardening
//
// Every length-prefixed read is bounded against the bytes actually present, and
// the zstd decoder is capped, because the input is a file fetched over a
// network and is not necessarily well-formed. No allocation is sized from a
// number the input claims without first checking that many bytes exist. See
// MaxDecompressedBytes for the decompression-bomb bound specifically.
//
// # Neutral types
//
// Decoding produces VersionDeps — raw PEP 508 and PEP 440 strings, unparsed.
// Parsing belongs to the caller (github.com/posit-dev/go-python-packaging), so
// this package depends only on a zstd implementation. That keeps it usable both
// by a resolver that wants parsed requirements and by a server that wants to
// hand the raw strings straight to an API response.
package pypirsf
