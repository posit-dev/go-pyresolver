// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/posit-dev/go-python-packaging/version"
)

// FilterPolicy is the release-admission policy a FilteredIndex applies.
//
// # The zero value filters nothing
//
// That is why every boolean is spelled Exclude rather than Allow. A wrapper
// whose zero value silently dropped every pre-release would punish the caller
// who wrapped an index only to compose it, and adding a fourth axis later would
// change the behavior of code that never asked for it. FilteredIndex with a zero
// policy is a pass-through, and a new axis will be too.
//
// # Which axes need distribution files
//
// Only ExcludePrereleases can be decided from a version alone. Both
// ExcludeYanked and SnapshotDate are properties of individual FILES -- PEP 592
// yanking is per-file, and an upload time belongs to a file, not a release -- so
// they can only be evaluated through Files.
//
// That is not a detail of this implementation, it is a constraint on
// composition: a file-level policy over an index that serves no files (RSFIndex,
// by the shape of the data) can admit NOTHING, because there is no file to admit
// anything on.
//
// ⚠️ Such a policy REFUSES rather than answering: Versions, Metadata and Files
// all report ErrFilesUnavailable. Returning empty version lists instead would
// report every package as having no acceptable version, which downstream reads as
// a constraint conflict that does not exist. v0.3.0 did exactly that; see
// hasAdmissibleFile for why, and for why refusing is sound now when it was not
// then.
//
// # The two file axes fail in OPPOSITE directions
//
// ⚠️ Worth knowing before relying on either, because it is not symmetric and the
// asymmetry runs the unsafe way:
//
//	SnapshotDate    fails CLOSED. An unrecorded upload time cannot be shown to
//	                precede the cutoff, so the file is dropped.
//	ExcludeYanked   fails OPEN. DistFile.Yanked cannot express "not captured",
//	                so a source that does not record PEP 592 data reports every
//	                file as un-yanked and the policy admits everything while
//	                appearing to work.
//
// Only the date axis is genuinely safe against missing data. The yanked axis is
// fixable additively -- a YankedKnown field on DistFile would let this policy
// refuse what it cannot verify, exactly as the date axis does -- so it is not
// locked in by shipping this. It is not fixed here because it changes DistFile,
// which every implementation populates.
type FilterPolicy struct {
	// ExcludePrereleases drops pre-release versions, which per PEP 440 means
	// anything carrying a pre-release segment (a, b, rc) OR a dev segment. A
	// post-release is NOT a pre-release.
	//
	// ⚠️ This is a HARD filter, not pip's default preference. pip excludes
	// pre-releases from *selection* but admits one whose specifier can match
	// nothing else -- and that exception is driven by the requirement being
	// solved, which is not in scope at this seam and cannot be. If you want pip's
	// behavior, leave this false and let the resolver apply the preference where
	// it can see the specifier; set it true when the answer is "this repository
	// does not serve pre-releases", which is the case a hard filter fits.
	ExcludePrereleases bool

	// ExcludeYanked drops yanked files per PEP 592, and drops a version whose
	// files are all yanked.
	//
	// ⚠️ It can only be as good as the data. DistFile.Yanked is a plain bool with
	// no way to express "yank status was not captured", so an index that does not
	// record yanking reports every file as un-yanked and this policy admits
	// everything while appearing to work. Only set it over a source whose files
	// actually carry PEP 592 data. Tracked as a gap in DistFile rather than
	// papered over here, because inventing an answer would be worse.
	ExcludeYanked bool

	// SnapshotDate, when non-zero, drops files uploaded after that instant, and
	// drops a version left with no files.
	//
	// The cutoff is INCLUSIVE: a file uploaded at exactly this instant existed as
	// of it.
	//
	// ⚠️ A file whose UploadTime is the zero value is DROPPED, not admitted. An
	// unrecorded upload time is a different fact from one before the cutoff, and
	// conflating them would let a file published yesterday into a snapshot dated
	// last year -- invisibly, since nothing in the result would say so. Dropping
	// it is the choice that shows up: the file, or the whole version, goes
	// missing where someone can see it.
	SnapshotDate time.Time
}

