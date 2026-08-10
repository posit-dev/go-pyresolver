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
// by the shape of the data) is unsatisfiable and reports ErrFilesUnavailable
// rather than answering. See FilteredIndex.
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
// # A file-level policy needs a file-serving inner index
//
// ExcludeYanked and SnapshotDate cannot be evaluated over an index that returns
// ErrFilesUnavailable, and that error is propagated rather than resolved either
// way. Admitting everything would defeat the policy invisibly; dropping
// everything would report every package in the index as having no acceptable
// version, which reads as a constraint conflict that does not exist. Since
// RSFIndex returns ErrFilesUnavailable unconditionally -- an RSF carries no
// filename, hash, upload time, or yanked flag -- the arrangement RFD 0001 calls
// for is a file-level policy over a MultiIndex pairing the RSF with a
// file-serving source, not over the RSF alone.
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
// ⚠️ An empty result is not one answer but two, and they are kept apart. A
// release that genuinely ships no files answers empty-and-nil, because the
// interface says so -- a release can have every file deleted. A release whose
// files the POLICY removed answers ErrMetadataUnavailable, because returning
// empty there would assert something false about the release and would disagree
// with what Metadata says about the same version. Conflating a value that was
// never there with one that was removed is finding F10 of
// rstudio/package-manager#19466, in a different shape.
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
	if len(kept) == 0 && len(files) > 0 {
		return nil, f.excluded("Files", pkg, ver)
	}
	return kept, nil
}

// hasAdmissibleFile reports whether (pkg, ver) has at least one file the policy
// admits.
//
// ⚠️ ErrFilesUnavailable is propagated, not swallowed. It is the one sentinel
// about a source's CAPABILITY rather than about one package's data -- RSFIndex
// returns it for every lookup -- so treating it as "no files survive" would
// report every package in the index as having no acceptable version. That is a
// composition mistake and the error must say so. See FilteredIndex.
//
// ErrMetadataUnavailable and ErrPackageNotFound are treated as "this index will
// not speak to files for this version", so the version cannot be shown to
// satisfy a file-level policy and is dropped. Reaching either from here means
// the inner index listed a version its Files disowns; RSFIndex and MockIndex do
// not do that, but filtering and composition are exactly where such an
// invariant stops holding, so it is handled rather than assumed away.
func (f *FilteredIndex) hasAdmissibleFile(ctx context.Context, pkg PackageName, ver version.Version) (bool, error) {
	files, err := f.inner.Files(ctx, pkg, ver)
	switch {
	case err == nil:
	case errors.Is(err, ErrFilesUnavailable):
		return false, fmt.Errorf(
			"filtered index (%s): cannot apply a file-level policy to %q %s: "+
				"the inner index serves no distribution files, so yanked status and upload "+
				"time are unknowable; compose the policy over an index that serves files: %w",
			f.policy, pkg, ver, err)
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
