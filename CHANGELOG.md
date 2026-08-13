# Changelog

All notable changes to this module are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
While the major version is `0`, breaking changes ship in a **minor** bump rather
than a major one, and are always listed under a `Breaking` heading so they are not
mistaken for a safe patch upgrade.

Entries go under `## [Unreleased]`, never into a dated section. A dated section may
already be tagged, and a Go module tag cannot be moved once the module proxy has
served it.

## [Unreleased]

### Added

- `pep440set`, a canonical PEP 440 version-set algebra (intersection, union,
  complement) satisfying go-pubgrub's `versionset.Set`. `FromSpecifiers` maps a
  specifier set to spans, operator-level pre- and post-release guards included,
  and is held to `version.Specifiers.Check` by **differential testing rather
  than by construction**: a generated operator/operand/version grid, a walk over
  a production PyPI snapshot, and a fuzzer over arbitrary specifier text. All
  three currently agree everywhere they look.

  Agreement on input none of them has looked at is **not** claimed. The grid
  contained only canonical operand spellings and only release segments that fit
  in an `int64`, and both gaps hid real mapping bugs — the alias spellings of
  `~=` (`~=1.0c1`, `~=1.0.pre1`, `~=1.0.r1`, `~=0.0.posT`, …), where the prefix
  is derived from the raw operand text and the seven spellings are **not**
  interchangeable, and release segments at or above 2<sup>63</sup>, which the
  ordering key silently truncated. Both are fixed and both are now in the grids.

  Arbitrary-equality (`===`) specifiers report `ErrUnrepresentable` rather than
  being approximated, as does the `||` OR-of-ANDs form, whose groups no exported
  accessor exposes. `Equal` and `IsEmpty` are exact on **positions**, not on
  versions: two sets admitting exactly the same versions can compare unequal
  when they differ only across a gap that no version occupies.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- `candidate`, the policy layer the resolver consults when choosing which
  version of a package to try next: a `Policy` ranking interface with a
  newest-first default, and pre-release admission derived once from the
  requirements a resolution starts with. Ranking cannot remove a version, so a
  caller's preference can never make a version look nonexistent to the solver.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- `provider`, the translation layer between Python packaging semantics and
  go-pubgrub's generic solver. It answers the solver's two questions -- which
  versions of a package exist within a range, and what one version requires --
  by reading a `MetadataIndex`, evaluating PEP 508 markers against one concrete
  target environment, and converting version specifiers through `pep440set`.

  Extras are modeled as virtual packages: `flask[async]` is a distinct solver
  node that depends on `flask` at exactly the same version plus whatever the
  extra adds, so a solver with no notion of extras resolves them correctly and
  the two cannot drift apart. An extra no version declares has no candidates at
  all rather than resolving happily and installing nothing.

  The interpreter is modeled as a package too, rather than as a filter applied
  behind the solver's back. A `Requires-Python` mismatch therefore appears in
  the derivation graph as an ordinary version conflict that names `python`,
  instead of a version silently vanishing and the report saying a package has
  no versions.

  A version that cannot be used -- an sdist-only release with no published
  metadata, a specifier with no version-set equivalent, a direct-reference URL
  requirement -- is excluded from the candidate count and the reason is recorded
  for the eventual failure report. An index that cannot ANSWER is a different
  thing and propagates as an error: a transport failure read as "no such
  version" would let a resolution quietly settle on an older release, or blame
  the user's constraints for an outage.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- `resolver`, the public entry point, which completes the adapter. A consumer
  can now hand `Resolve` a list of PEP 508 requirements, an `index.MetadataIndex`
  and a target environment, and get back one pinned version per package the
  requirements transitively reach — extras included, markers evaluated, and
  transitive multi-version conflicts backtracked rather than hard-failed.

  Virtual extra packages collapse into their base project, so a `Resolution`
  reads as `flask 3.0` with `[async]` noted alongside rather than as two
  entries; the interpreter and the synthetic root do not appear at all. `Order`
  is the solver's decision order and every extras list is sorted, so two runs
  over the same inputs produce byte-identical output.

  A conflict comes back as a `*ResolutionError` whose `Report` explains the
  chain of reasoning step by step, rendered in Python terms — `flask[async]`,
  `>=1.0,<2.0`, and `Python 3.11.4` for the interpreter. An sdist-only release
  relevant to the failure gets a note of its own, because a report that says
  "no version of flask matches >=3.0" about a release visible on PyPI is true
  in a way nobody can act on. An index that could not answer is passed through
  as itself rather than dressed up as a conflict between requirements.

  **Not included, deliberately:** no distribution-file or wheel selection —
  a `Resolution` is versions only, and the PyPI RSF carries no file records to
  select between; one concrete marker environment per resolution, not universal
  resolution; and no sdist building to discover metadata.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- `pep440set.Set.String`, which renders a version set as PEP 440 specifier text
  (`>=1.0,<2.0`, `==1.*`, `!=1.0`). Without it nothing outside the package could
  describe a set at all — spans and bounds are unexported — and go-pubgrub's
  failure report fell back to `%v` on the raw struct.

  A rendering that **is** a specifier is **version-exact**: parse it back and it
  holds the same versions. That is not free, because a bound is a position and a
  position is finer than a specifier — the complement of `<=1.0` starts above
  1.0's local variants and therefore holds `1.0.post1`, which `>1.0` does not
  match — so those bounds are rendered by naming the least version above them
  (`>=1.0.post0.dev0`). The few positions PEP 440 has no operator for at all are
  rendered with a bracketed marker (`<=1.0[+post]`, `<1.0[+pre]`) naming the
  region the bare specifier would misstate, so **no two sets holding different
  versions ever render the same text**. Measured over 1,500 production packages:
  54,120 renderings, 0 disagreements.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- `provider.ReasonMetadataUnavailable`, the `Unusable.Reason` recorded for an
  sdist-only or dynamic-metadata release, so a consumer can pick those records
  out by name rather than by matching a sentence.
  ([#18657](https://github.com/rstudio/package-manager/issues/18657))

- A cold/warm resolution benchmark over a committed corpus
  (`resolver/bench_test.go`, `resolver/bench_corpus_test.go`), the RFD 0001
  Phase 3 exit gate. Test-only: no library code changed, and a benchmark never
  runs under `go test ./...`, so the 981 MB production snapshot is never
  required. It defaults to the committed excerpt and reads the same
  `PYPIRSF_TEST_FILE` as the other real-corpus tests.

  It counts `MetadataIndex` calls through a decorator as well as timing the
  resolution, because the call count is the part that does not depend on the
  machine. **The gate is not met.** Against the production snapshot: cold under
  100 ms for five of seven corpus entries and 5-7x over for the other two; warm
  under 1 ms for none of them, and warm is within 3% of cold everywhere, because
  the only cache in the path holds raw strings and every `Metadata` call
  re-parses them. Full table, profile and verdict are in the package
  documentation of `resolver/bench_test.go`.
  ([#18651](https://github.com/rstudio/package-manager/issues/18651))

### Fixed

- `pep440set` derived a bound's sort key on every comparison rather than once
  per bound. `cmpBound` sits in the innermost loop of the set algebra, which is
  itself in the solver's hot loop, and each call rendered both versions' release
  segments to a string and split it, then rendered and **re-parsed** both public
  versions. One resolution against an index shaped like a curated or air-gapped
  repository -- packages present, transitive dependencies absent -- took 17.5 s
  and allocated 25 GB *cumulatively*: bytes handed out and collected over the
  run, not memory held. **The symptom is latency, not memory exhaustion.** Peak
  heap through that same resolution was measured at 115 MB of `HeapSys` before
  this change and 15 MB after, three orders of magnitude below the cumulative
  figure; what the churn buys is garbage collection, and the resolution is
  GC-bound for tens of seconds. The failing, unsatisfiable path is the expensive
  one, so a request that could not be satisfied was the slowest one to be told
  so.

  The key is now derived once, when the bound is built. The span slices the
  algebra allocates are sized up front instead of grown -- and allocated only
  once there is a span to put in them, so an operation whose result is empty
  allocates nothing at all. Same resolution, same index calls, same outcome:
  **17.5 s to 2.1 s, 25.1 GB to 3.7 GB allocated, and 568 million allocations
  to 14.8 million**. Building a set of specifiers costs slightly more than it
  did (`>=1.0`: 1.55 to 1.91 µs) because a bound now pays for its key up front;
  comparing sets costs far less (`Equal`: 10.1 µs and 320 allocations to
  0.16 µs and none).

  No ordering changed. `TestBoundKeyAgreesWithLiteral` holds the derived-once
  and derived-on-demand paths to the same answer on every pair of a widened
  ordering grid, whose PEP 440 orderings were cross-checked against
  pypa/packaging 26.2. Against the full production snapshot,
  `TestDifferentialAgainstRealCorpus` still agrees with
  `version.Specifiers.Check` on every pair it compares: **755,934 (specifier,
  version) pairs over 7,188 specifier sets** in the 400-package sample it takes
  by default, and 33,918,235 pairs over 305,548 specifier sets when widened to
  20,000 packages with `PEP440SET_CORPUS_PACKAGES`. Zero disagreements either
  way.
  ([#19713](https://github.com/rstudio/package-manager/issues/19713))

## [0.4.0] - 2026-08-10

### Breaking

- `FilteredIndex` carrying a **file-level policy** (`ExcludeYanked` or `SnapshotDate`) over an
  index that serves **no files** now **refuses** instead of answering: `Versions`, `Metadata`
  and `Files` all report `ErrFilesUnavailable`, wrapped with the advice to compose over a
  file-serving source.

  ⚠️ **Anyone on `v0.3.0` should upgrade.** There, such a composition returned an **empty
  version list with a nil error** for every package — so every package looked like it had no
  acceptable version, which downstream reads as a constraint conflict that does not exist.
  That is indistinguishable from a real resolution failure, which makes it strictly worse
  than an error. `RSFIndex` serves no files *by design* and no index ever will, so this is the
  default outcome of the most obvious wiring, not an exotic edge case.

  How `v0.3.0` came to be wrong is worth recording, because it argues against changing it
  back. The refusal existed, and was removed because `ErrFilesUnavailable` could not be
  trusted: `MultiIndex` emitted it for a mere data condition, so a legitimate partial mirror
  hard-errored with advice to compose the very thing it had composed. The fix for *that*
  — reserving the sentinel for a genuine capability statement, emitted only when every source
  is fileless — landed in the **same release**. The two corrections passed each other: one
  made the signal trustworthy while the other stopped trusting it.

  A partial mirror still stays silent and drops the affected versions, because that case now
  arrives as `ErrMetadataUnavailable`. The distinction is capability versus data, and it holds
  at every layer: a `FilteredIndex` over a fileless inner can never serve a file, so its own
  `ErrFilesUnavailable` is true about itself, and a `MultiIndex` above it demotes that in turn.

  The refusal is **total where it matters**. It cannot fire when there are no versions to
  check, or when the version-level axis already excluded them all — but in those cases the
  file axes could not have changed the answer, so an empty result is correct over any index.
  A test pins that by asserting a fileless and a file-serving index with identical version
  content give identical answers in exactly those cases.

## [0.3.0] - 2026-08-10

### Changed

- Requires `go-python-packaging` **v0.5.0** (from v0.4.0). ⚠️ **Anyone on `v0.2.0` of this
  module should upgrade**, because `v0.4.0` shipped a wildcard/`~=` padding defect that this
  module inherited: the zero-padding loops appended `⌈gap/2⌉` zeros instead of `gap`, so
  `!=7.0.0.*` matched `7` where `pypa/packaging` excludes it, and `==0.0.0.*` matched
  `1!1.0`. Measured over an 82-row oracle sweeping version-segment gaps of 1-4 in both
  directions, `v0.4.0` diverged from the reference on **30 of 82** cases and `v0.5.0` on
  **0**. It survived undetected because `⌈1/2⌉ == 1`, making a one-segment gap correct by
  accident — and every test written for it used a one-segment gap.

  `go build` and `go test ./...` are clean against `v0.5.0`; the fix changes no behavior this
  module's own suite depended on.

### Added

- `index.FilteredIndex` and `index.FilterPolicy` — a `MetadataIndex` wrapper applying
  release-admission policy: pre-release exclusion, PEP 592 yanked-file exclusion, and an
  inclusive snapshot-date cutoff. The zero `FilterPolicy` filters nothing, which is why the
  fields are spelled `Exclude*`: a wrapper that silently dropped every pre-release would
  punish whoever wrapped an index only to compose it.

  The policy is enforced on **all three** methods, not only `Versions`. Filtering the listing
  alone would be bypassed by any caller holding a version from elsewhere — a pin, a lockfile,
  another index — so it would be a default rather than a policy.

  ⚠️ Only pre-release exclusion is decidable from a version alone. Yanking is per-file per
  PEP 592 and an upload time belongs to a file, so those two axes are evaluated through
  `Files`. A file-level policy over an index that serves no files therefore admits
  **nothing**, and it says so with empty version lists rather than by failing — every package
  looks like it has no acceptable version. This is **not** guarded, because it cannot be:
  "no file evidence" is the same observation whether the operator wired up a fileless index
  or the package is simply absent from the file source, and the second is a supported
  configuration (a partial mirror). Since `RSFIndex` serves no files by the shape of the
  data, the arrangement that works is a `FilteredIndex` over a `MultiIndex` pairing the RSF
  with a file-serving source, and **verifying that is the caller's job**.

  ⚠️ The two file axes fail in opposite directions, and not in the safe one. `SnapshotDate`
  fails **closed**: a file whose `UploadTime` is the zero value is dropped, because an
  unrecorded time cannot be shown to precede the cutoff, and admitting it would let a file
  published yesterday into a snapshot dated last year. `ExcludeYanked` fails **open**:
  `DistFile.Yanked` cannot express "not captured", so a source that does not record PEP 592
  data reports every file as un-yanked and the policy admits everything while appearing to
  work. Only set `ExcludeYanked` over a source whose files genuinely carry PEP 592 data. The
  gap is fixable additively later (a `YankedKnown` field on `DistFile`), so it is not locked
  in by this release.

  Under an active file-level policy, `Versions` issues one `Files` lookup per surviving
  version, serially. Cheap in-process; a 500-version package over a network-backed source is
  500 sequential round trips. Accepted for now because it is fixable additively — an optional
  batch interface adds nothing to existing implementations — and because caching belongs in
  the index being wrapped.

  Under an active file-level policy, a version with **no admissible file** is refused with
  `ErrMetadataUnavailable` by all three methods — including a version that had zero files to
  begin with, since there is then nothing to admit it on. The interface's "known version with
  no files is empty plus nil" answer still applies whenever no file-level policy is active.

- `index.MultiIndex` — a `MetadataIndex` over ordered sources. `Versions` returns the
  **union**; `Metadata` and `Files` return the first source that can answer, with
  `PackageMetadata.Origin` naming which one did. The asymmetry is the design: which versions
  exist is naturally a union, but two sources can disagree about one release, and merging
  their answers would produce a record no publisher ever made.

  Error taxonomy across sources, since a source's `ErrPackageNotFound` is that source's
  answer about itself and not a fact about the composed index:

  - `Versions` reports `ErrPackageNotFound` only when **no** source knows the name. One
    source knowing it and carrying no versions is an empty slice and a nil error.
  - `Metadata` prefers, in order of how much was learned, a malformed record somewhere
    (`ErrMetadataUnusable`) over no record anywhere (`ErrMetadataUnavailable`) over nobody
    having heard of the package (`ErrPackageNotFound`).
  - `Files` treats an **empty list with a nil error as an answer**, not a miss — a release
    can have every file deleted, and "keep looking" would let a stale mirror resurrect files
    the authoritative source removed. `ErrFilesUnavailable` does mean "ask another source",
    and it is returned only when **every** source is fileless: a fileless source emits it for
    every lookup without inspecting the package, so it is the weakest evidence available, not
    the strongest. Letting it win would make `ErrPackageNotFound` unreachable whenever an RSF
    is in the composition, leaving a caller unable to tell a typo'd name from "nobody serves
    files".
  - Each method tolerates sentinels the interface does not list for it, since a source may
    itself be a `FilteredIndex`. `Versions` skips `ErrFilesUnavailable`; `Metadata` treats it
    as "this source supplied no metadata"; `Files` tolerates all four. One source's choice of
    error does not abort the whole lookup.

  ⚠️ Source A knowing the package but not the version, while source B knows neither, is
  `ErrMetadataUnavailable` — **not** `ErrPackageNotFound`. The package was found; a caller
  branching on not-found there reports a missing package for a present one.

  Versions are deduped by PEP 440 equality across sources, collapsing a class to the
  **earliest** source's spelling so the representative resolves to the record `MultiIndex`
  treated as authoritative. A cross-source spelling difference is not bridged when the
  earliest source can list a version but not supply its metadata; that limitation is
  documented on the type and pinned by a test.

  ⚠️ Under a file-level `FilteredIndex` that limitation is larger than a metadata miss: the
  file lookup takes the same string-keyed path, so a version whose file evidence lives in
  another source under another spelling is **dropped from the version list**, and the package
  can appear to have no usable versions at all.

- The tests that read **producer output** now run on every pull request, against a committed
  ~988 KB excerpt of a real production snapshot at `index/testdata/pypi-trimmed.rsf`.
  `TestRSFIndexAgainstRealFile` was gated behind `PYPIRSF_TEST_FILE`, which CI does not set,
  so the only tests reading bytes this module did not write never ran — the suite was green
  and the property was unchecked. A missing fixture now **fails** rather than skipping, and
  CI fails if any of these tests skips: a silent skip is the defect, not the fallback.
  `index/testdata/README.md` records how the excerpt was derived and how to regenerate it.
- Assertions on the shapes the walk does not reach. Wiring the real-data test into CI closed
  half the gap: it passes on a real snapshot because its root is `flask`, whose closure is
  well-behaved, and the review measured 507 roots for which it would have failed. The
  excerpt therefore also carries seven packages chosen for the state they carry — every
  version key unparseable (`holygrail`), a `Requires-Dist` entry PEP 508 rejects
  (`aad-token-verify`), PEP 440-equal keys with contradictory dependencies
  (`database-connector`, `guessproj`), a direct-URL requirement (`memery`), an unreadable
  `Requires-Python` (`admobilize-malos`), and an ambiguous spelling resolved by tiebreak
  (`anpy`) — each with its own assertion.
- Tests pinning behaviours that no test constrained, found by re-running a mutation pass over
  this module (rstudio/package-manager#19466). Thirteen mutations that the suite did not
  notice now fail a test, each verified by applying the mutation, watching the test go red,
  and reverting: the choice of representative within a PEP 440 equality class, the ordering of
  `UnparseableVersionKeys`, deduplication of a repeated direct-URL requirement, `walk`'s exit
  code for an uncaptured root, `deps` emitting `requires_python_raw` only when the constraint
  is unreadable, JSON output leaving `<` and `>` unescaped, a bool flag not consuming the
  token after it, `exitCodeFor`'s fallback, the guidance on an RSF that predates dependency
  capture, and four decoder refusals on malformed input — two of which panicked rather than
  returning an error once the bounds check was loosened.

  Mutations that no test can distinguish are recorded as such in
  `index/pinned_mutations_test.go` and `cmd/pyresolve/pinned_mutations_test.go`, with the
  reason, rather than being covered by a test that only appears to pin them. One pair is
  mutually masking: reversing `Versions`' internal ordering and dropping `versions`' own sort
  are each invisible alone and caught together.

## [0.2.0] - 2026-08-10

### Breaking

- `MockIndex.Metadata` and `MockIndex.Files` return `ErrMetadataUnavailable`, not
  `ErrPackageNotFound`, for an unknown **version** of a **known** package. `ErrPackageNotFound`
  is untrue on its face there — the package *was* found — so a consumer branching on it
  reported a missing package for a present one.

  This aligns the mock with `RSFIndex`, which already answered `ErrMetadataUnavailable`.
  Listed as Breaking because a test written against the mock's old answer will now fail; the
  fix is to expect `ErrMetadataUnavailable`, which is what a real index returns.
- `pyresolve`'s `--` now works. It never did, in any position: `reorderArgs` consumed the
  terminator without re-emitting it, so `flag.Parse` — which does its own scan over the
  reordered arguments, and for which `--` is the only stop token — never saw it. A package
  named `--json` was parsed as the `--json` flag, and the command then exited **1**
  ("usage or file error") reporting `expected exactly one package name argument, got []`,
  blaming the caller for a name it had silently eaten. Listed as Breaking because arguments
  after `--` are now taken literally, which is the documented behavior but not the previous
  one.

### Added

- `pyresolve walk` reports the version it selected for each package. The text output shows
  it beside the name, and `--json` gains `selected_versions`. The walk takes one version of
  each package and its shape depends on which, so previously a reader could not tell which
  version produced any edge, and two walks over different snapshots printed identically
  while describing different graphs.

  `selected_versions` is partial by design: a package appears only if a version was
  selected for it, so names under `absent` and `no_dependency_data` are missing. A missing
  entry means no version was chosen, not that the version is unknown, and the category
  lists say why. Names under `unusable_metadata` **do** appear, since a version was
  selected and its metadata then failed to parse. Added as a map rather than by changing
  `packages` into objects, which would break existing JSON consumers.

  ⚠️ Version selection now happens before the depth cutoff rather than after, so packages
  *at* the cutoff report a version too. With `--depth 1` almost none of them did otherwise.
  One behavior change follows: a package at the cutoff with no selectable version is now
  reported under `no_dependency_data`, where before the cutoff returned first and it was
  reported as plainly reachable. Having no selectable version is a fact about the package,
  not about how deep the walk went.
- `PackageMetadata.RequiresPythonRaw` and `PackageMetadata.RequiresPythonUnreadable`
  preserve an interpreter constraint that could not be parsed. `RequiresPython` alone
  cannot distinguish "the record declared nothing" from "the record declared something we
  could not read" — both leave it empty — so a caller had no way to report the difference.
  The decoder still treats an unreadable constraint as unconstrained, which is deliberate
  and matches pip; what is new is that the decision is now visible rather than silent.

### Fixed

- The `MetadataIndex` contract documented an answer no implementation gave. It promised
  `ErrPackageNotFound` "if pkg **or ver** is unknown", while `RSFIndex` returned
  `ErrMetadataUnavailable` for an unknown version and `MockIndex` returned
  `ErrPackageNotFound` — three sources, three answers for one state.

  The contract now says `ErrPackageNotFound` only when the *package* is absent, and
  `ErrMetadataUnavailable` when the package is present but the index cannot supply metadata
  for that version, whether the version is unknown to it or known with nothing captured.

  ⚠️ Those two cases share one error **deliberately**: `RSFIndex` derives its version list
  from the RSF's dependency map, so a version absent from that map is indistinguishable from
  one present with nothing captured. An interface must not promise a distinction its
  implementations cannot make. The RSF does carry a separate per-snapshot version list, so
  the distinction is recoverable in principle, but `pypirsf` does not expose it today and it
  is therefore left unpromised rather than half-supported.
- `pyresolve deps` distinguishes an unreadable `Requires-Python` from an absent one. It
  printed `(unconstrained)` for both, asserting the publisher declared no interpreter
  constraint when the publisher declared one this tool could not read, and hiding that the
  version was being admitted for every interpreter by fallback rather than by declaration.
  536 packages in a production PyPI snapshot are in that state. The unreadable case now
  quotes the raw string, and `--json` gains `requires_python_raw` and
  `requires_python_unreadable` — previously the two states were byte-identical in JSON,
  since `requires_python` carries `omitempty`.
- `pyresolve walk` no longer counts names that are absent from the RSF as reachable
  packages. A missing name appeared in **both** `packages` and `absent`, which are
  contradictory claims, and `count` included it, inflating "N package(s) reachable" with
  names that cannot be installed from the file. `packages` and `absent` are now disjoint.

  Names under `no_dependency_data` and `unusable_metadata` **remain** reachable: those
  packages do have records, and the walk simply could not expand them. The finding this
  addresses grouped absent and uncaptured names together; they are different facts, and
  only the first was wrong.
- `RSFIndex.Metadata` and `RSFIndex.Files` reject an uninitialized (zero-value)
  `version.Version` with an explicit error naming it as a caller bug, rather than reporting
  `ErrMetadataUnavailable` — which blames the RSF for having no such version when the real
  problem is the argument. Deliberately not a new sentinel: there is nothing to branch on,
  only code to fix.

  This also closed a crash. Before `go-python-packaging` v0.3.1, `Version.String()` panicked
  on a zero value, so `Metadata` died on `decoded[ver.String()]`. `Files` did *not*, because
  `fmt` recovers a panic raised inside a `String` method and substitutes
  `%!s(PANIC=...)` — so it returned a garbled but plausible-looking error. The bump alone
  would have converted the crash into a silent wrong answer; the guard is what keeps it
  diagnosable.

### Changed

- Requires `go-python-packaging` **v0.4.0** (from v0.3.0), for the zero-value `Version`
  fixes above and for its comma requirement. That requirement is inherited, so a
  requirement string whose version constraints are not comma-separated (`>=1.0<2.0`) is now
  rejected here too. Over the production PyPI snapshot upstream measured 21 such strings
  rejected out of 2,804,136, and 0 newly accepted; each one previously parsed into a
  constraint boundary the input never contained.
- Requires `klauspost/compress` **v1.19.2** (from v1.19.1). Worth noting because a consumer
  adding this module raises its own pin through minimal version selection.
- Corrected a stale warning on `PackageMetadata.RequiresPython`. It said `Specifiers.Check`
  returns false for every version when the set holds no groups, inverting the zero value's
  meaning. That was true when written and stopped being true in `go-python-packaging`
  v0.3.0, which made an empty specifier set admit every version. Verified against v0.3.1
  rather than assumed. `SupportsPython` is still the preferred spelling, for clarity and for
  older builds, but not for the reason the comment gave.
- Documented why `walk`'s `ErrMetadataUnavailable` branch is unreachable and kept. It is
  unreachable only for `RSFIndex`, whose `Versions`/`Metadata` agreement is a tested
  invariant; the `MetadataIndex` contract permits that error for a known sdist-only release
  and `MockIndex` already returns it, so deleting the branch would trade dead code for a
  latent gap that reappears the moment the command is generalized to the interface.

## [0.1.0] - 2026-08-07

First release. **Read what is not in it before depending on it.**

### Added

- `pypirsf/` — the PyPI record layout and dependency-blob decoder for Repository
  Snapshot Format files. Reads a local file; makes no network request.
- `index/` — `MetadataIndex`, the seam between a resolver and its storage, with two
  implementations: `RSFIndex` over a local RSF file, and `MockIndex` for tests.
- `cmd/pyresolve` — a CLI that inspects an RSF file: `stats`, `versions`, `deps`,
  and `walk`. It reads the file named by `--rsf` and nothing else.

`index/` distinguishes four states a caller must not conflate, each with its own
sentinel: `ErrPackageNotFound` (no such package), `ErrMetadataUnavailable`
(present, nothing captured), `ErrMetadataUnusable` (captured, does not conform to
the spec), and `ErrFilesUnavailable` (ask a different source). The CLI keeps them
apart at the process boundary too: exit `0` success, `1` usage or file error,
`2` package not found, `3` metadata present but not conforming.

### Not in this release

`candidate/`, `provider/`, and `resolver/` are **documentation-only stubs with no
declarations**. Importing them compiles and gives you nothing. Version solving is
not implemented here yet; that work is RFD 0001's later phases. What `0.1.0`
delivers is metadata *retrieval* — enough for a consumer that needs an index over
an RSF file, which is what
[rstudio/package-manager#19437](https://github.com/rstudio/package-manager/issues/19437)
needs.

### Notes

- Requires `go-python-packaging` **v0.3.0 or later**. Earlier tags accept two
  version specifiers written adjacently and re-render them with a comma the input
  never contained, so `cryptography (>=3.3.2<4)` silently became
  `cryptography>=3.3.2,<4`. Under v0.3.0 that requirement is rejected and the
  version is reported `ErrMetadataUnusable` instead of resolving against a
  fabricated constraint. Measured over a production PyPI snapshot (932,861
  packages, 7,666,849 versions, 76,741,586 requirement strings), that moves 3,820
  versions from usable to unusable and takes 80 more packages to no usable version
  at all — 413 in total. **Failing loudly there is the intended behavior**: an
  invented constraint boundary changes which versions resolve, and the publisher's
  intent is not recoverable from the malformed string.
- Module floor is Go 1.25.
- Dual-licensed Apache-2.0 OR MIT. See `LICENSE-APACHE`, `LICENSE-MIT`, and
  `NOTICE` for attribution of adapted material.

[Unreleased]: https://github.com/posit-dev/go-pyresolver/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/posit-dev/go-pyresolver/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/posit-dev/go-pyresolver/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/posit-dev/go-pyresolver/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/posit-dev/go-pyresolver/releases/tag/v0.1.0
