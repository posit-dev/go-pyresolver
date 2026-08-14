// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"sync/atomic"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/version"
)

// countingIndex counts what a provider asks of its index.
//
// # Why this exists here when resolver/ already has one
//
// The resolver's version is in package resolver_test and measures a whole
// resolution. This one measures a single Provider, which is what a memo on the
// Provider has to be tested against. Duplicating ~30 lines is the smaller cost:
// the alternative is exporting a test helper across package boundaries, which
// would make a benchmark's instrumentation part of a differential's contract.
//
// ⚠️ It exists because a test that does not count index calls cannot tell a memo
// that is READ from one that is merely WRITTEN. An earlier version of the memo
// differential reported "calls served from a warm memo" as calls-minus-packages,
// which is arithmetic over its own loop shape: neuter the memo lookup so every
// call misses and that test still passes, still printing the same number. It had
// been quoted in a changelog as a measurement. See
// TestCandidatesAgreeAcrossRepeatedCallsWithDifferentRanges.
type countingIndex struct {
	inner index.MetadataIndex

	versions atomic.Int64
	metadata atomic.Int64
	files    atomic.Int64
}

func newCountingIndex(inner index.MetadataIndex) *countingIndex {
	return &countingIndex{inner: inner}
}

func (c *countingIndex) Versions(ctx context.Context, pkg index.PackageName) ([]version.Version, error) {
	c.versions.Add(1)
	return c.inner.Versions(ctx, pkg)
}

func (c *countingIndex) Metadata(ctx context.Context, pkg index.PackageName, ver version.Version) (index.PackageMetadata, error) {
	c.metadata.Add(1)
	return c.inner.Metadata(ctx, pkg, ver)
}

func (c *countingIndex) Files(ctx context.Context, pkg index.PackageName, ver version.Version) ([]index.DistFile, error) {
	c.files.Add(1)
	return c.inner.Files(ctx, pkg, ver)
}
