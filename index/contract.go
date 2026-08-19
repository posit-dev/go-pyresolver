// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"fmt"
	"sort"

	"github.com/posit-dev/go-python-packaging/extras"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

// This file holds the pieces of RSFIndex's logic that are contracts rather than
// implementation details. The rationale for exporting them, and the list of what
// they are, is in the package documentation under "If you are writing your own
// index over the same bytes" -- kept there rather than here because a comment
// floating between the imports and the first declaration is attached to nothing
// and godoc renders it nowhere, which is a poor home for the one explanation a
// consumer of this file needs.

// EqualityClass is one PEP 440 equality class of stored version keys, reduced to
// the single key that speaks for it.
//
// Key is the winning stored key, spelled exactly as the producer recorded it, so
// it can be used to look the record up. Version is that key parsed.
//
// ⚠️ "Version is Key parsed" is an invariant of the values DedupeEqualityClasses
// RETURNS, not of the type. Both fields are exported, so a hand-built
// EqualityClass{Key: "1.0"} carries the zero Version and makes Canonical() report
// nonsense. Pass around values you got from DedupeEqualityClasses.
type EqualityClass struct {
	// Key is the representative stored version key.
	Key string

	// Version is Key parsed. Sorting is by this value, not by Key.
	Version version.Version
}

// Canonical reports whether Key is already spelled the way PEP 440 normalization
// renders it.
//
// It is what tells a caller whether an alias entry is needed: when the key is
// canonical, a lookup keyed by Version.String() finds the record directly, and
// when it is not, the two spellings differ and the mapping has to be recorded.
func (c EqualityClass) Canonical() bool { return c.Version.String() == c.Key }

// DedupeResult is what DedupeEqualityClasses computed from a package's stored
// version keys.
//
// It is a struct rather than multiple return values so a later field can be added
// without breaking every caller -- this surface is published, and a Go module tag
// cannot be moved once the proxy has served it.
type DedupeResult struct {
	// Classes holds one representative per PEP 440 equality class, sorted
	// ascending by Version. Never nil, and no two elements compare equal, so it
	// is binary-searchable -- see Lookup, which is the search.
	Classes []EqualityClass

	// Rejected holds the keys PEP 440 could not parse, sorted. Nil when every key
	// parsed.
	//
	// ⚠️ It is returned rather than merely dropped because when EVERY key of a
	// package is rejected, Classes is empty -- and that is indistinguishable from
	// a package for which nothing was captured at all. Those are different facts
	// and they call for different responses. Reporting the second when the first
	// is true sends someone looking for missing data that is actually present,
	// just recorded under a string the specification does not accept: a
	// production snapshot holds `holygrail` with one key, "0.2.1.Perceval",
	// carrying a real dependency on sqlobject.
	Rejected []string
}

