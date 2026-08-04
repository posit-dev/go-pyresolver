# go-pyresolver — Claude Code guide

The Python-aware adapter that drives a generic PubGrub solver with PyPI
metadata. Part of [RFD 0001 — Native Go PyPI Dependency Resolution](https://github.com/rstudio/package-manager/blob/main/docs/rfds/0001-pypi-native-resolver/README.md),
Phase 3.

> **Status: skeleton.** Every package is a `doc.go` describing intended scope.
> `index/` is filled in by rstudio/package-manager#18646 (interface, types,
> MockIndex) and #18647 (RSFIndex, CachedJSONIndex).

## Module layout

| Package | Scope |
|---|---|
| `pypirsf/` | PyPI RSF record layout + dependency-blob decoder |
| `index/` | `MetadataIndex` interface + implementations |
| `candidate/` | Version and distribution-file selection policy |
| `provider/` | Python-to-generic-solver adaptation, incl. extras |
| `resolver/` | Public entry point |

## `pypirsf/` — the PyPI RSF record layout and deps decoder

This is the one implementation of the PyPI dependency-blob decode, shared by the
standalone resolver and by Package Manager. It deliberately lives here rather
than in `rstudio/repository-snapshot-format`: that library is generic on
purpose, and a PyPI record schema is Python-packaging knowledge, not format
knowledge.

Three rules for this package:

**Field order in `record.go` is wire format, not struct layout.** `ReleaseDate`
must stay between `Version` and `Summary`, and `Summary` must stay last.
Released readers navigate subfields by name with the RSF reader's forward-only
skip, so they advance from `summary` to the next element's `deleted` without
skipping — they silently depend on `summary` being last. Putting a field after
it leaks bytes into the next element and desynchronizes every shipped reader.
This already happened in production.

**Keep the hardening.** Every length-prefixed read is bounded against bytes
actually remaining, and `MaxDecompressedBytes` caps zstd expansion. The input is
a file fetched over a network; a standalone tool has even less reason to trust
it than a server does. Never size an allocation from a count the input claims
without checking the bytes exist.

**Decoded types stay raw.** `VersionDeps` holds unparsed PEP 508 and PEP 440
strings so this package depends only on zstd. Parsing belongs to the caller.

Do not add an encoder here. RSF files come from the producer pipeline, and a
second writer would be free to diverge from it.

Sibling modules: [`go-python-packaging`](https://github.com/posit-dev/go-python-packaging)
(PEP primitives) and `go-pubgrub` (the algorithm, language-agnostic).

## ⚠️ The clean-room policy does NOT apply to this module

RFD 0001 §7.1 scopes its clean-room attestation to the **go-pubgrub
implementation source only**. In this module:

- **Allowed and intended:** reading `uv-resolver`, `uv-pep440`, `uv-pep508`.
  Those crates are Apache-2.0/MIT and adapting from them *with attribution in
  `NOTICE`* is the standard pattern this RFD endorses.
- **Still forbidden, if you switch to working on go-pubgrub:** `pubgrub-rs`,
  `astral-sh/pubgrub` (both MPL-2.0), and any other Go PubGrub port — the Go
  ports for independence-claim reasons rather than licensing.

Do not restate the go-pubgrub rules here as if they bind this module; that
confusion would block legitimate work. Conversely, do not read forbidden
sources "just for this one adapter question" and then carry that context into
go-pubgrub work in the same session.

## Architectural invariants worth protecting

**The resolver core makes no HTTP request and touches no database.** It calls
`MetadataIndex`. Any `net/http` or `database/sql` import outside an `index/`
implementation is a design break, not a shortcut.

**Dependencies come from the RSF, not the CDN.** RFD Rev 15 reversed the
carrier: `requires_dist`, `requires_python`, and `provides_extra` are resident
in the PyPI RSF and read at runtime. Only `Files()` is CDN-backed. Code or docs
implying a CDN fetch for dependency metadata is stale — this reversal is what
makes air-gapped resolution work at all.

**Extras are virtual packages.** `pkg[security]` becomes a distinct package
depending on `pkg` at the same version plus the extra's own requirements. This
keeps the generic solver free of Python knowledge, and it is required from day
one: without it, any user of `[extras]` syntax silently gets an incomplete
closure.

## Build & test

```bash
go build ./...
go test ./...
go vet ./...
```

Module floor is **Go 1.25**, matching `go-python-packaging`.

The module currently has **no dependencies**. `go-python-packaging` is added
when #18646 lands the interface that references `version.Version` — an unused
`require` would just be stripped by `go mod tidy`. When adding it, pin **v0.2.0
or later**: earlier tags predate the PEP 440/508 conformance work and contain a
local-label ordering bug.

## Code Style

- Follow standard Go conventions.
- **Formatting:** always run `gofmt` before committing. `gofmt -w .` to format
  in place, or `gofmt -l .` to list unformatted files (must print nothing).
- **License header:** every `.go` file begins with, as its first line:
  ```go
  // SPDX-License-Identifier: Apache-2.0 OR MIT
  ```
  This module is dual-licensed Apache-2.0 OR MIT (see `LICENSE-APACHE`,
  `LICENSE-MIT`, `NOTICE`). When code is adapted from another project, add its
  attribution to `NOTICE` in the same PR that lands the code.

### Verify lint matches CI before claiming "lint clean"

CI runs `golangci-lint` via `golangci/golangci-lint-action@v9`, pinned to
golangci-lint **v2.11.2**. Reproduce it exactly from the module root:

```bash
golangci-lint config verify   # config must load
golangci-lint run ./...
```

Run it **from the module root**. Passing a path outside the module prints a
reassuring `0 issues.` next to a typechecking error, having scanned nothing.

`.golangci.yml` uses the v2 schema, where `gofmt` is a **formatter** and lives
under the top-level `formatters:` block, never under `linters:`. Listing it
under `linters:` fails config load and turns CI red before any file is scanned.

## Versioning

No tags yet. When releasing: while the major version is `0`, breaking changes
take a **minor** bump (`0.1.x` → `0.2.0`), never a patch, and get a
`CHANGELOG.md` entry under an explicit `Breaking` heading. A consumer pinning
`^0.1` would otherwise pick up a breaking change silently.
