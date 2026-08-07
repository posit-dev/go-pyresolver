// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"time"

	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// PackageMetadata is the dependency information for one (package, version).
//
// This is deliberately only what resolution needs. Descriptive metadata --
// summary, author, classifiers, license -- is not here: it is not an input to
// any resolution decision, and carrying it would inflate a type that exists on
// a per-candidate path.
type PackageMetadata struct {
	// Name and Version identify what this metadata describes. They are
	// redundant with the arguments the caller passed to Metadata, and are kept
	// so a PackageMetadata remains self-describing once it has been moved into
	// a log line, an error, or a cache entry.
	Name    PackageName
	Version version.Version

	// RequiresDist is the parsed Dist-Requires-Dist set: this version's
	// dependencies, each a PEP 508 requirement whose marker may make it
	// conditional.
	//
	// Parsed rather than raw strings, because every consumer needs it parsed
	// and re-parsing per candidate during resolution is pure waste.
	RequiresDist []requirement.Requirement

	// RequiresPython constrains the interpreter this version supports
	// (METADATA's Requires-Python).
	//
	// A version whose RequiresPython excludes the target interpreter is not a
	// usable candidate, which makes this a filter input rather than
	// information.
	//
	// Prefer SupportsPython over calling Check directly. It is the
	// intention-revealing spelling, and it keeps working if this package is
	// ever built against an older go-python-packaging.
	//
	// This comment used to warn that Check INVERTED the meaning of the zero
	// value, returning false for every version when the set holds no groups.
	// That was true and is no longer: go-python-packaging v0.3.0 made an empty
	// specifier set admit every version, which is what PEP 508 requires of an
	// omitted Requires-Python. The zero value now means "unconstrained" in both
	// this field and in Check. Verified against v0.3.1 rather than assumed.
	//
	// An absent Requires-Python is what leaves that zero value behind, and in a
	// production PyPI snapshot that is over two million versions.
	RequiresPython version.Specifiers

	// RequiresPythonRaw is the interpreter constraint exactly as the record
	// carried it, or "" when the record declared none.
	//
	// It exists because RequiresPython alone cannot distinguish two different
	// facts, and a caller that conflates them reports a falsehood:
	//
	//	record said nothing          -> RequiresPython empty, Raw ""
	//	record said something we
	//	  could not parse            -> RequiresPython empty, Raw "the string"
	//
	// The decoder deliberately treats an unparseable constraint as
	// unconstrained rather than failing the version, because an unreadable
	// interpreter constraint only over-admits a candidate (surfacing later as an
	// install-time failure) while an unreadable *requirement* would silently
	// under-constrain the graph and change the resolution. But "we chose to
	// ignore it" is not the same claim as "there was nothing to ignore", and
	// only this field preserves the difference.
	RequiresPythonRaw string

	// RequiresPythonUnreadable reports that the record declared an interpreter
	// constraint which could not be parsed, so RequiresPython is a permissive
	// fallback rather than a faithful reading of the record.
	//
	// Stored explicitly rather than derived from
	// `RequiresPythonRaw != "" && RequiresPython is empty`, because that
	// derivation silently becomes wrong the day an empty-but-parseable
	// constraint string exists, and because a caller should not have to
	// reconstruct a decision the decoder already made.
	RequiresPythonUnreadable bool

	// ProvidesExtra lists the extras this version defines, PEP 685-normalized.
	//
	// Needed to reject a request for an extra that does not exist. Without it
	// a typo like pkg[tests] where the extra is spelled test resolves happily
	// and silently installs nothing extra.
	ProvidesExtra []string

	// Origin names the index this metadata came from.
	//
	// Included per the settled interface decision in RFD 0001 Section 16: it
	// costs ~16 bytes per record and it is what makes a MultiIndex debuggable,
	// since otherwise there is no way to tell which source answered.
	Origin string
}

