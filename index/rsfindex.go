// SPDX-License-Identifier: Apache-2.0 OR MIT

package index

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/posit-dev/go-python-packaging/extras"
	"github.com/posit-dev/go-python-packaging/requirement"
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
	// stored key of each PEP 440 equality class, sorted, plus the alias index
	// Metadata resolves through. Keys rather than parsed versions on purpose --
	// see Versions. One entry per package, so it is bounded by the corpus.
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
//   - versionList holds only the ORDER Versions computed -- stored keys, as
//     strings -- and every call re-parses them. It cannot hold the parsed
//     versions, because a version.Version cannot be shared between goroutines.
//     See Versions for the measurement behind that.
//
// # What this does NOT change
//
// The number of index calls. Provider.Candidates must return a count that is
// zero exactly when nothing satisfies, so it establishes usability by doing each
// version's full dependency work; that contract is what makes calls scale with
// candidate versions rather than with the closure. This makes each call cheaper
// and removes no call. See provider/provider.go.
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
// Versions needs no such copy: it re-parses, so what it returns was never in the
// memo.
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
// The remaining growth, in both caches, is one entry per package and version a
// process has genuinely been asked about, which for a long-lived server against
// the whole corpus is still the corpus. A bound is one policy over the PAIR
// rather than two, since bounding the memo alone would leave deps holding the
// same package set in raw form. It is still not needed here: every caller in
// this module creates an RSFIndex per resolve.

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

