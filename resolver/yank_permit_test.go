// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/resolver"
)

// wantPins checks the exact selected set and YankedPins.
func wantPins(t *testing.T, res *resolver.Resolution, want map[string]string, yanked []pinView) {
	t.Helper()
	if got := pins(t, res); !reflect.DeepEqual(got, want) {
		t.Errorf("pinned = %v, want %v", got, want)
	}
	if got := pinViews(res); !reflect.DeepEqual(got, yanked) {
		t.Errorf("YankedPins = %v, want %v", got, yanked)
	}
}

// The reviewer's case: a 2.0 pins yanked foo 1.0 but is abandoned (it needs
// c>=2, and c 2.0 needs a z that does not exist). Its pin must not drop the
// live pin d 1.0 -> bar==1.0. pip 26.2.1 installs exactly this.
func TestAbandonedPinnerKeepsLivePins(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "a", "2.0", false, "foo==1.0", "c>=2")
	addRelease(t, idx, "a", "1.0", false)
	addRelease(t, idx, "c", "2.0", false, "z>=9")
	addRelease(t, idx, "z", "1.0", false)
	addRelease(t, idx, "b", "1.0", false, "foo")
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "d", "1.0", false, "bar==1.0")
	addRelease(t, idx, "bar", "1.0", true)

	res, err := resolveYank(t, idx, true, "a", "b", "d")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res,
		map[string]string{"a": "1.0", "b": "1.0", "d": "1.0", "foo": "0.9", "bar": "1.0"},
		[]pinView{{"bar", "1.0", "d 1.0"}})
}

// foo 1.0's only pinner, a, sits behind c 2.0, which is abandoned. A permit
// could still pull a in just to allow foo 1.0; pip would not install a, so
// neither may we.
func TestAbandonedPinnerOnlyDoesNotSelectYanked(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "b", "1.0", false, "foo")
	addRelease(t, idx, "c", "2.0", false, "a", "w")
	addRelease(t, idx, "c", "1.0", false)
	addRelease(t, idx, "a", "1.0", false, "foo==1.0")
	// Two releases of w, so the solver chooses a (one release) before it finds
	// w unsatisfiable.
	addRelease(t, idx, "w", "2.0", false, "z>=9")
	addRelease(t, idx, "w", "1.0", false, "z>=9")
	addRelease(t, idx, "z", "1.0", false)
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)

	count, restore := resolver.CountSolves()
	defer restore()
	res, err := resolveYank(t, idx, true, "b", "c")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res, map[string]string{"b": "1.0", "c": "1.0", "foo": "0.9"}, nil)
	if count() < 2 {
		t.Errorf("solves = %d, want the pin from abandoned a to be discovered (>= 2)", count())
	}
}

// pip 26.2.1 case M2: x 2.0 is yanked, x 1.0 -> a -> foo==1.0 (yanked). The
// pin is found only once a is chosen, so this needs a second solve.
func TestTransitivePinDiscoveredBehindYankedParent(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "x", "2.0", true)
	addRelease(t, idx, "x", "1.0", false, "a")
	addRelease(t, idx, "a", "1.0", false, "foo==1.0")

	_, err := resolveYank(t, idx, false, "x", "foo>=0.9")
	wantYankedKind(t, err, "foo", "1.0")

	res, err := resolveYank(t, idx, true, "x", "foo>=0.9")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res, map[string]string{"x": "1.0", "a": "1.0", "foo": "1.0"},
		[]pinView{{"foo", "1.0", "a 1.0"}})
}

// pip 26.2.1 case M: root asks foo>=1.0, and the only pinner of yanked foo 1.0
// is behind x. pip fails (no matching distribution for foo>=1.0); so do we,
// because foo has no usable version before a is ever chosen.
func TestRangeOnlyRootNeedsPinnerNobodyChoseFails(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "x", "2.0", true)
	addRelease(t, idx, "x", "1.0", false, "a")
	addRelease(t, idx, "a", "1.0", false, "foo==1.0")

	_, err := resolveYank(t, idx, true, "foo>=1.0", "x")
	wantYankedKind(t, err, "foo", "1.0")
}

// a 2.0 is probed (as a candidate for "a") but never chosen, because c needs
// a<2. Its pin must not count: one solve, foo 0.9.
func TestProbedPinnerIsNotDiscovered(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "a", "2.0", false, "foo==1.0")
	addRelease(t, idx, "a", "1.0", false)
	addRelease(t, idx, "c", "1.0", false, "a<2")
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)

	count, restore := resolver.CountSolves()
	defer restore()
	res, err := resolveYank(t, idx, true, "a", "c", "foo")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res, map[string]string{"a": "1.0", "c": "1.0", "foo": "0.9"}, nil)
	if count() != 1 {
		t.Errorf("solves = %d, want 1", count())
	}
}

// Option off: one solve, no permit package in the result or the error.
func TestOptionOffSolvesOnce(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "app", "1.0", false, "foo==1.0")
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)

	count, restore := resolver.CountSolves()
	defer restore()
	_, err := resolveYank(t, idx, false, "app")
	wantYankedKind(t, err, "foo", "1.0")
	if count() != 1 {
		t.Errorf("failing solve: solves = %d, want 1", count())
	}
	if strings.Contains(err.Error(), "exact pin to") {
		t.Errorf("error names a permit package: %v", err)
	}

	res, err := resolveYank(t, idx, false, "app", "foo==1.0")
	if err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Errorf("succeeding solve: solves = %d, want 1 more", count()-1)
	}
	wantPins(t, res, map[string]string{"app": "1.0", "foo": "1.0"}, []pinView{{"foo", "1.0", "root"}})
}

// With the option on, a failure that goes through a permit names the pinner,
// not the virtual package.
func TestPermitFailureNamesThePin(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "a", "2.0", false, "foo==1.0", "q")
	addRelease(t, idx, "q", "1.0", false, "foo<1")
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "b", "1.0", false, "foo>=1")

	_, err := resolveYank(t, idx, true, "a", "b")
	if err == nil {
		t.Fatal("want failure")
	}
	msg := err.Error()
	t.Log(msg)
	if strings.Contains(msg, "permit") {
		t.Errorf("error leaks the permit package: %s", msg)
	}
}