// filtersFiles reports whether any axis of the policy has to look at files.
//
// This is what keeps a pre-release-only policy composable over an index that
// serves no files: FilteredIndex calls Files only when this returns true.
func (p FilterPolicy) filtersFiles() bool {
	return p.ExcludeYanked || !p.SnapshotDate.IsZero()
}

// admitsVersion applies the axes decidable from a version alone.
func (p FilterPolicy) admitsVersion(ver version.Version) bool {
	return !p.ExcludePrereleases || !ver.IsPreRelease()
}

// admitsFile applies the per-file axes.
func (p FilterPolicy) admitsFile(f DistFile) bool {
	if p.ExcludeYanked && f.Yanked {
		return false
	}
	if !p.SnapshotDate.IsZero() {
		// See SnapshotDate: an unrecorded time cannot be shown to precede the
		// cutoff, so it is not admitted.
		if f.UploadTime.IsZero() || f.UploadTime.After(p.SnapshotDate) {
			return false
		}
	}
	return true
}

// String implements fmt.Stringer, rendering the active axes.
//
// ⚠️ It MUST be safe on the zero value, and this is not a stylistic point. fmt
// recovers a panic raised inside a String method and substitutes
// "%!s(PANIC=...)", so a panicking String is swallowed at exactly the call sites
// most likely to reach it -- a log line, an error message -- and the defect
// never surfaces as a crash. That is how rstudio/package-manager#19466's F14
// hid: the one call site that did not crash was the one that formatted.
func (p FilterPolicy) String() string {
	var parts []string
	if p.ExcludePrereleases {
		parts = append(parts, "exclude-prereleases")
	}
	if p.ExcludeYanked {
		parts = append(parts, "exclude-yanked")
	}
	if !p.SnapshotDate.IsZero() {
		parts = append(parts, "snapshot-date="+p.SnapshotDate.Format(time.RFC3339))
	}
	if len(parts) == 0 {
		return "no filtering"
	}
	return strings.Join(parts, ",")
}

// FilteredIndex wraps a MetadataIndex and applies a FilterPolicy to everything
// it serves.
//
// # The policy is enforced on all three methods
//
// Not only on Versions. A wrapper that filtered the listing alone would be
// bypassed by any caller holding a version from somewhere else -- a pin, a
// lockfile, another index -- so it would not be a policy, only a default. The
// cost is that a file-level policy makes Metadata consult Files, which is
// documented rather than avoided because a bypassable exclusion is worse than a
// second lookup.
//
// # Error taxonomy
//
// The inner index is always consulted FIRST, before the policy is applied. That
// ordering is deliberate: it keeps ErrPackageNotFound for a package that does
// not exist, instead of overwriting the specific error with a policy refusal for
// a package nobody has. A version the policy excludes reports
// ErrMetadataUnavailable -- the package WAS found, so ErrPackageNotFound would
// be untrue on its face, and a caller branching on it would report a missing
// package for a present one (see rstudio/package-manager#19466 F12).
//
// A known package all of whose versions the policy excludes is an EMPTY SLICE
// and a nil error from Versions, never ErrPackageNotFound. The interface makes
// that distinction load-bearing.
//
// # A file-level policy needs a file-serving inner index, and SAYS SO
//
// ExcludeYanked and SnapshotDate cannot be evaluated over an index that serves no
// files. Since RSFIndex returns ErrFilesUnavailable unconditionally -- an RSF
// carries no filename, hash, upload time, or yanked flag -- the arrangement RFD
// 0001 calls for is a file-level policy over a MultiIndex pairing the RSF with a
// file-serving source, not over the RSF alone.
//
// ⚠️ GET THAT WRONG AND YOU GET AN ERROR, NOT AN EMPTY ANSWER. Versions, Metadata
// and Files all report ErrFilesUnavailable, wrapped with the advice to compose
// over a file-serving source. The alternative -- dropping every version for want
// of file evidence -- makes every package look like it has no acceptable version,
// which downstream reads as a constraint conflict that does not exist. That is
// strictly worse than an error, because it is indistinguishable from a real
// resolution failure.
//
// This is the behavior v0.3.0 got wrong, and the reason is worth knowing before
// changing it back: the refusal was dropped when ErrFilesUnavailable could not be
// trusted through a MultiIndex, and the change that made it trustworthy shipped in
// the same release. See hasAdmissibleFile.
//
// Note the refusal is a property of the WRAPPER's configuration, not of any one
// package's data, so a caller cannot make it go away by choosing a different
// package. That is what makes it safe to surface from Versions.
//
// # Cost: one Files lookup per version
//
// Versions under a file-level policy issues a Files call per surviving version,
// serially, and a subsequent Metadata call for one of those versions issues
// another for the same version. Over an in-process source (an RSF plus a local
// file list) that is cheap. Over a network-backed source a 500-version package is
// 500 sequential round trips, and nothing here batches or caches.
//
// That is accepted for now rather than designed around, on the grounds that it is
// fixable ADDITIVELY and so does not need to be settled before this ships: an
// optional interface (say a FilesBatch method that FilteredIndex type-asserts for)
// adds no requirement to existing implementations, and caching belongs in the
// index being wrapped, which is where PPM already has a cache layer. A caller
// wrapping a network source should wrap a caching index, not this one.
//
// # Origin is not rewritten
//
// A filter did not produce the record, so PackageMetadata.Origin still names the
// index that did. Relabeling it would make a MultiIndex under a filter
// undebuggable, which is the one job RFD 0001 Section 16 gives the field.
//
// A FilteredIndex is immutable after construction and therefore safe for
// concurrent use whenever its inner index is.
type FilteredIndex struct {
	inner  MetadataIndex
	policy FilterPolicy
}

