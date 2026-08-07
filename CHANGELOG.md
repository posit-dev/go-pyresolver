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

### Breaking

- `pyresolve`'s `--` now works. It never did, in any position: `reorderArgs` consumed the
  terminator without re-emitting it, so `flag.Parse` — which does its own scan over the
  reordered arguments, and for which `--` is the only stop token — never saw it. A package
  named `--json` was parsed as the `--json` flag, and the command then exited **1**
  ("usage or file error") reporting `expected exactly one package name argument, got []`,
  blaming the caller for a name it had silently eaten. Listed as Breaking because arguments
  after `--` are now taken literally, which is the documented behavior but not the previous
  one.

### Fixed

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

- Requires `go-python-packaging` **v0.3.1** (from v0.3.0), for the zero-value `Version`
  fixes above.
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

[Unreleased]: https://github.com/posit-dev/go-pyresolver/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/posit-dev/go-pyresolver/releases/tag/v0.1.0
