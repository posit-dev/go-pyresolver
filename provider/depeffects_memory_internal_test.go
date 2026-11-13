// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

func atLeastSet(t *testing.T, lo string) pep440set.Set {
	t.Helper()
	specs, err := version.NewSpecifiers(">=" + lo)
	if err != nil {
		t.Fatal(err)
	}
	s, err := pep440set.FromSpecifiers(specs)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func reqsOf(t *testing.T, raw ...string) []requirement.Requirement {
	t.Helper()
	out := make([]requirement.Requirement, 0, len(raw))
	for _, s := range raw {
		r, err := requirement.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// A memo entry must not hold the version's requirements: each one pins its
// parsed marker and specifiers, and the memo keeps an entry per version read.
func TestDepEffectsHoldNoRequirements(t *testing.T) {
	typ := reflect.TypeOf(depEffects{})
	want := reflect.TypeOf([]requirement.Requirement(nil))
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type == want {
			t.Errorf("depEffects.%s holds the requirement slice", typ.Field(i).Name)
		}
	}
}

// An extra keeps a few of a long RequiresDist. The memoized entry must be sized
// to what it kept, and its expanded list must be the tail of deps, not a copy.
func TestBuiltEntryForExtraIsSmallAndShared(t *testing.T) {
	raw := []string{"a>=1", `b>=2; extra == "x"`, `c; extra == "y"`}
	for i := range 60 {
		raw = append(raw, fmt.Sprintf(`filler%d; extra == "z"`, i))
	}
	idx := index.NewMockIndex("test").SetMetadata("flask", "3.0", index.PackageMetadata{
		RequiresDist:  reqsOf(t, raw...),
		ProvidesExtra: []string{"x", "y", "z"},
	})
	p := New(context.Background(), idx, Options{})
	pkg, v := WithExtra("flask", "x"), version.MustParse("3.0")
	meta, err := idx.Metadata(context.Background(), "flask", v)
	if err != nil {
		t.Fatal(err)
	}

	b, err := p.builtEntry(pkg, v, meta)
	if err != nil || b.reason != "" {
		t.Fatalf("builtEntry: reason %q, err %v", b.reason, err)
	}
	// The extra's dependency list is unchanged: its base at the same version, and b.
	if len(b.deps) != 2 || b.deps[0].Package != Project("flask") || b.deps[1].Package != Project("b") {
		t.Fatalf("deps = %v, want [flask, b]", b.deps)
	}
	if got := cap(b.deps); got > 4 {
		t.Errorf("deps cap = %d for 2 entries; the memo keeps spare room", got)
	}
	if len(b.fx.expanded) != 1 || &b.fx.expanded[0] != &b.deps[1] {
		t.Errorf("fx.expanded is not the tail of deps")
	}
}

// A neighbour read shares built's slice rather than keeping a second copy.
func TestNeighbourSharesBuiltDeps(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("a", "1.0", "b>=1", "c<2")
	p := New(context.Background(), idx, Options{})
	budget := 5
	nb := p.neighbourDeps(Project("a"), version.MustParse("1.0"), &budget)
	if !nb.ok || len(nb.deps) != 2 {
		t.Fatalf("neighbour = %+v", nb)
	}
	key := nbrKey{Project("a"), "1.0"}
	if &p.nbrs[key].deps[0] != &p.built[key].deps[0] {
		t.Errorf("nbrs and built hold separate copies of the dependency list")
	}
}

// Under PrereleaseIfNoFinal a transitive requirement that names a pre-release
// still makes that package's pre-releases candidates, though the memo no longer
// keeps the requirements. This includes a version first read as a neighbour.
func TestPrereleaseNameNoticedFromMemoizedDeps(t *testing.T) {
	mk := func() *Provider {
		idx := index.NewMockIndex("test").
			AddVersion("lib", "1.0", "pre>=2.0rc1").
			AddVersion("pre", "1.0").
			AddVersion("pre", "2.0rc1")
		return New(context.Background(), idx, Options{PrereleaseIfNoFinal: true})
	}
	foundPre := func(p *Provider) bool {
		_, found, _, err := p.Candidates(Project("pre"), atLeastSet(t, "1.5"))
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	lib, v := Project("lib"), version.MustParse("1.0")

	p := mk()
	if foundPre(p) {
		t.Fatal("pre-release offered before any requirement named it")
	}
	if _, err := p.Dependencies(lib, pep440set.Exactly(v)); err != nil {
		t.Fatal(err)
	}
	if !foundPre(p) {
		t.Error("pre-release not offered after Dependencies read the requirement")
	}

	// Read first as a neighbour (records nothing), then selected.
	p = mk()
	budget := 5
	p.neighbourDeps(lib, v, &budget)
	if foundPre(p) {
		t.Fatal("a neighbour read must not record pre-release names")
	}
	if _, err := p.Dependencies(lib, pep440set.Exactly(v)); err != nil {
		t.Fatal(err)
	}
	if !foundPre(p) {
		t.Error("pre-release not offered when the deps came from the memo")
	}
}
