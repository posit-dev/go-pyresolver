// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/posit-dev/go-python-packaging/version"

	"github.com/posit-dev/go-pyresolver/pypirsf"
)

// ErrFilesUnavailable means this index cannot report distribution files at all,
// as opposed to a particular version having none.
//
// A distinct sentinel rather than reusing ErrMetadataUnavailable, because the
// two call for different handling: ErrMetadataUnavailable says "this version
// needs a build, choose another", while this says "ask a different source". A
// composing index needs to tell those apart to know whether falling back is
// even sensible.
var ErrFilesUnavailable = errors.New("distribution files unavailable from this index")

// RSFIndex is a MetadataIndex backed by a local Repository Snapshot Format file.
//
// This is the standalone path: one file on disk, no network, no database. It is
// what makes a resolution reproducible — the file is a dated artifact, so the
// same file resolves the same way forever.
//
// # Files is not served
//
// An RSF carries dependency metadata, not distribution files: there is no
// filename, hash, upload time, or yanked flag anywhere in the record. Files
// therefore always returns ErrFilesUnavailable. That is a property of the data,
// not a gap in this code, and it is why a tool built on this emits resolved
// version pins rather than download URLs. Pair it with a file-aware index, or
// hand the pins to something that knows how to fetch.
//
// # Versions reported
//
// The versions this reports are those with CAPTURED dependency metadata, not
// every version that ever existed. For resolution that is the right set: a
// version whose requirements are unknown cannot be resolved through, so
// offering it as a candidate could only produce a silently incomplete answer.
type RSFIndex struct {
	file   *pypirsf.File
	origin string

	// mu guards decoded. Lookups are otherwise concurrent, and pypirsf.File is
	// itself safe for concurrent use.
	mu      sync.RWMutex
	decoded map[PackageName]map[string]pypirsf.VersionDeps

	// memoMu guards parsed and versionList, the two PARSED caches. Separate
	// from mu because they sit above decoded and are read on paths that have
	// already consulted it: sharing one lock would mean taking it twice per
	// call, and sync.RWMutex forbids recursive read locking (a writer arriving
	// between the two RLocks deadlocks).
	memoMu sync.RWMutex
	parsed map[PackageName]map[string]memoEntry

	// versionList memoizes what Versions computed for a package: the winning
	// stored key of each PEP 440 equality class, sorted, the PARSED version of
	// each of those keys, and the alias index Metadata resolves through. One
	// entry per package, so its key set is bounded by the corpus -- but see the
	// retention note above deps for what the parsed half costs per entry.
	versionList map[PackageName]versionPlan
}

// memoEntry is one memoized Metadata outcome for a stored version key.
//
// ⚠️ The key is the STORED key, never the caller's ver.String(). That is what
// bounds this map: its key set is a subset of the package's stored keys, so it
// can never hold more entries than deps already holds, whatever a caller asks
// for. Keying it by the request instead makes it unbounded by anything the
// corpus controls -- a caller can spell a version that is PEP 440-equal to a
// stored one in unlimited ways ("1.0", "1.0.0", "1.0.0.0", ...), and can ask
// about versions that do not exist at all. Both would mint a permanent entry
// per distinct spelling. See resolveStoredKey.
//
// ErrMetadataUnusable is memoized alongside the successes because it IS a fact
// about a stored record: a record whose Requires-Dist does not parse will not
// start parsing, and a backtracking resolution asks about the same rejected
// version many times. It takes a stored key like any other outcome.
//
// Nothing else is memoized. ErrMetadataUnavailable names a version that is NOT
// in the corpus, so there is no stored key to file it under, and inventing one
// from the request is exactly the unboundedness above; resolveStoredKey answers
// it in O(log n) parses instead of the O(n) scan that made memoizing it look
// worthwhile. A decode failure or a missing package is not a fact about a
// version at all -- it comes from deps, which has its own cache and its own
// error taxonomy, and pinning a transport-shaped failure to a version key would
// outlive the condition that caused it.
type memoEntry struct {
	meta PackageMetadata

	// unusable is non-nil when this record's Requires-Dist does not parse. The
	// success and the failure are mutually exclusive, and meta is the zero value
	// whenever this is set.
	unusable *unusableRecord
}

// unusableRecord is the FACTS behind an ErrMetadataUnusable, memoized in place
// of the finished error message.
//
// ⚠️ Storing the message instead would put the FIRST caller's requested version
// into every later caller's error, because the memo is keyed by the stored key
// and several requested spellings share one entry. A caller asking about "1.0"
// would be told its request for "1.0.0" failed. Nothing request-scoped is
// memoized here for the same reason PackageMetadata.Version is stored zeroed;
// unusableErr re-renders against the version actually asked for.
type unusableRecord struct {
	// requirement is the stored Requires-Dist string that would not parse.
	requirement string
	// cause is the parse error, kept in the chain so the malformed string stays
	// recoverable for diagnostics.
	cause error
}

// NewRSFIndex wraps an open pypirsf.File.
//
// The caller retains ownership of file and is responsible for closing it; an
// index does not own the file it was handed, since one file can back several.
//
// origin labels which index answered, surfaced as PackageMetadata.Origin. Empty
// means "rsf".
func NewRSFIndex(file *pypirsf.File, origin string) (*RSFIndex, error) {
	if file == nil {
		return nil, errors.New("index: NewRSFIndex requires a non-nil file")
	}
	if origin == "" {
		origin = "rsf"
	}

	return &RSFIndex{
		file:        file,
		origin:      origin,
		decoded:     make(map[PackageName]map[string]pypirsf.VersionDeps),
		parsed:      make(map[PackageName]map[string]memoEntry),
		versionList: make(map[PackageName]versionPlan),
	}, nil
}