// NewFilteredIndex returns a FilteredIndex applying policy to inner.
//
// It panics if inner is nil. That is a programming error with no data behind it,
// and panicking at construction points at the line that built the index rather
// than at whichever lookup first dereferenced it.
//
// ⚠️ Note the asymmetry with NewMultiIndex, which accepts ZERO sources happily. A
// MultiIndex over nothing has a defensible meaning -- an index that knows no
// packages, and answers ErrPackageNotFound for everything. A FilteredIndex over
// nothing has none: there is no such thing as filtering the absence of an index,
// and a nil inner could only produce a nil dereference at the first lookup. The
// two differ because the underlying question differs, not by oversight.
func NewFilteredIndex(inner MetadataIndex, policy FilterPolicy) *FilteredIndex {
	if inner == nil {
		panic("index.NewFilteredIndex: inner index is nil")
	}
	return &FilteredIndex{inner: inner, policy: policy}
}

// Versions implements MetadataIndex, returning only the versions the policy
// admits.
//
// ErrPackageNotFound passes through unchanged; a filter does not invent
// packages. A known package whose every version is excluded is an empty slice
// and a nil error.
func (f *FilteredIndex) Versions(ctx context.Context, pkg PackageName) ([]version.Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	versions, err := f.inner.Versions(ctx, pkg)
	if err != nil {
		return nil, err
	}

	if f.policy.ExcludePrereleases {
		kept := make([]version.Version, 0, len(versions))
		for _, ver := range versions {
			if f.policy.admitsVersion(ver) {
				kept = append(kept, ver)
			}
		}
		versions = kept
	}

	if !f.policy.filtersFiles() {
		return versions, nil
	}

	// One Files lookup per surviving version. That is the price of a file-level
	// policy at the listing level, and there is no cheaper way: whether a
	// release existed at a date, or has anything left un-yanked, is only knowable
	// from its files.
	kept := make([]version.Version, 0, len(versions))
	for _, ver := range versions {
		admissible, err := f.hasAdmissibleFile(ctx, pkg, ver)
		if err != nil {
			return nil, err
		}
		if admissible {
			kept = append(kept, ver)
		}
	}
	return kept, nil
}

// Metadata implements MetadataIndex, refusing a version the policy excludes.
func (f *FilteredIndex) Metadata(ctx context.Context, pkg PackageName, ver version.Version) (PackageMetadata, error) {
	if err := ctx.Err(); err != nil {
		return PackageMetadata{}, err
	}

	// Inner first, so an unknown package keeps ErrPackageNotFound and an
	// uninitialized version keeps whatever the inner index says about it.
	meta, err := f.inner.Metadata(ctx, pkg, ver)
	if err != nil {
		return PackageMetadata{}, err
	}

	if !f.policy.admitsVersion(ver) {
		return PackageMetadata{}, f.excluded("Metadata", pkg, ver)
	}

	if f.policy.filtersFiles() {
		admissible, err := f.hasAdmissibleFile(ctx, pkg, ver)
		if err != nil {
			return PackageMetadata{}, err
		}
		if !admissible {
			return PackageMetadata{}, f.excluded("Metadata", pkg, ver)
		}
	}

	return meta, nil
}

