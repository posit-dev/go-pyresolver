// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package resolver is the public entry point of this module: give it PEP 508
// requirements and a MetadataIndex, get back one pinned version per package, or
// a failure explained in Python terms.
//
// This is the only package Package Manager is expected to import directly.
// Everything else in this module is machinery reachable through it, which keeps
// the supported surface small enough to evolve the internals without breaking
// the consumer. Resolve, Options, Resolution and ResolutionError are that
// surface.
//
// # PubGrub from day one, with no breadth-first phase
//
// Python requirements routinely produce transitive multi-version conflicts --
// foo>=1.0 together with a bar that transitively needs foo<1.0 -- and a naive
// walk over requires_dist would either silently produce a wrong closure or
// hard-fail on them. RFD 0001 Section 7 records the reasoning: shipping the
// naive version first would mean shipping known-broken behavior for non-trivial
// inputs.
//
// The same choice is what makes a failure explainable. PubGrub's derivation
// graph records exactly which facts forced the contradiction, so a
// ResolutionError can say "root-b 1.0 depends on middle <1.0, middle 0.9
// depends on shared <2.0, and root-a 1.0 depends on shared >=2.0" instead of
// "no solution found".
//
// # What a Resolution is, and what it is not
//
// It is versions: one per package the requirements transitively reach, plus
// which extras were activated and the order the solver decided them in.
//
// It is NOT a file list. Choosing between the wheels and sdists of a pinned
// version is a separate job, deferred rather than forgotten: the PyPI RSF
// carries no file records at all -- index.RSFIndex reports ErrFilesUnavailable
// -- so on the standalone path there is nothing to select between, and a
// selection policy written now could not be exercised against real data. See
// candidate's package documentation.
//
// Three further limits, all deliberate:
//
//   - One concrete environment per resolution. Options.Environment is a single
//     marker target; universal (environment-independent) resolution is deferred
//     by RFD 0001.
//   - No sdist building. A release whose dependency metadata cannot be read
//     without executing a build is not a candidate, and ResolutionError names
//     it rather than letting the report claim the version does not exist.
//   - No network and no database. Everything comes through
//     index.MetadataIndex, which is what lets the same resolver serve
//     connected Package Manager, air-gapped Package Manager, and a local file.
package resolver