// deps returns the decoded dependency map for pkg, caching it.
//
// Caching matters more than it looks: a resolver calls Versions once and then
// Metadata per candidate version, and without this every one of those calls
// would re-read and re-decompress the same package blob.
//
// The cache is unbounded, which is deliberate for this shape of consumer. A
// resolution touches the packages in its closure, so the cache is bounded by the
// work actually requested rather than by the corpus. A long-lived server process
// resolving arbitrary requests would want a bound; that is not this.
//
// The returned map is shared with the cache and MUST NOT be mutated or handed
// to a caller. The exported methods copy what they return out of it.
func (idx *RSFIndex) deps(pkg PackageName) (map[string]pypirsf.VersionDeps, error) {
	idx.mu.RLock()
	cached, ok := idx.decoded[pkg]
	idx.mu.RUnlock()
	if ok {
		return cached, nil
	}

	decoded, err := idx.file.Deps(pkg.String())
	if err != nil {
		if errors.Is(err, pypirsf.ErrPackageNotFound) {
			return nil, fmt.Errorf("index %q: %q: %w", idx.origin, pkg, ErrPackageNotFound)
		}
		return nil, fmt.Errorf("index %q: %q: %w", idx.origin, pkg, err)
	}

	idx.mu.Lock()
	// Another goroutine may have decoded this in the meantime; keep whichever
	// landed first so all callers share one map and the copy-on-return
	// contract holds for a single object.
	if existing, raced := idx.decoded[pkg]; raced {
		decoded = existing
	} else {
		idx.decoded[pkg] = decoded
	}
	idx.mu.Unlock()

	return decoded, nil
}

