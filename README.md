# go-pyresolver

Python dependency resolution for Go: the Python-aware adapter that drives a
generic [PubGrub](https://medium.com/@nex3/pubgrub-2fb6470504f) solver with PyPI
metadata.

Part of [RFD 0001 — Native Go PyPI Dependency Resolution](https://github.com/rstudio/package-manager/blob/main/docs/rfds/0001-pypi-native-resolver/README.md).

> **Status: partial.** `pypirsf/`, `index/`, and `cmd/pyresolve` are implemented
> and released in `v0.1.0`. `candidate/`, `provider/`, and `resolver/` are still
> documentation-only stubs with no declarations — **version solving is not
> implemented yet**, and importing those three gives you nothing. They are
> populated per RFD 0001's later phases. See [`CHANGELOG.md`](CHANGELOG.md).

## Where this sits

Three Posit-owned Go modules divide the work:

| Module | Role |
|---|---|
| [`go-python-packaging`](https://github.com/posit-dev/go-python-packaging) | PEP primitives — PEP 440 versions, PEP 508 requirements and markers, PEP 685 extras, wheel/METADATA parsing, compatibility tags |
| `go-pubgrub` | The version-solving algorithm, deliberately language-agnostic so it can serve future R or Julia resolution |
| **`go-pyresolver`** (this module) | The Python-aware glue between the two, plus metadata retrieval |

## Packages

| Package | Scope |
|---|---|
| `pypirsf/` | PyPI record layout and dependency-blob decoder for Repository Snapshot Format files |
| `index/` | `MetadataIndex` — the seam between resolver and storage — and its implementations |
| `candidate/` | Which version to try next, and which distribution file represents it |
| `provider/` | Adapts Python semantics (notably extras) to the generic solver |
| `resolver/` | The public entry point |

## Where dependency metadata comes from

Resolution needs two things: which versions of a package exist, and what each
version requires. Both live in a Repository Snapshot Format (RSF) file, so a
resolution reads one local file and makes no per-package network request. That is
what makes offline and reproducible resolution possible.

`pypirsf/` decodes that file. You supply the file; this module does not fetch it.

The resolver core never makes an HTTP request and never touches a database. It
calls `MetadataIndex`, and the implementation decides where the bytes come from.
That is what lets one resolver serve connected PPM, air-gapped PPM, local Python
sources, and tests.

## Build & test

```bash
go build ./...
go test ./...
go vet ./...
```

Module floor is **Go 1.25**, matching `go-python-packaging` (conservative for a
public library; PPM consumes it fine at 1.26).

## License

Dual-licensed under **Apache-2.0** OR **MIT** at your option. Every source file
carries `SPDX-License-Identifier: Apache-2.0 OR MIT`. See [`NOTICE`](NOTICE) for
attribution.
