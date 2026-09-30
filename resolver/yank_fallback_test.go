// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/version"
)

// breakingIndex makes every version of brk require a missing package once
// broken is set, so a solve after that fails.
type breakingIndex struct {
	*index.MockIndex
	t      *testing.T
	brk    index.PackageName
	broken bool
}

func (b *breakingIndex) Metadata(ctx context.Context, pkg index.PackageName, v version.Version) (index.PackageMetadata, error) {
	meta, err := b.MockIndex.Metadata(ctx, pkg, v)
	if err == nil && b.broken && pkg == b.brk {
		meta = meta.Clone()
		meta.RequiresDist = append(meta.RequiresDist, mustRequirements(b.t, "missing")...)
	}
	return meta, err
}

// The first solve is valid (k 0.9) and learns k 1.0's pin on yanked
// m 1.0. If the next solve then fails, Resolve returns the first answer.
func TestLaterFailedSolveReturnsLastValid(t *testing.T) {
	mock := newYankIndex()
	addRelease(t, mock, "k", "1.0", false, "m==1.0")
	addRelease(t, mock, "k", "0.9", false)
	addRelease(t, mock, "m", "1.0", true)
	addRelease(t, mock, "m", "0.9", false)
	idx := &breakingIndex{MockIndex: mock, t: t, brk: index.NewPackageName("k")}

	res, err := resolveYank(t, idx, true, "k")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res, map[string]string{"k": "1.0", "m": "1.0"}, []pinView{{"m", "1.0", "k 1.0"}})

	solves := 0
	restore := resolver.OnSolve(func() {
		solves++
		idx.broken = solves >= 2
	})
	defer restore()
	res, err = resolveYank(t, idx, true, "k")
	if err != nil {
		t.Fatalf("solves=%d: %v", solves, err)
	}
	if solves < 2 {
		t.Fatalf("solves = %d, want a second solve", solves)
	}
	wantPins(t, res, map[string]string{"k": "0.9"}, nil)
}