// # The parsed memo, and why the blob cache is not enough
//
// deps caches the RAW record: every field pypirsf.VersionDeps carries is an
// unparsed published string. So a warm blob cache saves the file seek, the zstd
// decode and the varint unmarshal -- and none of those is where the time goes.
// Measured on the Phase 3 corpus (rstudio/package-manager#18651), a warm
// resolution was within 3% of a cold one on all seven entries, because Metadata
// re-parsed its PEP 508 requirements on every call (41% of resolution CPU in
// requirement.Parse alone, 100% of it reached through Metadata) and Versions
// re-parsed and re-sorted the package's whole version list on every call.
//
// Two memos below cache what deps cannot. They are deliberately NOT symmetric,
// and the asymmetry is the interesting part:
//
//   - parsed holds a whole PackageMetadata per (package, stored version key).
//     This is the step Provider.Candidates' own cost note names ("a memo keyed
//     by (package, version) is the obvious next step if this ever shows up in a
//     profile") and the reason PackageMetadata was defined to hold parsed
//     requirements at all (types.go: "re-parsing per candidate during resolution
//     is pure waste").
//
//   - versionList holds the ORDER Versions computed -- the stored keys -- AND
//     the parsed version of each. It held keys alone until go-python-packaging
//     v0.6.0, because a version.Version could not then be shared between
//     goroutines; that constraint is gone and the parsed half is memoized. See
//     Versions for the measurement, and the retention note below for the cost.
//
// # What this does NOT change
//
// The number of index calls. This memo makes each call cheaper and removes no
// call.
//
// ⚠️ HISTORICAL, as of the found/rank change: when this was written,
// Provider.Candidates returned an exact count and so did each in-range version's
// full dependency work, which is what made calls scale with candidate versions
// rather than with the closure. That is what the paragraph above was measured
// against. Candidates now answers existence and stops at the first usable version,
// so the call counts recorded here and in resolver/bench_test.go's tables are the
// OLD ones — see the CHANGELOG for the current figures. The point this note exists
// to make is unaffected: a memo changes the cost per call and never the number of
// them.
//
// # Metadata's slices are copied, deliberately
//
// Metadata hands back a copy of RequiresDist and ProvidesExtra rather than the
// cached slices. Sharing would be marginally cheaper and it would be a silent
// breaking change: before the memo existed every call built a fresh slice, so a
// caller has always been free to sort or overwrite what it was given, and this
// module has such a caller -- cmd/pyresolve's `versions` subcommand sorts the
// result of Versions in place. Under a shared memo that call corrupts the cache
// for every later caller, with nothing at the mutation site to suggest it.
// index/mock.go copies for the same reason, and the two implementations agreeing
// is what lets a mock-backed test detect a caller that mutates.
//
// The copy is a memmove of already-parsed values; it does not re-run a parse,
// which is the cost this exists to remove. Its protection is bounded, and
// honestly so -- but it reaches every exported mutable slice under a
// PackageMetadata, which is the copy policy cloneMetadata sets out field by
// field. Below RequiresDist the only such slice is Requirement.Extras; a
// requirement's specifiers and marker tree are unexported all the way down and
// unreachable without unsafe. A caller with unsafe can still reach anything, and
// a deep copy of a parsed requirement graph would cost more than the parse it
// replaces.
//
// Versions makes the same copy, and for the same caller. It used to need none,
// because it re-parsed and so what it returned was never in the memo; now that
// the memo holds the parsed values, handing back plan.versions would let
// cmd/pyresolve's `versions` subcommand sort the cache.
//
// It is a memmove of already-parsed values, and its cost is measured rather than
// waved away -- see the CHANGELOG for the warm figures against a variant that
// hands back the memo's own slice.
//
// ⚠️ It is REDUNDANT on the RESOLUTION path specifically, and saying more than
// that would be overclaiming. provider.Provider passes what it gets straight into
// candidate.Rank, which copies unconditionally and never writes to its argument,
// so a resolution copies twice. The copy is there for the EXPORTED contract:
// cmd/pyresolve's `versions` subcommand, and whatever an external consumer does
// with a slice an exported method handed it.
//
// ⚠️ AN INTERNAL NO-COPY ACCESSOR IS NOT THEREFORE FREE, and an earlier draft of
// this note said it was, on the strength of "provider is the only library-side
// caller". It is not the only one. FilteredIndex.Versions and MultiIndex.Versions
// both call it, and FilteredIndex has a PASS-THROUGH fast path: with a policy
// that filters neither pre-releases nor files it returns the inner index's slice
// by reference (filtered.go). Wiring a no-copy accessor through it would hand the
// memo's own slice to an arbitrary external caller -- the exact silent breaking
// change this copy exists to prevent. MultiIndex is safe because it always
// rebuilds. Whoever takes that optimization owns FilteredIndex's fast path first.
//
// # Bounded by the corpus, like the blob cache
//
// Neither memo is bounded by a policy -- no cap, no eviction -- and both are
// bounded by the same thing deps is: the corpus. That is a property of the KEYS,
// not of well-behaved callers, and it is the reason both memos are safe to leave
// uncapped.
//
//   - versionList is keyed by package. One entry per package deps decoded.
//   - parsed is keyed by (package, STORED version key). Its key set is a subset
//     of the keys deps already holds for that package.
//
// So a caller cannot make either memo hold an entry that does not correspond to
// something in the file, however many lookups it issues and whatever it asks
// for. What a memo adds is the CONSTANT: parsed requirements alongside the raw
// strings, for the subset of (package, version) actually asked about.
//
// ⚠️ A BOUNDED KEY SET IS NOT BOUNDED MEMORY, and versionList's parsed half is
// where that distinction stops being academic. It holds a version.Version per
// stored key where it used to hold a string header, and a version.Version is a
// large struct. Measured rather than estimated, as the live heap one warmed index
// keeps alive after a resolution finishes (resolver/peak_heap_test.go,
// TestIndexRetainedHeapAfterResolve, medians of five interleaved rounds against
// the production snapshot): app-set 0.35 MB -> 0.99 MB, wide-versions 1.31 MB ->
// 4.41 MB. Roughly triple, on closures of seven to eighteen packages.
//
// For a CLI that builds an index per resolve and drops it, a few megabytes that
// never accumulate is a rounding error. For a long-lived server holding one index
// over the whole corpus it is not: a production PyPI snapshot carries 932,861
// packages, so "bounded by the corpus" must not be read as "small".
//
// ⚠️ And the retention is not paid only by callers who benefit from it.
// versionPlanFor builds the same plan on the Metadata path, so a consumer that
// calls Metadata and never calls Versions pays the full increase and gets none of
// the speedup -- findEqualKey still re-parses plan.order rather than probing
// plan.versions. Taking that follow-up would close the gap; until then it is a
// real asymmetry and it lands on exactly the long-lived consumer this note is
// about.
//
// ⚠️ This did not come for free, and the earlier draft of this change did NOT
// have the property. Keying parsed by the request's ver.String() -- the obvious
// key -- made the memo unbounded by anything the corpus controls: 20,000
// lookups of nonexistent versions of one package left 20,000 permanent entries
// while deps stayed at 4, because deps does not cache a package it could not
// find but the memo cached the miss. Alias spellings did the same to the
// successes. See memoEntry and resolveStoredKey for how the key was fixed, and
// note which consumer that mattered for: a CLI resolve, which builds an index
// and drops it, could not have noticed. A long-lived server accepting arbitrary
// requests -- the exact consumer deps' own note says is out of scope -- would
// have grown until it was restarted.
//
// The remaining growth, in all three caches, is one entry per package and
// version a process has genuinely been asked about, which for a long-lived
// server against the whole corpus is still the corpus. A bound is one policy over
// the SET rather than three, since bounding one would leave the others holding
// the same package set in another form.
//
// ⚠️ It is not needed by any caller IN THIS MODULE -- each creates an RSFIndex
// per resolve -- and that sentence has now been true of a claim that was
// nonetheless wrong for a server twice over (see the ver.String() keying above).
// So, plainly: the parsed-version memo MULTIPLIES the per-package retention, and
// a bound is a prerequisite for embedding this index in a long-lived server
// process, not an optimization to consider later. Whoever does that integration
// owns it before the first resolve, not after the first out-of-memory.

// lookupMetadata reads the parsed memo. key is a STORED version key, not a
// caller's rendering -- see memoEntry. ok is false when nothing is memoized for
// this (package, key); a memoized failure is ok with a non-nil err.
func (idx *RSFIndex) lookupMetadata(pkg PackageName, key string) (memoEntry, bool) {
	idx.memoMu.RLock()
	defer idx.memoMu.RUnlock()

	byVersion, ok := idx.parsed[pkg]
	if !ok {
		return memoEntry{}, false
	}
	entry, ok := byVersion[key]
	return entry, ok
}