// DedupeEqualityClasses collapses stored version keys into PEP 440 equality
// classes and returns one representative per class, sorted ascending by the
// parsed version. The result is never nil, and no two elements compare equal, so
// it is binary-searchable with the same comparator that sorted it.
//
// # Why a single representative
//
// The producer records whatever version string a publisher used, so one package
// can carry both "1.0" and "1.0.0" as separate stored keys. Those are the SAME
// version under PEP 440. Returning both hands a resolver two candidates it
// cannot tell apart: they compare equal, so no constraint can select between
// them and the choice falls to whatever order the caller happens to iterate.
//
// Worse, the two stored records can disagree about dependencies -- measured on a
// production snapshot as 59 equality classes across 56 packages, 10 of which
// disagree. A resolver offered both would resolve a different graph depending on
// which it picked, with nothing in the data to justify either answer.
//
// # How the representative is chosen
//
// A key that round-trips through PEP 440 normalization wins, because that is the
// best available evidence of what the publisher actually wrote. Between two keys
// that are both canonical or both not, the lexicographically smaller wins.
//
// ⚠️ That tiebreak is not cosmetic. Without it the winner would come from Go's
// randomized map iteration and the same index would answer differently from one
// call to the next.
//
// # Unparseable keys
//
// A key PEP 440 rejects is skipped rather than failing the package: real corpora
// carry a few non-conforming keys and one of them must not make every other
// version unreachable.
//
// ⚠️ Skipping is silent here by design, but it must not be silent to a human.
// When EVERY key of a package is rejected this returns an empty slice, which is
// indistinguishable from a package for which nothing was captured at all -- and
// those are different facts. A caller that needs to tell them apart must report
// the rejected keys separately; RSFIndex does so through UnparseableVersionKeys.
func DedupeEqualityClasses(keys []string) DedupeResult {
	candidates := make([]EqualityClass, 0, len(keys))
	var rejected []string
	for _, raw := range keys {
		v, parseErr := version.Parse(raw)
		if parseErr != nil {
			rejected = append(rejected, raw)
			continue
		}
		candidates = append(candidates, EqualityClass{Key: raw, Version: v})
	}
	sort.Strings(rejected)

	// Sorting is not about the returned order for its own sake -- it is what puts
	// the members of a class next to each other so one representative can be
	// taken per class. Within a class, order by the same rule that picks the
	// representative, so the winner is simply the first member.
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].Version.Equal(candidates[j].Version) {
			return candidates[i].Version.LessThan(candidates[j].Version)
		}
		return preferKey(candidates[i].Key, candidates[i].Canonical(),
			candidates[j].Key, candidates[j].Canonical())
	})

	// Non-nil even when every key was rejected: an empty slice and a nil one are
	// not the same value to a caller that checks.
	out := make([]EqualityClass, 0, len(candidates))
	for i, c := range candidates {
		if i > 0 && candidates[i-1].Version.Equal(c.Version) {
			// A later member of a class already represented.
			continue
		}
		out = append(out, c)
	}

	// Clipped to its length before it crosses the API boundary. A collapsed class
	// leaves spare capacity behind, and a slice with len < cap is the shape that
	// lets an append by one holder grow IN PLACE into memory another holder is
	// reading -- the same hazard storePlan clips for internally.
	return DedupeResult{Classes: out[:len(out):len(out)], Rejected: rejected}
}