// Files implements MetadataIndex, returning only the files the policy admits.
//
// ⚠️ Under an ACTIVE FILE-LEVEL POLICY, no admissible file means
// ErrMetadataUnavailable -- and that includes a version with ZERO files to begin
// with, not only one whose files were filtered away.
//
// The zero-file case is worth stating because it is subtle enough to be
// "corrected" back. An earlier version of this method guarded with
// `len(kept) == 0 && len(files) > 0`, meaning to preserve the interface's
// empty-and-nil answer for a release that genuinely ships no files. It instead
// produced an inconsistency: hasAdmissibleFile found nothing admissible either
// way, so Metadata refused that version and Versions dropped it, while Files
// called it "exists, ships no files". One (pkg, ver), two answers.
//
// Refusing is the side that wins, because:
//
//   - It is what the policy means. "Has a file uploaded at or before the cutoff"
//     and "has an un-yanked file" are both false when there is no file at all. A
//     release with nothing in it cannot be shown to have existed at a date, and
//     admitting it under ExcludeYanked would admit it on no evidence.
//   - Otherwise the policy is bypassable by a caller holding a version from a
//     pin, a lockfile, or another index -- which is the whole reason it is
//     enforced here and not only on Versions.
//   - The old guard made the answer depend on whether the inner index held files
//     BEFORE filtering, an implementation detail invisible to the caller.
//
// The interface's empty-and-nil answer is NOT lost: with no file-level policy
// active, filtersFiles below short-circuits and the inner answer passes through
// verbatim. What the rule gives up is only the zero-file case under a file-level
// policy, where "refused" is the truthful answer rather than a claim about what
// the release ships.
//
// See rstudio/package-manager#19466 F10 for the general shape of the hazard --
// conflating a value that was never there with one that was removed -- and note
// that here the two are distinguished by whether the policy is active at all,
// which is a fact about this index rather than a guess about the data.
//
// ⚠️ One asymmetry with Metadata, deliberate: the inner index is consulted BEFORE
// the version-level policy check, so over a fileless inner index a pre-release
// excluded by ExcludePrereleases reports ErrFilesUnavailable here while Metadata
// reports the policy refusal. Both statements are true, and the order is not
// arbitrary -- checking the policy first would let this method claim "excluded by
// policy" for a package that does not exist, since ErrFilesUnavailable carries no
// information about whether pkg is present and nothing else here does either.
// Establishing that the package exists takes precedence over reporting a decision
// about it. Reversing this to save the round trip would trade a true-but-less-apt
// error for a possibly false one.
func (f *FilteredIndex) Files(ctx context.Context, pkg PackageName, ver version.Version) ([]DistFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	files, err := f.inner.Files(ctx, pkg, ver)
	if err != nil {
		return nil, err
	}

	if !f.policy.admitsVersion(ver) {
		return nil, f.excluded("Files", pkg, ver)
	}

	if !f.policy.filtersFiles() {
		return files, nil
	}

	kept := make([]DistFile, 0, len(files))
	for _, file := range files {
		if f.policy.admitsFile(file) {
			kept = append(kept, file)
		}
	}
	// No admissible file, whether because they were all filtered away or because
	// there were none to begin with. Both mean the policy has nothing to admit
	// this version on. See the method doc for why zero files is not carved out.
	if len(kept) == 0 {
		return nil, f.excluded("Files", pkg, ver)
	}
	return kept, nil
}

