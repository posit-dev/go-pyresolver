// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package provider adapts Python packaging semantics to the generic PubGrub
// solver in github.com/posit-dev/go-pubgrub.
//
// go-pubgrub is deliberately language-agnostic so it can serve future R or
// Julia resolution, which means it knows nothing about PEP 440 versions, PEP
// 508 markers, or extras. This package is the translation layer: it presents
// Python packages and version ranges to the solver in the solver's own terms,
// and translates the solver's questions back into MetadataIndex calls and
// candidate selection.
//
// The most significant translation is extras. Extras are supported from day
// one using the extras-as-virtual-packages pattern that Poetry and uv both
// use: a request for pkg[security] becomes a distinct virtual package that
// depends on both pkg at the same version and on whatever security pulls in.
// Modeling extras this way lets the generic solver handle them with no
// Python-specific knowledge. Omitting extras would silently produce incomplete
// closures for anyone using [extras] syntax, which RFD 0001 Section 7 judges
// worse than the status quo.
//
// This package is also where a failed resolution is turned into an
// explanation. PubGrub's derivation graph is what makes good error messages
// possible, but the raw graph is expressed in solver terms; rendering it as
// something a Python user recognizes belongs here.
package provider