// storeMetadata records a parsed outcome under a STORED version key. Last writer
// wins, which is safe because the outcome is a pure function of the cached blob
// and the key: two goroutines racing compute the same answer.
func (idx *RSFIndex) storeMetadata(pkg PackageName, key string, entry memoEntry) {
	idx.memoMu.Lock()
	defer idx.memoMu.Unlock()

	byVersion, ok := idx.parsed[pkg]
	if !ok {
		byVersion = make(map[string]memoEntry)
		idx.parsed[pkg] = byVersion
	}
	byVersion[key] = entry
}

// cloneMetadata returns m with its Version taken from ver and its exported
// slices copied. See the memo notes above for why the copy is made.
//
// The copy itself is PackageMetadata.Clone, which owns the field-by-field policy
// and the maintenance contract that comes with it. Calling it rather than
// repeating it here is what keeps the memo's copy and the copy an external
// consumer applies from drifting apart.
//
// Version is overwritten with the CALLER'S OWN value rather than served from the
// memo. That WAS a concurrency requirement: under go-python-packaging v0.5.0 and
// earlier a version.Version could not be shared between goroutines at all, so
// handing the first caller's Version to every later caller was a data race. It is
// no longer one -- v0.6.0 pads into a fresh slice, and Versions now memoizes
// parsed versions on the strength of that -- so this substitution survives on its
// SECOND reason alone, which is the one that was always sufficient.
//
// That reason: there is no other value on offer. Metadata zeroes Version before
// storing, so the memo has never held one. Returning the caller's own value is
// the only thing this can do, and it is what buildMetadata did before the memo
// existed. Nor is the substitution observable through a rendering difference --
// the memo is keyed by the STORED key and a caller may have spelled the version
// differently, so the two need not render alike.
//
// ⚠️ Do not now "simplify" this by storing the first caller's Version and serving
// it: that is safe from a data-race standpoint today, and it would still tell the
// second caller a different version string than it asked about. See unusableErr,
// which declines to memoize a finished message for exactly the same reason.
func cloneMetadata(m PackageMetadata, ver version.Version) PackageMetadata {
	m.Version = ver
	return m.Clone()
}

// Versions implements MetadataIndex.
//
// # PEP 440-equal keys are collapsed to one version
//
// The producer records whatever version string a publisher used, so one package
// can carry both "1.0" and "1.0.0" as separate stored keys. Those are the SAME
// version under PEP 440, and returning both hands a resolver two candidates it
// cannot tell apart: they compare equal, so no constraint can select between
// them, and the choice falls to whatever order the caller happens to iterate.
//
// Worse, the two stored records can disagree about dependencies — measured on a
// production snapshot as 59 equality classes across 56 packages, 10 of which
// disagree. A resolver offered both would produce a different dependency graph
// depending on which it picked, with nothing in the data to justify either.
//
// So one representative is returned per equality class, chosen by preferKey.
// Metadata uses that same function, which is what makes the pair coherent: the
// version handed out here resolves to the record dedup treated as authoritative.
// It does NOT make the underlying data unambiguous; which spelling the publisher
// meant is unknowable from the snapshot, and a caller still cannot detect that a
// class was collapsed.
//
// # Memoized per package -- the order AND the parsed versions
//
// What is memoized is the ORDER (the winning stored key of each equality class,
// already sorted and deduped) and the PARSED version of each of those keys. A
// call after the first neither sorts nor parses; it copies.
//
// Memoizing the order came first and bought the larger share: sorting n versions
// is O(n log n) PEP 440 comparisons while parsing them is n parses, and the sort
// was 0.84 s of the 0.91 s this method cost on the corpus's app-set entry. What
// remained was the n parses, and by 0.7.0 they were most of what was left:
// profiled as a share of resolver.Resolve's own cumulative cost, re-parsing the
// stored keys was 22.7% of a warm app-set resolution and 69.1% of a warm
// wide-versions one, and 58.4% and 94.8% of the objects those resolutions
// allocated. Memoizing them is worth 1.37x and 3.23x end to end.
//
// ⚠️ Those shares are of THIS tree. The same memo measured against 0.6.0 was
// 1.24x and 2.27x, because 0.7.0's pep440set change shrank the denominator rather
// than the parse. A percentage-of-Resolve figure is a fact about one base; see
// the CHANGELOG, which records both and why they differ.
//
// ⚠️ THIS COULD NOT BE DONE BEFORE go-python-packaging v0.6.0, and the reason is
// worth keeping because it is the reason the memo has the shape it has. Under
// v0.5.0 and earlier a version.Version could not be shared between goroutines at
// all: Version.Compare padded the shorter operand's release segment with
// `append`, and cmpkey built that segment by RESLICING away trailing zeros, so
// "3.0.0" carried a Parts of len 1 and cap 3 and padding it back wrote into spare
// capacity in the backing array. A by-value copy of a Version copies the slice
// HEADER, so two goroutines comparing two copies wrote to the same memory. v0.6.0
// removes that two ways over: a packable version never touches part.Parts at all,
// and the fallback pads by copying. The defect was upstream, in rstudio/go-version
// v0.0.2, and was never fixable here -- key.release is unexported.
//
// So the parsed values are safe to share, and this memo shares them WITHIN the
// index. It does not hand them out: see the copy below.
//
// ⚠️ The returned slice is a COPY of the memo's, and that is not defensive
// tidiness. Before this memo existed every call built a fresh slice, so a caller
// has always been free to sort what it was given, and this module has such a
// caller -- cmd/pyresolve's `versions` subcommand sorts the result in place.
// Returning plan.versions would make that call reorder the cache for every later
// caller, permanently, with nothing at the mutation site to suggest it: a silent
// breaking change dressed as an optimization. Its cost was measured rather than
// waved away -- see the copy note above deps -- and it is inside the figures
// quoted here.
//
// Metadata reads the same memo, binary-searching plan.order to turn a caller's
// version into the stored key it names. That is not an incidental reuse: it is
// what makes "the version Versions hands out" and "the record Metadata resolves
// for it" the same choice by construction rather than by two call sites agreeing
// on preferKey. See resolveStoredKey.
//
// ⚠️ The parsed half is RETENTION, not just churn traded away, and it is retained
// for the life of the index rather than of a resolution. See the retention note
// above deps: for this module's callers, which build an index per resolve, it is
// a rounding error; for a long-lived server it is the thing that needs a bound
// before the integration, not after it.
func (idx *RSFIndex) Versions(ctx context.Context, pkg PackageName) ([]version.Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	idx.memoMu.RLock()
	plan, ok := idx.versionList[pkg]
	idx.memoMu.RUnlock()
	if !ok {
		decoded, err := idx.deps(pkg)
		if err != nil {
			return nil, err
		}
		plan = computeVersionOrder(decoded)
		idx.storePlan(pkg, plan)
	}

	// Never plan.versions itself. See the copy note above -- and note that the
	// FIRST call copies too: it holds the same slice it just stored, so returning
	// it directly would leave exactly one caller per package able to corrupt the
	// memo, which is the worst of both arrangements to debug.
	//
	// make, not slices.Clone: a package whose every key is unparseable must come
	// back as an empty NON-NIL slice, which is what it returned before any memo
	// existed, and slices.Clone propagates nil.
	out := make([]version.Version, len(plan.versions))
	copy(out, plan.versions)
	return out, nil
}

