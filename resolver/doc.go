// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package resolver is the public entry point of this module: give it
// requirements and a MetadataIndex, get back a resolved set of pinned versions
// and the files that satisfy them, or an explained failure.
//
// This is the only package PPM is expected to import directly. Everything else
// in this module is machinery reachable through it, which keeps the supported
// surface small enough to evolve the internals without breaking the consumer.
//
// The resolver is PubGrub from day one, with no breadth-first phase. Python
// requirements routinely produce transitive multi-version conflicts — foo>=1.0
// together with a bar that transitively needs foo<1.0 — and a naive walk over
// requires_dist would either silently produce a wrong closure or hard-fail on
// them. RFD 0001 Section 7 records the reasoning: shipping the naive version
// first would mean shipping known-broken behavior for non-trivial inputs.
package resolver
