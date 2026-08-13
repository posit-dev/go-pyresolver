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

	// versionList memoizes the ORDER Versions computed for a package: the
	// winning stored key of each PEP 440 equality class, sorted. Keys rather
	// than parsed versions on purpose -- see Versions.
	versionList map[PackageName][]string
}

// memoEntry is one memoized Metadata outcome for a (package, version).
//
// The error is memoized alongside the value, not only the success. Both
// ErrMetadataUnavailable and ErrMetadataUnusable are FACTS ABOUT THE RECORD --
// deterministic functions of the decoded blob and the requested version -- so
// recomputing them yields the same answer at the same cost. That cost is not
// small: the unavailable path scans every stored key of the package and parses
// each one, which for a package carrying ten thousand releases is the most
// expensive thing Metadata can do, and a backtracking resolution asks for the
// same missing version repeatedly.
//
// Errors that are NOT facts about the record -- a decode failure, a missing
// package -- are deliberately not memoized here. They come from deps, which has
// its own cache and its own error taxonomy, and pinning a transport-shaped
// failure to a (package, version) key would outlive the condition that caused
// it.
type memoEntry struct {
	meta PackageMetadata
	err  error
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
		versionList: make(map[PackageName][]string),
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
//   - parsed holds a whole PackageMetadata per (package, version). This is the
//     step Provider.Candidates' own cost note names ("a memo keyed by (package,
//     version) is the obvious next step if this ever shows up in a profile") and
//     the reason PackageMetadata was defined to hold parsed requirements at all
//     (types.go: "re-parsing per candidate during resolution is pure waste").
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
// which is the cost this exists to remove. Its protection is shallow, and
// honestly so: requirement.Requirement holds its own slices and marker tree, so
// a caller determined to reach inside one still can. A deep copy of a parsed
// requirement graph would cost more than the parse it replaces. The copy defends
// the memo's own structure -- reordering, truncation, element assignment --
// which is what a real caller does.
//
// Versions needs no such copy: it re-parses, so what it returns was never in the
// memo.
//
// # Unbounded, following the blob cache
//
// Neither memo is bounded, matching the deliberate choice documented on deps.
// The rationale is the same and this does not weaken it: a resolution touches
// the packages in its closure, so the memo is bounded by the work actually
// requested rather than by the corpus, and an RSFIndex is created per resolve by
// every caller in this module. What changes is the CONSTANT -- the memo retains
// parsed requirements for every (package, version) asked about, on top of the
// raw strings deps already holds.
//
// ⚠️ A long-lived server process resolving arbitrary requests would want a bound
// on BOTH caches; that is still not this. Bounding the memo alone would not help
// -- deps would keep the same package set resident in raw form -- so the bound,
// when it is needed, is one policy over the pair rather than two.

// lookupMetadata reads the parsed memo. ok is false when nothing is memoized for
// this (package, version); a memoized failure is ok with a non-nil err.
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

// storeMetadata records a parsed outcome. Last writer wins, which is safe
// because the outcome is a pure function of the cached blob and the key: two
// goroutines racing compute the same answer.
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
// The substitution is not observable: ver and the memoized Version render to
// the same string, because that rendering is the memo key.
//
// Nothing else in PackageMetadata carries a version.Version. Requirement and
// Marker store their operands as strings and parse per call, verified against
// go-python-packaging v0.5.0, which is why the parsed requirements CAN be
// shared.
func cloneMetadata(m PackageMetadata, ver version.Version) PackageMetadata {
	m.Version = ver

	// nil is preserved rather than normalized to an empty slice: "the record
	// declared no requirements" has always come back as a nil slice here, and a
	// caller distinguishing nil from empty must keep seeing what it saw.
	if m.RequiresDist != nil {
		reqs := make([]requirement.Requirement, len(m.RequiresDist))
		copy(reqs, m.RequiresDist)
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
	order, ok := idx.versionList[pkg]
	idx.memoMu.RUnlock()
	if ok {
		return parseKeys(order)
	}

	decoded, err := idx.deps(pkg)
	if err != nil {
		return nil, err
	}

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
	winners := make([]string, 0, len(candidates))
	for i, c := range candidates {
		if i > 0 && candidates[i-1].parsed.Equal(c.parsed) {
			// A later member of a class already represented. See the dedup note in
			// the method doc.
			continue
		}
		out = append(out, c.parsed)
		winners = append(winners, c.key)
	}

	idx.memoMu.Lock()
	// Last writer wins. Two goroutines racing here computed the same order from
	// the same keys, so which one lands does not matter -- unlike deps, where
	// first-writer-wins exists so every caller shares ONE map object. A []string
	// is safe to share because nothing in the resolution path writes to one.
	idx.versionList[pkg] = winners
	idx.memoMu.Unlock()

	// The freshly parsed values, not a re-parse of what was just stored: the
	// first call should not pay twice.
	return out, nil
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

// preferKey reports whether key a is the better representative of a PEP 440
// equality class than key b.
//
// Canonical spellings win, then the lexicographically smallest. Shared by
// Versions and Metadata deliberately: Versions decides which spelling a caller
// ever sees, and Metadata decides which stored record that spelling resolves to.
// If those two used separate implementations of "the same rule" they would agree
// only by coincidence, and a resolver would be able to hold a version that
// resolves to a different package's dependency set than the one dedup considered
// authoritative.
//
// canonical means the key round-trips through PEP 440 normalization, which is the
// best available evidence of what the publisher actually wrote. The lexicographic
// tail exists only to make the outcome total, since two non-canonical spellings
// can both compare equal.
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

func preferKey(a string, aCanonical bool, b string, bCanonical bool) bool {
	if aCanonical != bCanonical {
		return aCanonical
	}
	return a < b
}

// Metadata implements MetadataIndex.
//
// # Memoized per (package, version)
//
// The parse below is memoized, keyed by the version's canonical rendering. See
// the memo notes above deps for what that buys, what it costs, and why the
// returned slices are copies.
//
// ver.String() is the right key because it is a total, deterministic function of
// the requested version: two version.Version values that render the same ARE the
// same version, and two that compare PEP 440-equal but render differently ("1.0"
// and "1.0.0") take separate memo entries that both resolve, through preferKey,
// to the same stored record. So the memo can duplicate an entry; it cannot
// disagree with itself.
func (idx *RSFIndex) Metadata(ctx context.Context, pkg PackageName, ver version.Version) (PackageMetadata, error) {
	if err := ctx.Err(); err != nil {
		return PackageMetadata{}, err
	}

	if err := checkVersionInitialized("Metadata", pkg, ver); err != nil {
		return PackageMetadata{}, err
	}

	key := ver.String()
	if entry, ok := idx.lookupMetadata(pkg, key); ok {
		if entry.err != nil {
			return PackageMetadata{}, entry.err
		}
		return cloneMetadata(entry.meta, ver), nil
	}

	decoded, err := idx.deps(pkg)
	if err != nil {
		// Deliberately not memoized: deps could not produce a record at all, so
		// this says nothing about the version. See memoEntry.
		return PackageMetadata{}, err
	}

	meta, err := idx.buildMetadata(pkg, ver, key, decoded)

	// Stored with Version ZEROED. cloneMetadata always fills it from the
	// caller's own ver, so a memoized Version could only ever be a version.Version
	// shared between goroutines -- which is exactly what must not happen. Zeroing
	// makes that structural rather than a rule to remember, and it stops the memo
	// retaining the first caller's value for the life of the index.
	stored := meta
	stored.Version = version.Version{}
	idx.storeMetadata(pkg, key, memoEntry{meta: stored, err: err})

	if err != nil {
		return PackageMetadata{}, err
	}
	// Cloned on the miss path too, so the value a caller gets never aliases the
	// memo -- cold and warm hand back the same kind of thing.
	return cloneMetadata(meta, ver), nil
}

// buildMetadata parses one stored record into PackageMetadata.
//
// Split out of Metadata so the memo wraps exactly the work worth memoizing:
// everything here is a pure function of decoded, pkg, ver and key.
func (idx *RSFIndex) buildMetadata(
	pkg PackageName, ver version.Version, key string,
	decoded map[string]pypirsf.VersionDeps,
) (PackageMetadata, error) {
	raw, ok := decoded[key]
	if !ok {
		// The producer writes whatever version string the publisher used, so
		// "1.0" and "1.0.0" can both appear and neither is wrong. Fall back to
		// PEP 440 equality before giving up.
		//
		// ⚠️ MORE THAN ONE KEY CAN QUALIFY, so the choice must not be made by map
		// iteration order. Go randomizes it, and a package carrying two
		// equal-comparing spellings with different dependencies then answers
		// differently from one call to the next on the same index — measured on a
		// production snapshot as 500 calls returning two distinct results. That is
		// a wrong answer delivered with total confidence, and it falsifies this
		// type's documented guarantee that the same file resolves the same way
		// forever.
		//
		// The rule: among keys that compare equal, prefer the one whose spelling
		// is already canonical, then the lexicographically smallest. The first
		// clause is the principled half — a key that round-trips through
		// normalization is the best available evidence of what the publisher
		// meant. The second exists only to make the outcome total, since two
		// non-canonical spellings can both compare equal.
		//
		// Neither clause makes the underlying data unambiguous: which spelling is
		// authoritative is unknowable from the snapshot, and the caller cannot
		// currently detect that it happened. Surfacing the ambiguity needs an API
		// this interface does not have yet; determinism is the part that can be
		// fixed here.
		//
		// The rule itself lives in preferKey, shared with Versions, so the version
		// Versions hands out and the record Metadata resolves for it cannot drift
		// apart. Two separate implementations of "the same rule" would agree only
		// by coincidence.
		bestKey, found := "", false
		bestCanonical := false
		for key := range decoded {
			parsed, parseErr := version.Parse(key)
			if parseErr != nil || !parsed.Equal(ver) {
				continue
			}
			canonical := parsed.String() == key
			if !found || preferKey(key, canonical, bestKey, bestCanonical) {
				bestKey, bestCanonical, found = key, canonical, true
			}
		}
		if found {
			raw = decoded[bestKey]
		} else {
			// The package exists but this version has no captured metadata.
			// Unavailable rather than not-found: reporting not-found would
			// invite a resolver to treat it as a typo and give up on a package
			// that is genuinely present.
			return PackageMetadata{}, fmt.Errorf("index %q: %q %s: %w",
				idx.origin, pkg, ver, ErrMetadataUnavailable)
		}
	}

	meta := PackageMetadata{
		Name:    pkg,
		Version: ver,
		Origin:  idx.origin,
	}

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
				return PackageMetadata{}, fmt.Errorf(
					"index %q: %q %s: parsing requirement %q: %w: %w",
					idx.origin, pkg, ver, rawReq, ErrMetadataUnusable, reqErr)
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