// cloneMetadata returns m with its exported slices copied and its Version taken
// from ver. See the memo notes above for why the copy is made and why it is
// shallow.
//
// ⚠️ Version is overwritten with the CALLER'S OWN value rather than served from
// the memo, and that is a concurrency requirement, not a tidiness one. A
// version.Version must not be shared between goroutines: Version.Compare pads
// the shorter release segment with append, into spare capacity a by-value copy
// shares, so two goroutines comparing two copies write to the same memory. See
// Versions for the full account. The memo would otherwise hand the FIRST
// caller's Version to every later caller on every goroutine. ver is a value the
// caller already owns, so returning it shares nothing new.
//
// The substitution is not observable, and not because the two render alike --
// they need not, since the memo is keyed by the STORED key and a caller may have
// spelled the version differently. It is not observable because there is no
// other value on offer: Metadata zeroes Version before storing, so the memo has
// never held one. Returning the caller's own value is the only thing this can
// do, and it is what buildMetadata did before the memo existed.
//
// Nothing else in PackageMetadata carries a version.Version. Requirement and
// Marker store their operands as strings and parse per call, verified against
// go-python-packaging v0.5.0, which is why the parsed requirements CAN be
// shared.
//
// # The copy policy, field by field
//
// The rule is: every EXPORTED MUTABLE slice reachable from the returned value is
// copied. Exported, because that is what a caller can reach without unsafe;
// mutable, because a string is not.
//
//   - RequiresDist -- copied. The slice a caller sorts or truncates.
//   - RequiresDist[i].Extras -- copied. ⚠️ It is exported, it is a []string, and
//     it is reachable THROUGH the copied RequiresDist, so copying the outer
//     slice alone leaves it aliased: `first.RequiresDist[i].Extras[0] = "x"`
//     corrupted the memo permanently, for every later caller, until this loop
//     existed. It is also the ONLY such slice below RequiresDist -- Specifiers
//     and Marker are unexported all the way down -- so this closes the gap
//     rather than narrowing it.
//   - ProvidesExtra -- copied. Same reasoning as RequiresDist.
//   - RequiresPython -- SHARED, deliberately. version.Specifiers wraps a
//     [][]Specifier, but the outer field and every field of a Specifier are
//     unexported, so a caller holding one has no exported path to any element:
//     it is read-only in practice for the same reason a Marker is, and copying
//     it would cost an allocation per Metadata call to defend nothing.
//   - Name, Origin, RequiresPythonRaw, RequiresPythonUnreadable -- values.
//
// Adding an exported slice to PackageMetadata, or to requirement.Requirement on
// a go-python-packaging bump, means adding a copy here.
func cloneMetadata(m PackageMetadata, ver version.Version) PackageMetadata {
	m.Version = ver

	// nil is preserved rather than normalized to an empty slice: "the record
	// declared no requirements" has always come back as a nil slice here, and a
	// caller distinguishing nil from empty must keep seeing what it saw. The
	// same holds for Requirement.Extras, which go-python-packaging documents as
	// nil when the requirement carried no "[...]" clause.
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
// # Memoized per package -- as KEYS, not as parsed versions
//
// What is memoized is the ORDER: the winning stored key of each equality class,
// already sorted and deduped. Each call still parses those keys into fresh
// version.Version values.
//
// That looks like leaving the obvious win on the table, and it is not. The sort
// is where the time went -- 0.84 s of the 0.91 s this method cost on the
// corpus's app-set entry -- because sorting n versions is O(n log n) PEP 440
// comparisons while parsing them is n parses. Memoizing the order removes the
// comparisons and keeps the parses.
//
// Metadata reads the same memo, binary-searching it to turn a caller's version
// into the stored key it names. That is not an incidental reuse: it is what
// makes "the version Versions hands out" and "the record Metadata resolves for
// it" the same choice by construction rather than by two call sites agreeing on
// preferKey. See resolveStoredKey.
//
// ⚠️ A version.Version MUST NOT BE SHARED BETWEEN GOROUTINES, so memoizing the
// parsed values is not available. Version.Compare pads the shorter operand's
// release segment with `append`, and cmpkey builds that segment by RESLICING
// away trailing zeros -- so "3.0.0" carries a Parts of len 1 and cap 3, and
// padding it back to three segments writes into spare capacity in the backing
// array rather than reallocating. A by-value copy of a Version copies the slice
// HEADER, so two goroutines comparing two copies write to the same memory.
//
// Verified, not inferred: a memo holding parsed versions makes eight concurrent
// resolutions against one shared RSFIndex fail `go test -race`, and the same
// test passes both without the memo and with the memo holding keys. The writes
// happen to store the same value at the same address, so the corruption is
// benign in practice today -- but it is a data race the Go memory model gives
// no guarantee about, and this type documents itself as safe for concurrent use.
//
// The defect is upstream, in rstudio/go-version v0.0.2 (part.Parts.Padding
// appending into shared capacity) as reached through go-python-packaging v0.5.0
// (version.Version.Compare). It is not introduced here and it is not fixable
// here: key.release is unexported, so this module cannot hand out a Version
// whose backing array it has clipped. When it is fixed upstream, memoizing the
// parsed values becomes available and recovers the remaining parse cost.
func (idx *RSFIndex) Versions(ctx context.Context, pkg PackageName) ([]version.Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	idx.memoMu.RLock()
	plan, ok := idx.versionList[pkg]
	idx.memoMu.RUnlock()
	if ok {
		return parseKeys(plan.order)
	}

	decoded, err := idx.deps(pkg)
	if err != nil {
		return nil, err
	}

	plan, parsed := computeVersionOrder(decoded)
	idx.storePlan(pkg, plan)

	// The freshly parsed values, not a re-parse of what was just stored: the
	// first call should not pay twice.
	return parsed, nil
}

// versionPlan is what Versions computed for one package, and what Metadata
// resolves a request against.
type versionPlan struct {
	// order is the winning stored key of each PEP 440 equality class, sorted
	// ascending by the parsed version and deduped, so no two elements compare
	// equal. That makes it binary-searchable with the same comparator that
	// sorted it.
	order []string

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

	plan, _ = computeVersionOrder(decoded)
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

	// Clipped to its length. computeVersionOrder sizes order for every candidate
	// and appends only the class representatives, so a package with a collapsed
	// equality class leaves spare capacity behind -- and a cached slice with
	// len < cap is the shape that lets an append by one holder overwrite what
	// another holder is reading. Nothing appends to this today; the clip is what
	// keeps that from becoming load-bearing.
	plan.order = plan.order[:len(plan.order):len(plan.order)]
	idx.versionList[pkg] = plan
}

// computeVersionOrder does the parse, sort and dedup behind both Versions and
// versionPlanFor, returning the plan and the parsed versions of its order, in
// the same order.
//
// The two returns are parallel by construction, which is what lets Versions
// serve its first call from the parsed half without re-parsing the keys it just
// stored.
func computeVersionOrder(decoded map[string]pypirsf.VersionDeps) (versionPlan, []version.Version) {
	type candidate struct {
		key       string
		parsed    version.Version
		canonical bool
	}

	candidates := make([]candidate, 0, len(decoded))
	for raw := range decoded {
		v, parseErr := version.Parse(raw)
		if parseErr != nil {
			// A version key PEP 440 rejects is skipped rather than failing the
			// package. Real corpora carry a few non-conforming keys, and one of
			// them must not make every other version unreachable.
			//
			// ⚠️ Skipping is silent HERE by design, but it must not be silent to a
			// human. When EVERY key of a package is rejected this returns an empty
			// slice, which is indistinguishable from a package for which nothing
			// was captured at all — and those are different facts. See
			// UnparseableVersionKeys, which exists so a diagnostic caller can tell
			// them apart.
			continue
		}
		candidates = append(candidates, candidate{key: raw, parsed: v, canonical: v.String() == raw})
	}

	// Sorting is not about the returned order, which the interface does not
	// promise — it is what puts the members of a PEP 440 equality class next to
	// each other so one representative can be chosen per class.
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].parsed.Equal(candidates[j].parsed) {
			return candidates[i].parsed.LessThan(candidates[j].parsed)
		}
		// Within a class, order by the same rule that picks the representative, so
		// the winner is simply the first member.
		return preferKey(candidates[i].key, candidates[i].canonical,
			candidates[j].key, candidates[j].canonical)
	})

	out := make([]version.Version, 0, len(candidates))
	plan := versionPlan{order: make([]string, 0, len(candidates))}
	for i, c := range candidates {
		if i > 0 && candidates[i-1].parsed.Equal(c.parsed) {
			// A later member of a class already represented. See the dedup note in
			// the method doc.
			continue
		}
		out = append(out, c.parsed)
		plan.order = append(plan.order, c.key)

		// Only the non-canonical winners need an alias entry: for a canonical
		// key the caller's ver.String() IS the key, and decoded resolves it
		// without help. Allocated lazily so the common package pays nothing.
		if !c.canonical {
			if plan.alias == nil {
				plan.alias = make(map[string]string)
			}
			plan.alias[c.parsed.String()] = c.key
		}
	}

	return plan, out
}