// versionPlan is what Versions computed for one package, and what Metadata
// resolves a request against.
type versionPlan struct {
	// order is the winning stored key of each PEP 440 equality class, sorted
	// ascending by the parsed version and deduped, so no two elements compare
	// equal. That makes it binary-searchable with the same comparator that
	// sorted it.
	order []string

	// versions is order, parsed, element for element: versions[i] is
	// version.Parse(order[i]). Same length, same order, always.
	//
	// ⚠️ SHARED, and only inside the index. Versions returns a copy; nothing here
	// or in Metadata writes to it. Sharing it at all became legal in
	// go-python-packaging v0.6.0 and would have been a data race before -- see
	// Versions.
	versions []version.Version

	// alias maps a class winner's CANONICAL RENDERING to its stored key, for the
	// classes where the two differ. Nil when every winner is already spelled
	// canonically, which is most packages.
	//
	// ⚠️ This is what keeps the common Metadata call O(1). Versions hands out
	// parsed versions, so a caller's ver.String() is a canonical rendering --
	// and when the stored key is NOT canonical, the direct decoded[ver.String()]
	// lookup misses every single time and falls through to the binary search.
	// certifi is the shape that made this show up: zero-padded calendar keys
	// like "2015.04.28" render as "2015.4.28", so every one of the resolution's
	// 131 Metadata calls paid ~7 parses. Measured as roughly 2 microseconds and
	// 7 allocations per call before this map existed.
	//
	// It costs one map entry per non-canonical winner, so it is bounded by the
	// corpus like everything else here, and it is empty for the packages that do
	// not need it.
	alias map[string]string
}

// versionPlanFor returns pkg's memoized plan, computing and storing it on the
// first ask. decoded must be pkg's blob map, already obtained from deps --
// passed in rather than fetched here so this never takes mu while holding
// memoMu.
func (idx *RSFIndex) versionPlanFor(pkg PackageName, decoded map[string]pypirsf.VersionDeps) versionPlan {
	idx.memoMu.RLock()
	plan, ok := idx.versionList[pkg]
	idx.memoMu.RUnlock()
	if ok {
		return plan
	}

	plan = computeVersionOrder(decoded)
	idx.storePlan(pkg, plan)
	return plan
}

// storePlan records a package's version plan. The single store site, so the clip
// below is the single place the memo's capacity is established.
//
// Last writer wins. Two goroutines racing here computed the same plan from the
// same keys, so which one lands does not matter -- unlike deps, where
// first-writer-wins exists so every caller shares ONE map object. The plan is
// safe to share because nothing in the resolution path writes to one.
func (idx *RSFIndex) storePlan(pkg PackageName, plan versionPlan) {
	idx.memoMu.Lock()
	defer idx.memoMu.Unlock()

	// Clipped to their length. computeVersionOrder sizes both slices for every
	// candidate and appends only the class representatives, so a package with a
	// collapsed equality class leaves spare capacity behind -- and a cached slice
	// with len < cap is the shape that lets an append by one holder overwrite
	// what another holder is reading. Nothing appends to either today; the clip
	// is what keeps that from becoming load-bearing.
	//
	// ⚠️ The clip does NOT reclaim the spare capacity -- the backing array is
	// unchanged and only a later append would reallocate. It is not a retention
	// measure and should not be read as one; equality classes collapse in 59
	// classes across the whole production snapshot, so there is nothing there to
	// reclaim. What it buys is that an append by one holder cannot grow IN PLACE
	// into memory another holder is reading.
	plan.order = plan.order[:len(plan.order):len(plan.order)]
	plan.versions = plan.versions[:len(plan.versions):len(plan.versions)]
	idx.versionList[pkg] = plan
}

