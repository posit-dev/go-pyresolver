// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/version"
)

func gateReqs(t *testing.T, ss ...string) []requirement.Requirement {
	t.Helper()
	var out []requirement.Requirement
	for _, s := range ss {
		r, err := requirement.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// Root asks for foo[x]; yanked foo 1.0's extra x requires bar, which pins
// foo==1.0. Only that cycle pins foo, so the gate must reject it; an extra
// node not marked yanked would get in unpinned and let bar admit base foo.
func TestGateYankedExtraNeedsPin(t *testing.T) {
	var env marker.Environment
	idx := index.NewMockIndex("test").SetYanksCaptured(true)
	p := New(context.Background(), idx, Options{
		Environment:              env,
		Requirements:             gateReqs(t, "foo[x]"),
		YankExemptTransitivePins: true,
	})
	if p.yank == nil {
		t.Fatal("yank state not built")
	}
	one := version.MustParse("1.0")
	foo, fooX, bar := Project("foo"), WithExtra("foo", "x"), Project("bar")
	all := pep440set.All()

	p.yank.recordDecided(Root(), p.opts.RootVersion, []dependency{{Package: foo, Allowed: all}, {Package: fooX, Allowed: all}})
	p.yank.recordDecided(fooX, one, []dependency{{Package: foo, Allowed: pep440set.Exactly(one)}, {Package: bar, Allowed: all}})
	p.yank.recordDecided(foo, one, nil)
	p.yank.recordDecided(bar, one, []dependency{{Package: foo, Allowed: pep440set.Exactly(one)}})

	fooMeta := index.PackageMetadata{
		Yanked:        true,
		ProvidesExtra: []string{"x"},
		RequiresDist:  gateReqs(t, `bar; extra == "x"`),
	}
	p.yank.discover(foo, one, fooMeta, env)
	p.yank.discover(fooX, one, fooMeta, env)
	p.yank.discover(bar, one, index.PackageMetadata{RequiresDist: gateReqs(t, "foo==1.0")}, env)

	selected := map[Package]pep440set.Set{
		Root(): pep440set.Exactly(p.opts.RootVersion),
		foo:    pep440set.Exactly(one),
		fooX:   pep440set.Exactly(one),
		bar:    pep440set.Exactly(one),
	}
	inJ, bad := p.validateYankJustification(selected)
	if len(bad) != 3 {
		t.Fatalf("bad = %v (J = %v), want foo, foo[x] and bar outside J", bad, inJ)
	}

	// Control: a root pin on foo justifies the same selection.
	p.opts.Requirements = gateReqs(t, "foo[x]==1.0")
	if _, bad := p.validateYankJustification(selected); len(bad) != 0 {
		t.Fatalf("with a root pin, bad = %v, want none", bad)
	}
}