// hasAdmissibleFile reports whether (pkg, ver) has at least one file the policy
// admits.
//
// ⚠️ ZERO FILES IS FALSE, not a special case. A version with no files has no file
// satisfying the cutoff and no un-yanked file, so there is nothing to admit it
// on. Versions therefore drops it and Metadata refuses it, and Files agrees --
// the three must not disagree about one (pkg, ver), which they briefly did when
// Files carved the zero-file case out. See FilteredIndex.Files for the full
// reasoning.
//
// ⚠️ A CAPABILITY ANSWER AND A DATA ANSWER ARE NOT THE SAME THING, and the whole
// correctness of a file-level policy rests on the difference:
//
//	ErrFilesUnavailable       the index cannot serve files AT ALL, so the policy
//	                          cannot be evaluated. REFUSED, loudly.
//	ErrMetadataUnavailable    this version has no file this index can produce.
//	ErrPackageNotFound        A data answer: the version is DROPPED, nil error.
//
// Silently dropping on a capability failure would report every package in the
// index as having no acceptable version, which downstream reads as a constraint
// conflict that does not exist. That is the outcome this whole file is arranged to
// avoid, so it is an error rather than an empty answer.
//
// # Why this is now sound, having twice not been
//
// v0.3.0 dropped silently here, because ErrFilesUnavailable could not be trusted:
// MultiIndex emitted it for a mere data condition, so a legitimate partial mirror
// hard-errored with advice to compose the very thing it had composed. That was
// real. But the fix for it landed in the SAME change -- MultiIndex now reserves
// the sentinel for a genuine capability statement, emitting it only when EVERY
// source is fileless, and demoting a fileless source's answer to the weakest
// evidence otherwise. The two fixes over-corrected past each other: one made the
// signal trustworthy while the other stopped trusting it.
//
// The signal is trustworthy at every layer, not just the top one, which is what
// makes this compositional rather than lucky. A FilteredIndex over a fileless
// inner can never serve a file, so its own ErrFilesUnavailable is a true statement
// about itself; a MultiIndex above it demotes that in turn. The partial-mirror
// case now stays silent because it arrives as ErrMetadataUnavailable, not because
// this method guesses.
//
// # The refusal is TOTAL where it matters
//
// It cannot fire when the per-version loop never runs -- a package with no
// versions, or one whose versions the version-level axis already excluded. An
// earlier review read that as the guard being incomplete, and it is worth being
// precise about why it is not:
//
//	IN THOSE CASES THE FILE AXES COULD NOT HAVE CHANGED THE ANSWER.
//
// An empty result is then correct over ANY index, files or not, so there is
// nothing for a capability check to report. This is the opposite of a guard that
// fires only on favourable data: it fires exactly where it is load-bearing and is
// silent exactly where it would be noise. The invariant, stated once:
//
//	A capability failure is reported wherever a file-level axis could have
//	changed the answer, and nowhere else.
//
// TestFilteredIndexCapabilityRefusalIsTotal pins that by asserting a fileless and
// a file-serving index with identical version content give identical answers in
// the non-firing cases. If they ever diverge, the silence has become a real gap.
//
// Reaching ErrMetadataUnavailable or ErrPackageNotFound for a version the inner
// index just listed means that index disowns its own listing; RSFIndex and
// MockIndex do not, but filtering and composition are exactly where such an
// invariant stops holding, so it is handled rather than assumed away.
func (f *FilteredIndex) hasAdmissibleFile(ctx context.Context, pkg PackageName, ver version.Version) (bool, error) {
	files, err := f.inner.Files(ctx, pkg, ver)
	switch {
	case err == nil:
	case errors.Is(err, ErrFilesUnavailable):
		// Capability, not data. See the method doc.
		return false, fmt.Errorf(
			"filtered index (%s): cannot apply a file-level policy to %q: the inner index "+
				"serves no distribution files at all, so yanked status and upload time are "+
				"unknowable and no version could be admitted; compose the policy over an "+
				"index that serves files, such as a MultiIndex pairing this one with a "+
				"file-serving source: %w",
			f.policy, pkg, err)
	case errors.Is(err, ErrMetadataUnavailable), errors.Is(err, ErrPackageNotFound):
		return false, nil
	default:
		return false, err
	}

	for _, file := range files {
		if f.policy.admitsFile(file) {
			return true, nil
		}
	}
	return false, nil
}

// excluded builds the refusal for a version the policy does not admit.
//
// ErrMetadataUnavailable, never ErrPackageNotFound: the package was found.
func (f *FilteredIndex) excluded(method string, pkg PackageName, ver version.Version) error {
	return fmt.Errorf("filtered index (%s): %s(%q %s): excluded by policy: %w",
		f.policy, method, pkg, ver, ErrMetadataUnavailable)
}

// Compile-time assertion that FilteredIndex satisfies the interface.
var _ MetadataIndex = (*FilteredIndex)(nil)