// computeVersionOrder does the parse, sort and dedup behind both Versions and
// versionPlanFor.
//
// plan.order and plan.versions are filled in one pass and are parallel by
// construction, which is the invariant everything above relies on: it is what
// lets Versions serve from the parsed half while resolveStoredKey binary-searches
// the key half, without either needing to check that the two still agree. Keep
// the two appends adjacent.
func computeVersionOrder(decoded map[string]pypirsf.VersionDeps) versionPlan {
	keys := make([]string, 0, len(decoded))
	for raw := range decoded {
		keys = append(keys, raw)
	}

	// The dedup rule, the sort and the skip-unparseable policy all live in
	// DedupeEqualityClasses, which is exported so a consumer building its own
	// index over the same bytes shares them instead of re-deriving them. Calling
	// it here is what keeps the exported rule and the rule this index's own tests
	// exercise the same rule.
	classes := DedupeEqualityClasses(keys)

	// Both non-nil even for a package whose every key is unparseable: Versions has
	// always answered that with an empty slice rather than a nil one.
	plan := versionPlan{
		order:    make([]string, 0, len(classes)),
		versions: make([]version.Version, 0, len(classes)),
	}
	for _, c := range classes {
		plan.order = append(plan.order, c.Key)
		plan.versions = append(plan.versions, c.Version)

		// Only the non-canonical winners need an alias entry: for a canonical
		// key the caller's ver.String() IS the key, and decoded resolves it
		// without help. Allocated lazily so the common package pays nothing.
		if !c.Canonical() {
			if plan.alias == nil {
				plan.alias = make(map[string]string)
			}
			plan.alias[c.Version.String()] = c.Key
		}
	}

	return plan
}

// UnparseableVersionKeys returns the stored version keys for pkg that PEP 440
// rejects, sorted. It is empty when every key parses.
//
// # Why this exists
//
// Versions skips a key it cannot parse, which is the right behaviour for a
// resolver: a few non-conforming keys are normal in a real corpus and one of them
// must not make every other version of that package unreachable.
//
// ⚠️ But when EVERY key of a package is rejected, Versions returns an empty slice,
// and that is indistinguishable from a package for which nothing was captured at
// all. Those are different facts and they call for different responses. Reporting
// the second when the first is true sends someone looking for missing data that is
// actually present, just recorded under a string the specification does not
// accept — the snapshot holds `holygrail` with one key, "0.2.1.Perceval", carrying
// a real dependency on sqlobject.
//
// Deliberately NOT on the MetadataIndex interface. A resolver has no use for it;
// it exists for diagnostics, and widening the resolver seam for a reporting
// concern would oblige every implementation to answer a question none of them are
// asked.
func (idx *RSFIndex) UnparseableVersionKeys(ctx context.Context, pkg PackageName) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	decoded, err := idx.deps(pkg)
	if err != nil {
		return nil, err
	}

	var bad []string
	for raw := range decoded {
		if _, parseErr := version.Parse(raw); parseErr != nil {
			bad = append(bad, raw)
		}
	}
	sort.Strings(bad)
	return bad, nil
}

// preferKey reports whether key a is the better representative of a PEP 440
// equality class than key b.
//
// Canonical spellings win, then the lexicographically smallest. It is applied in
// exactly ONE place -- computeVersionOrder, which uses it to pick the winner of
// each class -- and Metadata reaches that decision by searching the resulting
// order rather than by re-running the rule. That is what makes the pair
// coherent: the spelling Versions hands out and the record Metadata resolves for
// it come from the same computation, not from two call sites that agree only as
// long as nobody edits one of them.
//
// canonical means the key round-trips through PEP 440 normalization, which is the
// best available evidence of what the publisher actually wrote. The lexicographic
// tail exists only to make the outcome total, since two non-canonical spellings
// can both compare equal.
func preferKey(a string, aCanonical bool, b string, bCanonical bool) bool {
	if aCanonical != bCanonical {
		return aCanonical
	}
	return a < b
}

// resolveStoredKey maps a caller's version onto the stored key whose record
// answers for it. ok is false when the package carries no such record, which is
// what Metadata reports as ErrMetadataUnavailable.
//
// # Three steps, cheapest first
//
//  1. An exact hit on a stored key. The producer writes whatever version string
//     the publisher used, so most requests name a key verbatim.
//  2. The plan's alias index, which names the stored key of every class whose
//     winner is spelled non-canonically. This is the step that covers a version
//     obtained from Versions, since Versions hands out parsed values and their
//     rendering is canonical by definition. Without it, a package with
//     zero-padded calendar keys pays step 3 on EVERY call -- see
//     versionPlan.alias for the measurement.
//  3. A binary search over the plan's order, for a request PEP 440-equal to a
//     stored key but spelled like neither -- "1.0.0.0" against a stored "1.0" --
//     and for a version that does not exist at all.
//
// ⚠️ MORE THAN ONE KEY CAN QUALIFY at steps 2 and 3, so the choice must not be
// made by map iteration order. Go randomizes it, and a package carrying two
// equal-comparing spellings with different dependencies then answers differently
// from one call to the next on the same index -- measured on a production
// snapshot as 500 calls returning two distinct results. That is a wrong answer
// delivered with total confidence, and it falsifies this type's documented
// guarantee that the same file resolves the same way forever.
//
// Both steps answer from the plan, which holds one winner per equality class
// chosen by preferKey. So the choice is deterministic because it was already
// made, once, for the whole package -- not because two call sites apply the same
// rule and are trusted to keep agreeing.
//
// Step 3 is a BINARY search, and that is load-bearing rather than tidy. It used
// to be a linear scan that parsed every stored key -- for a package carrying ten
// thousand releases, the most expensive thing Metadata could do, and the reason
// memoizing its outcome looked worthwhile. It was not: the only key available to
// file that outcome under is the caller's, which nothing bounds (see memoEntry).
// Searching a sorted order costs O(log n) parses instead of O(n), ~14 against
// 10,000 on that package, which makes the miss cheap enough not to want caching.
//
// Correctness of the search rests on plan.order being sorted by
// version.Version.Compare and deduped under it -- the same total order this
// probes with -- which computeVersionOrder establishes.
func (idx *RSFIndex) resolveStoredKey(
	pkg PackageName, ver version.Version, decoded map[string]pypirsf.VersionDeps,
) (string, bool, error) {
	key := ver.String()
	if _, ok := decoded[key]; ok {
		return key, true, nil
	}

	plan := idx.versionPlanFor(pkg, decoded)
	if stored, ok := plan.alias[key]; ok {
		return stored, true, nil
	}
	return findEqualKey(plan.order, ver)
}

