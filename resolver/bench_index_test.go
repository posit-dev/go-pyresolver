// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"sync/atomic"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-python-packaging/version"
)

// countingIndex counts what a resolution asks of its index.
//
// A decorator rather than a change to any index implementation: the thing being
// measured is the resolver's call pattern, and instrumenting an implementation
// would both measure the wrong layer and make the measurement unavailable for
// every other MetadataIndex.
//
// # Why the call count is the number that matters
//
// Wall time on one machine says whether the gate was met today. The call count
// says WHY, and it is the same on every machine: it is a property of the
// resolver's algorithm, not of the hardware, the page cache, or the snapshot's
// compression. A change that halves the time on an M4 Max and leaves Metadata
// at 14,000 calls has moved the constant; a change that takes Metadata to 200
// has moved the algorithm.
//
// Counters are atomic because MetadataIndex is documented as safe for
// concurrent use and a future resolver is free to look ahead. Today's solver is
// single-threaded, so this costs nothing measurable.
type countingIndex struct {
	inner index.MetadataIndex

	versions atomic.Int64
	metadata atomic.Int64
	files    atomic.Int64

	// candidates is the total number of versions handed back by Versions,
	// summed over calls. It is the denominator for the question the benchmark
	// exists to answer: how does the work scale with the size of the version
	// lists the resolution walks?
	candidates atomic.Int64

	// errs counts lookups that came back with an error of any kind, including
	// the ordinary ones (ErrPackageNotFound, ErrMetadataUnavailable). A
	// resolution doing most of its work on error paths is measuring something
	// other than resolution, and without this the benchmark could not tell.
	errs atomic.Int64
}

func newCountingIndex(inner index.MetadataIndex) *countingIndex {
	return &countingIndex{inner: inner}
}

func (c *countingIndex) Versions(ctx context.Context, pkg index.PackageName) ([]version.Version, error) {
	c.versions.Add(1)
	vs, err := c.inner.Versions(ctx, pkg)
	if err != nil {
		c.errs.Add(1)
		return vs, err
	}
	c.candidates.Add(int64(len(vs)))
	return vs, nil
}

func (c *countingIndex) Metadata(
	ctx context.Context,
	pkg index.PackageName,
	ver version.Version,
) (index.PackageMetadata, error) {
	c.metadata.Add(1)
	meta, err := c.inner.Metadata(ctx, pkg, ver)
	if err != nil {
		c.errs.Add(1)
	}
	return meta, err
}

func (c *countingIndex) Files(
	ctx context.Context,
	pkg index.PackageName,
	ver version.Version,
) ([]index.DistFile, error) {
	c.files.Add(1)
	fs, err := c.inner.Files(ctx, pkg, ver)
	if err != nil {
		c.errs.Add(1)
	}
	return fs, err
}

// counts is a snapshot of one countingIndex, summed across benchmark
// iterations.
type counts struct {
	versions   int64
	metadata   int64
	files      int64
	candidates int64
	errs       int64
}

func (c *counts) add(from *countingIndex) {
	c.versions += from.versions.Load()
	c.metadata += from.metadata.Load()
	c.files += from.files.Load()
	c.candidates += from.candidates.Load()
	c.errs += from.errs.Load()
}

// total is every MetadataIndex call, which is the headline "index calls per
// resolve" figure.
func (c *counts) total() int64 { return c.versions + c.metadata + c.files }