// SupportsPython reports whether this version's Requires-Python admits the given
// interpreter version.
//
// # Why this exists rather than calling RequiresPython.Check
//
// An empty specifier set must admit EVERY interpreter: a conjunction over no
// constraints is vacuously true, and that is what the reference implementation
// does (`Version("3.11") in SpecifierSet("")` is True). But
// version.Specifiers.Check iterates its groups and returns false when there are
// none, so on an empty set it answers the exact opposite — no interpreter is
// admitted. Its own andCheck helper returns true for an empty GROUP, so the
// library is inconsistent with itself; filed upstream.
//
// Two paths here produce an empty set, and both mean unconstrained:
//
//   - The version declares no Requires-Python at all. Over two million versions
//     in a production PyPI snapshot.
//   - Its Requires-Python is unparseable, which is deliberately non-fatal (see
//     RSFIndex). pip does the same thing: it catches InvalidSpecifier and
//     treats the candidate as compatible.
//
// So the leniency the parser intends is only actually lenient if callers come
// through here.
func (m PackageMetadata) SupportsPython(target version.Version) bool {
	if m.RequiresPython.String() == "" {
		return true
	}
	return m.RequiresPython.Check(target)
}

// DistKind distinguishes a built wheel from a source distribution.
type DistKind int

const (
	// DistKindUnknown is the zero value, meaning the kind was never set. It is
	// first so that a DistFile built by an incomplete implementation is
	// obviously incomplete rather than silently claiming to be a wheel.
	DistKindUnknown DistKind = iota

	// DistKindWheel is a built distribution (.whl).
	DistKindWheel

	// DistKindSDist is a source distribution (.tar.gz, .zip).
	DistKindSDist
)

// String implements fmt.Stringer.
func (k DistKind) String() string {
	switch k {
	case DistKindWheel:
		return "wheel"
	case DistKindSDist:
		return "sdist"
	default:
		return "unknown"
	}
}

// DistFile is one distribution file for a (package, version).
type DistFile struct {
	// Filename is the base filename, e.g. "flask-3.0.0-py3-none-any.whl".
	//
	// Kept separately from Location because the filename carries the
	// compatibility tags, and parsing them out of an arbitrary URL is not
	// something the interface should require of a consumer.
	Filename string

	// Location is where the file can be retrieved from, as a scheme-prefixed
	// string such as "https://..." or "file://...".
	//
	// Per the settled decision in RFD 0001 Section 16, this interface treats
	// Location as OPAQUE: it is the consumer's job to parse the scheme and
	// decide how to fetch. Being self-describing is what makes it useful in a
	// log line or an error message, and it is what lets a connected index and
	// an air-gapped index return the same type.
	Location string

	// Kind is whether this is a wheel or an sdist, derived once at
	// construction so consumers need not re-parse the filename.
	Kind DistKind

	// Size is the file size in bytes, or 0 if the index did not report one.
	Size int64

	// Hashes maps a lowercase hash algorithm name to its lowercase hex digest,
	// e.g. {"sha256": "..."}. A map rather than a single SHA256 field because
	// PEP 691 permits multiple, and the offline downloader needs whichever the
	// index actually published in order to verify what it wrote to disk.
	Hashes map[string]string

	// UploadTime is when the file was published.
	//
	// Included per the settled decision in RFD 0001 Section 16: it is what
	// makes an --exclude-newer style snapshot cutoff expressible natively,
	// and it corresponds to PyPI's upload_time_iso_8601.
	UploadTime time.Time

	// RequiresPython is the per-file interpreter constraint from the Simple
	// index (PEP 503's data-requires-python). Zero value means unconstrained.
	//
	// This is per-FILE and is not the same as PackageMetadata.RequiresPython:
	// a release can ship one wheel for older interpreters and another for
	// newer ones, so the file-level constraint is what decides whether a
	// particular file is usable.
	RequiresPython version.Specifiers

	// Yanked reports whether the file was yanked per PEP 592, with
	// YankedReason carrying the publisher's explanation when they gave one.
	//
	// Yanking is a per-file property in PEP 592, which is why it lives here
	// and not on PackageMetadata. RFD 0001 Section 6 assigns yanked *policy*
	// to FilteredIndex; that policy needs the fact to be carried somewhere,
	// and this is the only per-file type. Note this goes slightly beyond the
	// four interface decisions Section 16 settles explicitly.
	Yanked       bool
	YankedReason string
}

// IsWheel reports whether f is a built distribution.
func (f DistFile) IsWheel() bool { return f.Kind == DistKindWheel }
