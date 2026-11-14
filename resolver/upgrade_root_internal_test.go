// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/tags"
	"github.com/posit-dev/go-python-packaging/version"
)

func rootTestOptions(t testing.TB) Options {
	t.Helper()
	env, err := marker.EnvironmentFromTarget(
		tags.Target{Implementation: "cp", PyMajor: 3, PyMinor: 11, OS: "linux", Arch: "x86_64"},
		marker.InterpreterIdentity{
			ImplementationName:           "cpython",
			PlatformPythonImplementation: "CPython",
			PythonFullVersion:            "3.11.4",
			ImplementationVersion:        "3.11.4",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return Options{Environment: env, PythonVersion: version.MustParse("3.11.4")}
}

// eagerRoot is the root logic from before the lazy scan (v0.20.0): it filters
// the candidates by the root's specifiers up front. Kept as the reference the
// lazy upRoot must agree with.
type eagerRoot struct {
	name  index.PackageName
	cands []version.Version
	zero  bool
	u     *usability
}

func newEagerRoot(
	ctx context.Context, idx index.MetadataIndex, u *usability,
	name index.PackageName, reqs []requirement.Requirement, pin version.Version,
) *eagerRoot {
	vs, _ := idx.Versions(ctx, name)
	r := &eagerRoot{name: name, u: u}
	for _, v := range vs {
		if v.IsPreRelease() && !pin.IsPreRelease() {
			continue
		}
		if admitsAll(reqs, v) {
			r.cands = append(r.cands, v)
		}
	}
	if len(r.cands) == 0 {
		return nil
	}
	slices.SortStableFunc(r.cands, func(a, b version.Version) int { return a.Compare(b) })
	_, maj, _ := release(r.top(pin))
	r.zero = maj == 0
	return r
}

func (r *eagerRoot) level(v version.Version) level {
	return (&upRoot{zero: r.zero}).level(v)
}

func (r *eagerRoot) top(pin version.Version) version.Version {
	for i := len(r.cands) - 1; i >= 0 && r.cands[i].GreaterThan(pin); i-- {
		if r.u.ok(r.name, r.cands[i]) {
			return r.cands[i]
		}
	}
	return pin
}

func (r *eagerRoot) behind(pin version.Version) (levels, versions int) {
	top := r.top(pin)
	if !pin.LessThan(top) {
		return 0, 0
	}
	for i := len(r.cands) - 1; i >= 0 && r.cands[i].GreaterThan(pin); i-- {
		if r.u.ok(r.name, r.cands[i]) {
			versions++
		}
	}
	return levelsBetween(r.level(top), r.level(pin)), versions
}

func (r *eagerRoot) bounds(pin version.Version) []version.Version {
	var out []version.Version
	pinLv := r.level(pin)
	for i := 0; i < len(r.cands); {
		lv := r.level(r.cands[i])
		j := i
		for j < len(r.cands) && r.level(r.cands[j]) == lv {
			j++
		}
		if pinLv.less(lv) {
			for _, c := range r.cands[i:j] {
				if c.GreaterThan(pin) && r.u.ok(r.name, c) {
					out = append(out, c)
					break
				}
			}
		}
		i = j
	}
	return out
}

// lazyAndEager builds both root kinds for one project over one usability.
func lazyAndEager(
	t testing.TB, idx index.MetadataIndex, spec string, pin version.Version,
) (*upRoot, *eagerRoot) {
	t.Helper()
	ctx := context.Background()
	opts := rootTestOptions(t)
	req, err := requirement.Parse("a" + spec)
	if err != nil {
		t.Fatal(err)
	}
	reqs := []requirement.Requirement{req}
	u := &usability{
		p:    provider.New(ctx, idx, providerOptions(reqs, opts)),
		memo: map[usableKey]bool{},
	}
	base := &Resolution{Pinned: map[index.PackageName]version.Version{"a": pin}}
	var lazy *upRoot
	if roots := upgradeRootSet(ctx, reqs, idx, opts, base, u); len(roots) > 0 {
		lazy = roots[0]
	}
	return lazy, newEagerRoot(ctx, idx, u, "a", reqs, pin)
}

func vstrings(vs []version.Version) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.String())
	}
	return out
}