// Lookup returns the class representative that answers for ver, searching
// classes as returned by DedupeEqualityClasses.
//
// ⚠️ THIS IS THE STEP THAT ACTUALLY DRIFTED, and the reason to call it rather
// than hand-roll it. A caller can hold a version that is PEP 440-equal to a
// stored key while being spelled like NEITHER that key nor the class
// representative: stored "1.0" against a request parsed from "1.0.0.0" renders
// "1.0.0.0", so a map keyed by either spelling misses, yet the two versions ARE
// equal because trailing zeros are insignificant. An implementation that checks
// only its key map and its canonical-rendering map reports the version unknown
// when it is present. That is a real regression that shipped in a consumer.
//
// It does NOT arise from an index's own Versions output, since those renderings
// are exactly what a canonical map is keyed by -- which is why a differential
// test between two paths that both route through the same lookup cannot see it.
// It arises from a version the caller constructed: an admin-pinned requirement,
// or one carried over from another index in a MultiIndex.
//
// # What this does and does not cover
//
// It resolves by PEP 440 EQUALITY, which subsumes an exact match on a
// representative's key and a match on its canonical rendering. It cannot see a
// stored key that lost its class, because a non-representative key is not in
// classes by construction -- a caller that wants an exact stored-key hit to win
// must check its own key map FIRST and fall back to this. RSFIndex does exactly
// that.
//
// O(log n) and allocation-free: classes carry their parsed Version, so no probe
// is re-parsed.
func Lookup(classes []EqualityClass, ver version.Version) (EqualityClass, bool) {
	lo, hi := 0, len(classes)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		switch probe := classes[mid].Version; {
		case probe.Equal(ver):
			return classes[mid], true
		case probe.LessThan(ver):
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return EqualityClass{}, false
}

// UnparseableRequirementError reports a stored Requires-Dist string that PEP 508
// rejects. It is what ParseRecord returns when a record cannot be used, and it
// satisfies errors.Is(err, ErrMetadataUnusable) so it can be returned straight
// from a MetadataIndex.Metadata implementation.
//
// ⚠️ It names the requirement and nothing else -- in particular it does NOT name
// a version. The same parsed record answers for every spelling in its equality
// class, so a caller that memoizes this error and renders it later would
// otherwise report the FIRST caller's requested version to every later one. Keep
// the facts here and render the message against the version actually asked
// about, which is what RSFIndex does.
type UnparseableRequirementError struct {
	// Requirement is the stored Requires-Dist string that would not parse.
	Requirement string

	// Err is the underlying parse error, kept in the chain so the specific
	// malformed string stays recoverable for diagnostics.
	Err error
}

func (e *UnparseableRequirementError) Error() string {
	return fmt.Sprintf("parsing requirement %q: %v", e.Requirement, e.Err)
}

// Unwrap returns BOTH the parse cause and ErrMetadataUnusable.
//
// ⚠️ The sentinel is load-bearing, not decoration. MetadataIndex consumers branch
// on errors.Is(err, ErrMetadataUnusable) -- MultiIndex does, to decide whether a
// later source may still answer for this version. An index that does the obvious
// thing and returns ParseRecord's error straight out of Metadata would otherwise
// produce a refusal that no composer recognizes, and MultiIndex would take the
// wrong branch. Carrying it here makes the obvious implementation the correct
// one.
//
// errors.Is still reaches the parse cause, so a diagnostic caller loses nothing.
func (e *UnparseableRequirementError) Unwrap() []error {
	return []error{ErrMetadataUnusable, e.Err}
}

// RawRecord is the set of published strings ParseRecord reads: exactly the
// METADATA fields an RSF record carries that bear on resolution.
//
// ⚠️ It is a NAMED-FIELD struct rather than positional arguments deliberately.
// RequiresDist and ProvidesExtra are both []string, so a positional signature
// lets a caller transpose them, and the transposition does not fail -- a bare
// extra name like "docs" is a valid PEP 508 requirement and a requirement string
// normalizes as an extra, so the call SUCCEEDS and returns an inverted dependency
// set with no error. Measured, not theorized. A struct also lets a fourth
// published field be added later without breaking every caller.
type RawRecord struct {
	// RequiresDist is the record's Requires-Dist lines, unparsed.
	RequiresDist []string

	// RequiresPython is the record's Requires-Python constraint, or "" if it
	// declared none.
	RequiresPython string

	// ProvidesExtra is the record's Provides-Extra names, un-normalized.
	ProvidesExtra []string
}

// ParseRecord builds the parsed metadata triple from the strings a record
// published: its Requires-Dist set, its Requires-Python constraint, and its
// Provides-Extra list.
//
// It is a pure function of its arguments. Name, Version and Origin are left
// zeroed, because they identify which index answered and which version was
// asked about -- facts the record itself does not carry. A caller fills them in.
//
// # The deliberate asymmetry between the two failure modes
//
// An unparseable REQUIREMENT is fatal: it returns *UnparseableRequirementError
// and no metadata. Dropping it silently would hand the resolver an incomplete
// dependency set and produce a confident wrong answer, which is the one failure
// mode worth failing loudly for.
//
// An unparseable REQUIRES-PYTHON is not fatal. The record is returned with
// RequiresPython left unconstrained and RequiresPythonUnreadable set. An
// unreadable interpreter constraint only over-admits a candidate, surfacing
// later as an install-time failure, whereas an unreadable requirement would
// under-constrain the graph and change the resolution itself. pip draws the line
// the same way: it catches InvalidSpecifier on Requires-Python and treats the
// candidate as compatible.
//
// ⚠️ The permissiveness is RECORDED, not merely applied. Leaving RequiresPython
// empty is a decision this code made, not a fact about the record, and
// RequiresPythonUnreadable is what lets a caller say which of the two it is
// looking at. RequiresPythonRaw is likewise preserved verbatim whether or not it
// parses, so "the record declared no interpreter constraint" stays
// distinguishable from "the record declared one we could not read" -- conflating
// them reports that the publisher said nothing when the publisher said something
// unreadable.
//
// Provides-Extra entries are normalized per PEP 685, so a request for
// pkg[Test-Suite] matches a declared "test_suite".
func ParseRecord(raw RawRecord) (PackageMetadata, error) {
	var meta PackageMetadata

	if len(raw.RequiresDist) > 0 {
		meta.RequiresDist = make([]requirement.Requirement, 0, len(raw.RequiresDist))
		for _, rawReq := range raw.RequiresDist {
			req, reqErr := requirement.Parse(rawReq)
			if reqErr != nil {
				return PackageMetadata{}, &UnparseableRequirementError{Requirement: rawReq, Err: reqErr}
			}
			meta.RequiresDist = append(meta.RequiresDist, req)
		}
	}

	meta.RequiresPythonRaw = raw.RequiresPython

	if raw.RequiresPython != "" {
		specs, specErr := version.NewSpecifiers(raw.RequiresPython)
		if specErr != nil {
			meta.RequiresPython = version.Specifiers{}
			meta.RequiresPythonUnreadable = true
		} else {
			meta.RequiresPython = specs
		}
	}

	if len(raw.ProvidesExtra) > 0 {
		meta.ProvidesExtra = make([]string, 0, len(raw.ProvidesExtra))
		for _, extra := range raw.ProvidesExtra {
			meta.ProvidesExtra = append(meta.ProvidesExtra, extras.Normalize(extra))
		}
	}

	return meta, nil
}

// Clone returns a deep copy of m that is safe to hand an external caller, and is
// the copy contract a cache serving PackageMetadata must apply on every read.
//
// # The rule
//
// Every EXPORTED MUTABLE slice reachable from the returned value is copied.
// Exported, because that is what a caller can reach without unsafe; mutable,
// because a string is not.
//
//   - RequiresDist -- copied. The slice a caller sorts or truncates.
//   - RequiresDist[i].Extras -- copied. ⚠️ It is exported, it is a []string, and
//     it is reachable THROUGH the copied RequiresDist, so copying the outer
//     slice alone leaves it aliased: `first.RequiresDist[i].Extras[0] = "x"`
//     corrupted a shared cache entry permanently, for every later caller, until
//     this loop existed. It is also the ONLY such slice below RequiresDist --
//     Specifiers and Marker are unexported all the way down -- so this closes
//     the gap rather than narrowing it.
//   - ProvidesExtra -- copied. Same reasoning as RequiresDist.
//   - RequiresPython -- SHARED, deliberately. version.Specifiers wraps a
//     [][]Specifier, but the outer field and every field of a Specifier are
//     unexported, so a caller holding one has no exported path to any element:
//     it is read-only in practice for the same reason a Marker is, and copying
//     it would cost an allocation per call to defend nothing.
//   - Name, Version, Origin, RequiresPythonRaw, RequiresPythonUnreadable --
//     values.
//
// ⚠️ THE MAINTENANCE CONTRACT: adding an exported slice to PackageMetadata, or
// to requirement.Requirement on a go-python-packaging bump, means adding a copy
// here. This method exists on the type that defines that contract precisely so
// no consumer has to notice the change independently.
//
// nil is preserved rather than normalized to an empty slice: "the record
// declared no requirements" comes back as a nil slice, and a caller
// distinguishing nil from empty must keep seeing what it saw. The same holds for
// Requirement.Extras, which go-python-packaging documents as nil when the
// requirement carried no "[...]" clause.
func (m PackageMetadata) Clone() PackageMetadata {
	if m.RequiresDist != nil {
		reqs := make([]requirement.Requirement, len(m.RequiresDist))
		copy(reqs, m.RequiresDist)
		for i := range reqs {
			if reqs[i].Extras != nil {
				ex := make([]string, len(reqs[i].Extras))
				copy(ex, reqs[i].Extras)
				reqs[i].Extras = ex
			}
		}
		m.RequiresDist = reqs
	}
	if m.ProvidesExtra != nil {
		extra := make([]string, len(m.ProvidesExtra))
		copy(extra, m.ProvidesExtra)
		m.ProvidesExtra = extra
	}
	return m
}