// parseKeys re-parses a memoized version order.
//
// Every key here parsed successfully when the order was built, so a failure now
// would mean version.Parse is not a function of its input. Treated as a
// programming error rather than skipped, because silently dropping a version
// would make the memoized answer differ from the first one.
//
// Always non-nil, matching what Versions returned before the memo existed: the
// slice was built with make, so a package whose every key is unparseable came
// back as an empty non-nil slice rather than nil.
func parseKeys(order []string) ([]version.Version, error) {
	out := make([]version.Version, len(order))
	for i, key := range order {
		v, err := version.Parse(key)
		if err != nil {
			return nil, fmt.Errorf("index: memoized version key %q no longer parses: %w", key, err)
		}
		out[i] = v
	}
	return out, nil
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
// Each probe is parsed fresh and discarded, which is the point: the order holds
// strings precisely because a version.Version cannot be shared between
// goroutines (see Versions), so the comparison has to re-parse. Only log2(n) of
// them are parsed.
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
	meta := PackageMetadata{Origin: idx.origin}

	if len(raw.RequiresDist) > 0 {
		meta.RequiresDist = make([]requirement.Requirement, 0, len(raw.RequiresDist))
		for _, rawReq := range raw.RequiresDist {
			req, reqErr := requirement.Parse(rawReq)
			if reqErr != nil {
				// A requirement this module cannot parse is a hard error, not a
				// skip. Dropping it silently would hand the resolver an
				// incomplete dependency set and produce a confident wrong
				// answer -- the one failure mode worth failing loudly for.
				//
				// Wrapped in ErrMetadataUnusable so a caller can CLASSIFY the
				// refusal rather than only observe that something failed. The
				// policy is unchanged: this version is still refused. What changes
				// is that a caller can now tell "this one version is unusable"
				// apart from "the index is broken", and respond in proportion --
				// a resolver by trying another version, a diagnostic traversal by
				// reporting the package and continuing. Returning an opaque error
				// forced every caller to choose between aborting and swallowing
				// everything, and the CLI chose to abort, discarding an entire
				// walk over one bad entry.
				//
				// The original parse error stays in the chain, so the specific
				// malformed string is still recoverable for diagnostics.
				return PackageMetadata{}, &unusableRecord{requirement: rawReq, cause: reqErr}
			}
			meta.RequiresDist = append(meta.RequiresDist, req)
		}
	}

	// Preserved verbatim whether or not it parses, so a caller can tell "the
	// record declared no interpreter constraint" from "the record declared one
	// we could not read". Discarding it made those two indistinguishable, and
	// the CLI reported both as "(unconstrained)" -- claiming the publisher said
	// nothing when the publisher said something unreadable.
	meta.RequiresPythonRaw = raw.RequiresPython

	if raw.RequiresPython != "" {
		specs, specErr := version.NewSpecifiers(raw.RequiresPython)
		if specErr != nil {
			// Left unconstrained rather than fatal, unlike RequiresDist. An
			// unreadable interpreter constraint over-admits a candidate, which
			// surfaces later as an install-time failure; an unreadable
			// requirement would silently under-constrain the graph and change
			// the resolution itself. pip draws the line the same way: it catches
			// InvalidSpecifier on Requires-Python and treats the candidate as
			// compatible.
			//
			// The permissiveness is recorded rather than merely applied: this is
			// a decision the decoder made, not a fact about the record, and
			// RequiresPythonUnreadable is what lets a caller say so.
			meta.RequiresPython = version.Specifiers{}
			meta.RequiresPythonUnreadable = true
		} else {
			meta.RequiresPython = specs
		}
	}

	if len(raw.ProvidesExtra) > 0 {
		meta.ProvidesExtra = make([]string, 0, len(raw.ProvidesExtra))
		for _, extra := range raw.ProvidesExtra {
			// Normalized per PEP 685 so a request for pkg[Test-Suite] matches a
			// declared "test_suite".
			meta.ProvidesExtra = append(meta.ProvidesExtra, extras.Normalize(extra))
		}
	}

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