// Each case is an edge the lazy scan could get wrong: it skips versions the
// specifiers do not admit while a level's run is open.
func TestUpgradeRootBounds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		versions    []string
		unavailable []string
		spec, pin   string
		want        []string
	}{
		{
			name:     "unusable first version of a level",
			versions: []string{"1.0", "2.0", "2.5", "3.0"}, unavailable: []string{"2.0"},
			pin: "1.0", want: []string{"2.5", "3.0"},
		},
		{
			name:     "specifier skips the middle of a level run",
			versions: []string{"1.0", "2.0", "2.1", "2.2", "3.0", "3.1"}, spec: "!=2.0,!=2.1,!=3.0",
			pin: "1.0", want: []string{"2.2", "3.1"},
		},
		{
			name:     "a whole level is skipped",
			versions: []string{"1.0", "1.5", "2.0", "2.5", "3.0"}, spec: "!=2.0,!=2.5",
			pin: "1.0", want: []string{"3.0"},
		},
		{
			name:     "pre-release pin counts pre-releases",
			versions: []string{"1.0a1", "1.0", "2.0a1", "2.0"}, spec: ">=1.0a1",
			pin: "1.0a1", want: []string{"2.0a1"},
		},
		{
			name:     "final pin ignores pre-releases",
			versions: []string{"1.0a1", "1.0", "2.0a1", "2.0"}, spec: ">=1.0a1",
			pin: "1.0", want: []string{"2.0"},
		},
		{
			name:     "equal-comparing versions are not above the pin",
			versions: []string{"1.0", "1.0.0", "2.0"},
			pin:      "1.0", want: []string{"2.0"},
		},
		{
			name:     "epoch is a level",
			versions: []string{"1.0", "2.0", "1!0.5", "1!1.0"},
			pin:      "2.0", want: []string{"1!0.5", "1!1.0"},
		},
		{
			name:     "local versions order after the public one",
			versions: []string{"1.0", "2.0+abc", "2.0+local"},
			pin:      "1.0", want: []string{"2.0+abc"},
		},
		{
			name:     "0.x levels are minors",
			versions: []string{"0.1", "0.2", "0.2.1", "0.3"},
			pin:      "0.1", want: []string{"0.2", "0.3"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := index.NewMockIndex("test")
			for _, v := range tc.versions {
				idx.AddVersion("a", v)
			}
			for _, v := range tc.unavailable {
				idx.SetUnavailable("a", v)
			}
			lazy, eager := lazyAndEager(t, idx, tc.spec, version.MustParse(tc.pin))
			pin := version.MustParse(tc.pin)
			if got := vstrings(lazy.bounds(pin)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("lazy bounds %v, want %v", got, tc.want)
			}
			if got := vstrings(eager.bounds(pin)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("reference bounds %v, want %v", got, tc.want)
			}
		})
	}
}

// The lazy root answers as the eager one did, on random roots (fixed seed).
func TestUpgradeRootMatchesEagerRoot(t *testing.T) {
	pool := []string{
		"0.1", "0.2", "0.2.1", "0.3", "0.4a1", "1.0", "1.0.0", "1.0.1", "1.5+abc", "1.5+xyz",
		"1.9rc1", "2.0", "2.0a1", "2.1.dev1", "3.0", "3.0+local", "1!0.5", "1!1.0", "2!0.1",
	}
	specs := []string{"", ">=1.0", "<2", "!=1.0", ">=0.2,<3", "!=2.0,!=3.0", ">=1.0a1", "<1!0"}
	rng := rand.New(rand.NewSource(94))
	checked := 0
	for range 1500 {
		idx := index.NewMockIndex("test")
		var added []string
		for _, i := range rng.Perm(len(pool))[:1+rng.Intn(10)] {
			idx.AddVersion("a", pool[i])
			added = append(added, pool[i])
			if rng.Intn(5) == 0 {
				idx.SetUnavailable("a", pool[i])
			}
		}
		spec := specs[rng.Intn(len(specs))]
		pin := version.MustParse(added[rng.Intn(len(added))])
		// base always pins a version the root's specifiers admit.
		if req, err := requirement.Parse("a" + spec); err != nil || !admitsAll([]requirement.Requirement{req}, pin) {
			continue
		}
		lazy, eager := lazyAndEager(t, idx, spec, pin)
		if eager == nil || lazy == nil {
			t.Fatalf("%v spec %q pin %v: root missing lazy=%v eager=%v", added, spec, pin, lazy != nil, eager != nil)
		}
		checked++
		desc := fmt.Sprintf("%v spec %q pin %v", added, spec, pin)
		if lazy.zero != eager.zero {
			t.Fatalf("%s: zero lazy=%v eager=%v", desc, lazy.zero, eager.zero)
		}
		if got, want := lazy.top(pin), eager.top(pin); !got.Equal(want) {
			t.Fatalf("%s: top %v, want %v", desc, got, want)
		}
		gl, gv := lazy.behind(pin)
		wl, wv := eager.behind(pin)
		if gl != wl || gv != wv {
			t.Fatalf("%s: behind (%d,%d), want (%d,%d)", desc, gl, gv, wl, wv)
		}
		if got := lazy.levelsBehind(pin); got != wl {
			t.Fatalf("%s: levelsBehind %d, want %d", desc, got, wl)
		}
		if got, want := vstrings(lazy.bounds(pin)), vstrings(eager.bounds(pin)); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: bounds %v, want %v", desc, got, want)
		}
	}
	if checked < 300 {
		t.Fatalf("only %d cases compared; the generator is too narrow", checked)
	}
}
