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

[Unreleased]: https://github.com/posit-dev/go-pyresolver/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/posit-dev/go-pyresolver/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/posit-dev/go-pyresolver/releases/tag/v0.1.0
