// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"context"
	"slices"
	"testing"

	"github.com/posit-dev/go-pyresolver/candidate"
	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/version"
)

func vs(t *testing.T, ss ...string) []version.Version {
	t.Helper()
	out := make([]version.Version, len(ss))
	for i, s := range ss {
		out[i] = version.MustParse(s)
	}
	return out
}

func strs(vv []version.Version) []string {
	out := make([]string, len(vv))
	for i, v := range vv {
		out[i] = v.String()
	}
	return out
}

// finalsFirst reorders in place but must give the same list as the copying
// partition it replaced.
func TestFinalsFirstInPlaceMatchesPartition(t *testing.T) {
	in := vs(t, "3.0rc1", "2.0", "2.0rc1", "1.5", "1.0a1", "1.0")
	want := []string{"2.0", "1.5", "1.0", "3.0rc1", "2.0rc1", "1.0a1"}

	got := finalsFirst("x", slices.Clone(in), candidate.EnabledPrereleases(nil, nil))
	if !slices.Equal(strs(got), want) {
		t.Errorf("finalsFirst = %v, want %v", strs(got), want)
	}
	// An enabled package keeps its order.
	enabled := candidate.EnabledPrereleases(nil, []index.PackageName{"x"})
	got = finalsFirst("x", slices.Clone(in), enabled)
	if !slices.Equal(strs(got), strs(in)) {
		t.Errorf("enabled finalsFirst = %v, want unchanged %v", strs(got), strs(in))
	}
}

// ascending shares the index list when it is already sorted, and must never
// reorder the list it was given.
func TestAscendingDoesNotReorderTheSource(t *testing.T) {
	p := &Provider{all: map[index.PackageName][]version.Version{}, asc: map[index.PackageName][]version.Version{}}
	sorted := vs(t, "1.0", "1.1", "2.0")
	unsorted := vs(t, "2.0", "1.0", "1.1")
	p.all["s"], p.all["u"] = sorted, unsorted

	if got := p.ascending("s"); !slices.Equal(strs(got), []string{"1.0", "1.1", "2.0"}) {
		t.Errorf("sorted: %v", strs(got))
	}
	if got := p.ascending("u"); !slices.Equal(strs(got), []string{"1.0", "1.1", "2.0"}) {
		t.Errorf("unsorted: %v", strs(got))
	}
	if !slices.Equal(strs(unsorted), []string{"2.0", "1.0", "1.1"}) {
		t.Errorf("ascending reordered its source: %v", strs(unsorted))
	}
}

// buildDeps is memoized: the second call does not rebuild, and each caller gets
// its own slice so an edit (widenDepender does one) cannot reach the memo.
func TestBuildDepsMemoizedAndCallerOwned(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("a", "1.0", "b>=1", "c<2")
	var env marker.Environment
	p := New(context.Background(), idx, Options{Environment: env})
	pkg, v := Project("a"), version.MustParse("1.0")
	meta, err := idx.Metadata(context.Background(), "a", v)
	if err != nil {
		t.Fatal(err)
	}

	d1, _, reason, err := p.buildDeps(pkg, v, meta)
	if err != nil || reason != "" || len(d1) != 2 {
		t.Fatalf("first build: %d deps, reason %q, err %v", len(d1), reason, err)
	}
	if len(p.built) != 1 {
		t.Fatalf("memo holds %d entries after one build, want 1", len(p.built))
	}
	d1[0].Depender = pep440set.Empty()
	d1[1] = dependency{}

	d2, _, _, err := p.buildDeps(pkg, v, meta)
	if err != nil || len(d2) != 2 {
		t.Fatalf("second build: %d deps, err %v", len(d2), err)
	}
	fresh, _, _, _ := p.buildDepsUncached(pkg, v, meta)
	for i := range fresh {
		if d2[i].Package != fresh[i].Package || !d2[i].Allowed.Equal(fresh[i].Allowed) || !d2[i].Depender.Equal(fresh[i].Depender) {
			t.Errorf("dep %d: memo hit differs from a fresh build (caller edit leaked)", i)
		}
	}
	if len(p.built) != 1 {
		t.Errorf("memo holds %d entries after a hit, want 1", len(p.built))
	}
}