// findEqualKey binary-searches a sorted, deduped version order for the key whose
// version is PEP 440-equal to ver.
//
// Each probe is parsed fresh and discarded. That USED to be forced -- the order
// held strings because a version.Version could not be shared between goroutines
// -- and it no longer is: plan.versions now holds the parsed value of every
// element of plan.order, at the same index, so this search could probe those
// directly and parse nothing.
//
// ⚠️ It deliberately does not, YET. Only log2(n) probes are parsed -- about 14
// against a package with ten thousand releases -- so this is not where the parse
// cost was, and folding it into the parsed-version memo's change would have
// confounded that memo's measurement with a second effect. It is a real follow-up
// with a real (small) win, not an oversight. Whoever takes it: pass the plan
// rather than the order, and the error return goes away with the parse.
//
// A parse failure here would mean version.Parse is not a function of its input,
// since every key in the order parsed when the order was built. Reported rather
// than treated as "not equal", because silently continuing would answer
// "unavailable" for a version that is present.
func findEqualKey(order []string, ver version.Version) (string, bool, error) {
	lo, hi := 0, len(order)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		probe, err := version.Parse(order[mid])
		if err != nil {
			return "", false, fmt.Errorf("index: memoized version key %q no longer parses: %w", order[mid], err)
		}
		switch {
		case probe.Equal(ver):
			return order[mid], true, nil
		case probe.LessThan(ver):
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return "", false, nil
}

// Metadata implements MetadataIndex.
//
// # Memoized per (package, stored version key)
//
// The parse below is memoized. See the memo notes above deps for what that buys,
// what it costs, and why the returned slices are copies.
//
// ⚠️ The memo key is the STORED key resolveStoredKey mapped the request onto,
// NOT the request's own rendering. The distinction is what keeps the memo
// bounded by the file rather than by what callers ask for -- "1.0", "1.0.0" and
// "1.0.0.0" are three requests naming one record, and a version that does not
// exist names none. See memoEntry.
//
// The consequence worth stating plainly: a request that resolves to nothing is
// never memoized, so ErrMetadataUnavailable is recomputed every time. That is
// affordable because resolveStoredKey answers it with a binary search rather
// than a scan.
//
// ⚠️ "1.0" and "1.0.0" collapsing onto one memo entry is NOT the same as their
// collapsing onto one answer, and it would be wrong to describe it that way.
// When both spellings are stored keys they are separate records that may carry
// different requirements -- the trimmed fixture holds `database-connector` under
// both spellings, with contradictory dependencies, which
// TestRealFileAwkwardShapes pins -- and each request hits its own record
// directly, exactly as it did before the memo existed. They share an entry only
// when they share a record, which is what makes the memo unable to disagree with
// itself: the key IS the record. Versions still reports one of the two, so a
// resolver never has to choose.
func (idx *RSFIndex) Metadata(ctx context.Context, pkg PackageName, ver version.Version) (PackageMetadata, error) {
	if err := ctx.Err(); err != nil {
		return PackageMetadata{}, err
	}

	if err := checkVersionInitialized("Metadata", pkg, ver); err != nil {
		return PackageMetadata{}, err
	}

	decoded, err := idx.deps(pkg)
	if err != nil {
		// Deliberately not memoized: deps could not produce a record at all, so
		// this says nothing about the version. See memoEntry.
		return PackageMetadata{}, err
	}

	key, found, err := idx.resolveStoredKey(pkg, ver, decoded)
	if err != nil {
		return PackageMetadata{}, err
	}
	if !found {
		// The package exists but this version has no captured metadata.
		// Unavailable rather than not-found: reporting not-found would invite a
		// resolver to treat it as a typo and give up on a package that is
		// genuinely present.
		return PackageMetadata{}, fmt.Errorf("index %q: %q %s: %w",
			idx.origin, pkg, ver, ErrMetadataUnavailable)
	}

	if entry, ok := idx.lookupMetadata(pkg, key); ok {
		if entry.unusable != nil {
			return PackageMetadata{}, idx.unusableErr(pkg, ver, entry.unusable)
		}
		return cloneMetadata(entry.meta, ver), nil
	}

	meta, unusable := idx.buildMetadata(decoded[key])
	meta.Name = pkg

	// Version is never set on the stored value. cloneMetadata always fills it
	// from the caller's own ver, so a memoized Version could only ever be a
	// version.Version shared between goroutines -- which is exactly what must not
	// happen. Leaving it zero makes that structural rather than a rule to
	// remember. Name is a string, carries no such hazard, and is a fact about
	// where the record lives, so it is memoized like any other field.
	idx.storeMetadata(pkg, key, memoEntry{meta: meta, unusable: unusable})

	if unusable != nil {
		return PackageMetadata{}, idx.unusableErr(pkg, ver, unusable)
	}
	// Cloned on the miss path too, so the value a caller gets never aliases the
	// memo -- cold and warm hand back the same kind of thing.
	return cloneMetadata(meta, ver), nil
}

// unusableErr renders a memoized unusable record against the version the CALLER
// asked for.
//
// ⚠️ Formatted per call rather than memoized as a finished message, for the same
// reason cloneMetadata takes Version from the caller: the memo is keyed by the
// STORED key, so several requested spellings share one entry, and a stored
// message would name whichever spelling happened to arrive first. A caller
// asking for "1.0" would be told its request for "1.0.0" failed, which is a
// falsehood aimed squarely at whoever is reading the log to work out what they
// asked for. The unusable record holds the FACTS -- which requirement string,
// and why it would not parse -- and only those are memoized.
func (idx *RSFIndex) unusableErr(pkg PackageName, ver version.Version, u *unusableRecord) error {
	return fmt.Errorf("index %q: %q %s: parsing requirement %q: %w: %w",
		idx.origin, pkg, ver, u.requirement, ErrMetadataUnusable, u.cause)
}

// buildMetadata parses one stored record into PackageMetadata.
//
// Split out of Metadata so the memo wraps exactly the work worth memoizing.
// Everything here is a pure function of raw ALONE -- which is why it takes
// neither the package nor the version, though it once did: anything derived from
// the request has no business in a value filed under a stored key. Metadata
// fills Name, Version and Origin, and unusableErr names the version.
//
// ⚠️ The memoized failure case is here and not in Metadata: a record whose
// Requires-Dist does not parse is a fact about that stored record, so it belongs
// under that record's key. It comes back as facts rather than as a message; see
// unusableRecord.
func (idx *RSFIndex) buildMetadata(raw pypirsf.VersionDeps) (PackageMetadata, *unusableRecord) {
	meta, err := ParseRecord(raw.RequiresDist, raw.RequiresPython, raw.ProvidesExtra)
	if err != nil {
		// Reduced back to facts for the memo. ParseRecord's error already carries
		// only the requirement and the cause -- no version -- but unusableRecord is
		// what the memo stores and unusableErr is what renders the message against
		// the version actually asked about.
		var unparseable *UnparseableRequirementError
		if !errors.As(err, &unparseable) {
			// ParseRecord documents this as its only error. A different one would
			// mean the contract changed underneath us, and silently treating it as
			// a bad requirement would misreport what happened.
			return PackageMetadata{}, &unusableRecord{requirement: "", cause: err}
		}
		return PackageMetadata{}, &unusableRecord{requirement: unparseable.Requirement, cause: unparseable.Err}
	}
	meta.Origin = idx.origin
	return meta, nil
}

// Files implements MetadataIndex by always reporting ErrFilesUnavailable.
//
// See the type documentation: an RSF carries no filename, hash, upload time, or
// yanked flag, so there is nothing to report. This is the data's shape, not a
// missing feature.
func (idx *RSFIndex) Files(_ context.Context, pkg PackageName, ver version.Version) ([]DistFile, error) {
	if err := checkVersionInitialized("Files", pkg, ver); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("index %q: %q %s: an RSF carries dependency metadata only: %w",
		idx.origin, pkg, ver, ErrFilesUnavailable)
}

// checkVersionInitialized rejects an uninitialized version.Version.
//
// This is a CALLER BUG, not a state of the data, so it is deliberately not one
// of the four sentinels: there is nothing to branch on, only code to fix. Left
// unguarded it would collapse into ErrMetadataUnavailable -- "no metadata for
// that version" -- which blames the RSF for the caller passing a zero value, the
// same class of state collapse this package has already had to fix four times.
//
// The empty rendering is a sound test because go-python-packaging guarantees no
// version Parse accepts renders as "" (the PEP 440 grammar requires a release
// segment, and gpp asserts this), so "" means an uninitialized Version and
// nothing else.
//
// ⚠️ Before gpp v0.3.1 this could not even be checked this way: Version.String()
// PANICKED on a zero value rather than returning "". Metadata crashed on
// decoded[ver.String()], while Files looked healthy because fmt recovers a panic
// raised inside a String method and substitutes "%!s(PANIC=...)" -- which is why
// rstudio/package-manager#19466's F14 reported this against Files, the one call
// site that did not crash.
func checkVersionInitialized(method string, pkg PackageName, ver version.Version) error {
	if ver.String() != "" {
		return nil
	}
	return fmt.Errorf("%s(%q): version is uninitialized (the zero value); "+
		"pass a version obtained from Versions or version.Parse", method, pkg)
}

// Len reports how many packages the underlying file carries.
func (idx *RSFIndex) Len() int { return idx.file.Len() }

// Packages returns every canonical name in the underlying file, sorted.
func (idx *RSFIndex) Packages() []string { return idx.file.Packages() }

// Compile-time assertion that RSFIndex satisfies the interface.
var _ MetadataIndex = (*RSFIndex)(nil)
