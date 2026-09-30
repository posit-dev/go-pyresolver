// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
)

// leaksVirtual reports whether text names a yank-pin virtual package.
func leaksVirtual(text string) bool {
	return strings.Contains(text, "exact pin on") || strings.Contains(text, "requirement on")
}

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
	if leaksVirtual(err.Error()) {
		t.Errorf("error names a virtual package: %v", err)
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

// app 2.0 pins yanked foo 1.0, so foo 1.0 gets a permit. foo 1.1 is yanked
// too and has no pinner; x needs it. The pin on 1.0 must not make 1.1 usable.
func TestPinnedVersionDoesNotPermitOtherYankedVersionOfSameName(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "app", "2.0", false, "foo==1.0")
	addRelease(t, idx, "app", "1.0", false)
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "foo", "1.1", true)
	addRelease(t, idx, "x", "1.0", false, "foo>=1.1")
	addRelease(t, idx, "x", "2.0", false, "foo>=1.1")
	addRelease(t, idx, "x", "3.0", false, "foo>=1.1")

	count, restore := resolver.CountSolves()
	defer restore()
	_, err := resolveYank(t, idx, true, "app", "foo>=0.5", "x")
	wantYankedKind(t, err, "foo", "1.1")
	if count() < 2 {
		t.Errorf("solves = %d, want the pin on foo 1.0 discovered (>= 2)", count())
	}
}

func permitLeakIndex(t *testing.T) *index.MockIndex {
	idx := newYankIndex()
	addRelease(t, idx, "a", "2.0", false, "foo==1.0", "c<2")
	addRelease(t, idx, "a", "1.0", false)
	addRelease(t, idx, "c", "1.0", false)
	addRelease(t, idx, "c", "2.0", false)
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "b", "1.0", false, "foo>=1")
	addRelease(t, idx, "b", "2.0", false, "foo>=1")
	addRelease(t, idx, "b", "3.0", false, "foo>=1")
	return idx
}

// The failure goes through foo 1.0's permit (its pinner a 2.0 needs c<2, the
// root wants c>=2). The report must name a 2.0 and never the virtual package.
func TestPermitFailureNamesThePin(t *testing.T) {
	count, restore := resolver.CountSolves()
	defer restore()
	_, err := resolveYank(t, permitLeakIndex(t), true, "a", "b", "c>=2")
	if err == nil {
		t.Fatal("want failure")
	}
	msg := err.Error()
	t.Log(msg)
	if count() < 2 {
		t.Fatalf("solves = %d, want the failure to come from a solve with the permit", count())
	}
	if strings.Contains(msg, "permit") || strings.Contains(msg, "version of an exact pin") || strings.Contains(msg, "\x00") {
		t.Errorf("error leaks a virtual package: %s", msg)
	}
	if !strings.Contains(msg, "a 2.0") {
		t.Errorf("error does not name the pinner a 2.0: %s", msg)
	}
}

// A successful resolution through a permit reports real packages only.
func TestPermitResolutionReportsOnlyRealPackages(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "x", "2.0", true)
	addRelease(t, idx, "x", "1.0", false, "a")
	addRelease(t, idx, "a", "1.0", false, "foo==1.0")

	res, err := resolveYank(t, idx, true, "x", "foo>=0.9")
	if err != nil {
		t.Fatal(err)
	}
	if count := len(res.Pinned) + len(res.YankedPins) + len(res.Unusable); count == 0 {
		t.Fatal("empty resolution")
	}
	if dump := fmt.Sprintf("%+v", res); strings.Contains(dump, "permit") || leaksVirtual(dump) {
		t.Errorf("resolution names a virtual package: %s", dump)
	}
}

// pip 26.2.1: c 2.0 needs foo>=1 and only yanked foo 1.0 matches, so pip backs
// off to c 1.0 -> a -> foo==1.0 and installs foo 1.0. Also with a clean foo
// 0.5 that c 2.0 cannot use.
func TestBackedOffRequirerStillPins(t *testing.T) {
	for _, clean := range []bool{false, true} {
		t.Run(fmt.Sprintf("clean_0.5=%v", clean), func(t *testing.T) {
			idx := newYankIndex()
			addRelease(t, idx, "c", "2.0", false, "foo>=1")
			addRelease(t, idx, "c", "1.0", false, "a")
			addRelease(t, idx, "a", "1.0", false, "foo==1.0")
			addRelease(t, idx, "foo", "1.0", true)
			if clean {
				addRelease(t, idx, "foo", "0.5", false)
			}

			_, err := resolveYank(t, idx, false, "c")
			wantYankedKind(t, err, "foo", "1.0")

			res, err := resolveYank(t, idx, true, "c")
			if err != nil {
				t.Fatal(err)
			}
			wantPins(t, res, map[string]string{"c": "1.0", "a": "1.0", "foo": "1.0"},
				[]pinView{{"foo", "1.0", "a 1.0"}})
		})
	}
}

// The case above with one more hop, c 1.0 -> e -> a. The need chain must run
// a -> e -> c -> root: one that stopped at e would let the solver keep c 2.0
// with an e nothing needs, and denying that edge would lose pip's answer.
func TestBackedOffRequirerTwoHops(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "c", "2.0", false, "foo>=1")
	addRelease(t, idx, "c", "1.0", false, "e")
	addRelease(t, idx, "e", "1.0", false, "a")
	addRelease(t, idx, "a", "1.0", false, "foo==1.0")
	addRelease(t, idx, "foo", "1.0", true)

	res, err := resolveYank(t, idx, true, "c")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res, map[string]string{"c": "1.0", "e": "1.0", "a": "1.0", "foo": "1.0"},
		[]pinView{{"foo", "1.0", "a 1.0"}})
}

// pip 26.2.1 case S: yanked foo 1.0 requires a, and a pins foo==1.0. Once c 2.0
// (which needs a and an unsatisfiable w) is gone, only foo 1.0 itself needs a,
// so a cannot vouch for it. pip installs b, c 1.0, foo 0.9 and no a.
func TestYankedVersionCannotJustifyItsOwnPinner(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true, "a")
	addRelease(t, idx, "a", "1.0", false, "foo==1.0")
	addRelease(t, idx, "b", "1.0", false, "foo")
	addRelease(t, idx, "c", "2.0", false, "a", "w")
	addRelease(t, idx, "c", "1.0", false)
	// Two releases of w, so the solver chooses a before it finds w unsatisfiable
	// and learns a's pin. pip's answer does not depend on it.
	addRelease(t, idx, "w", "2.0", false, "z>=9")
	addRelease(t, idx, "w", "1.0", false, "z>=9")
	addRelease(t, idx, "z", "1.0", false)

	count, restore := resolver.CountSolves()
	defer restore()
	res, err := resolveYank(t, idx, true, "b", "c")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res, map[string]string{"b": "1.0", "c": "1.0", "foo": "0.9"}, nil)
	if count() < 2 {
		t.Errorf("solves = %d, want a's pin learned (>= 2)", count())
	}
}

// pip 26.2.1 case U: p 1.0 and q 1.0 are yanked, and each one's only pinner is
// required only by the other. pip installs p 0.9 and q 0.9.
func TestMutuallyPinnedYankedVersionsNotSelected(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "p", "0.9", false)
	addRelease(t, idx, "p", "1.0", true, "qx")
	addRelease(t, idx, "q", "0.9", false)
	addRelease(t, idx, "q", "1.0", true, "px")
	addRelease(t, idx, "px", "1.0", false, "q==1.0")
	addRelease(t, idx, "qx", "1.0", false, "p==1.0")

	res, err := resolveYank(t, idx, true, "p", "q")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res, map[string]string{"p": "0.9", "q": "0.9"}, nil)
}

// The mutual case with both pins learned first, from an abandoned c 2.0. Each
// yanked version's pinner is then reachable only through the other yanked
// version. Same rule as case S: neither may be selected.
func TestMutuallyPinnedYankedVersionsWithLearnedPins(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true, "pb")
	addRelease(t, idx, "bar", "0.9", false)
	addRelease(t, idx, "bar", "1.0", true, "pf")
	addRelease(t, idx, "pf", "1.0", false, "foo==1.0")
	addRelease(t, idx, "pb", "1.0", false, "bar==1.0")
	addRelease(t, idx, "b", "1.0", false, "foo")
	addRelease(t, idx, "e", "1.0", false, "bar")
	addRelease(t, idx, "c", "2.0", false, "pf", "pb", "w")
	addRelease(t, idx, "c", "1.0", false)
	addRelease(t, idx, "w", "2.0", false, "z>=9")
	addRelease(t, idx, "w", "1.0", false, "z>=9")
	addRelease(t, idx, "z", "1.0", false)

	count, restore := resolver.CountSolves()
	defer restore()
	res, err := resolveYank(t, idx, true, "b", "e", "c")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res, map[string]string{"b": "1.0", "e": "1.0", "c": "1.0", "foo": "0.9", "bar": "0.9"}, nil)
	if count() < 2 {
		t.Errorf("solves = %d, want the pins learned (>= 2)", count())
	}
}

// a 2.0 pins foo==1.0 but is abandoned (c 2.0 needs a z that does not exist);
// d 1.0 pins it too and stays. YankedPins must name d 1.0 only.
func TestYankedPinsSkipsAbandonedPinner(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "a", "2.0", false, "foo==1.0", "c>=2")
	addRelease(t, idx, "a", "1.0", false)
	addRelease(t, idx, "c", "2.0", false, "z>=9")
	addRelease(t, idx, "z", "1.0", false)
	addRelease(t, idx, "d", "1.0", false, "foo==1.0")
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)

	res, err := resolveYank(t, idx, true, "a", "d")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res, map[string]string{"a": "1.0", "d": "1.0", "foo": "1.0"},
		[]pinView{{"foo", "1.0", "d 1.0"}})
}

// A failure whose proof runs through a need package: foo 1.0's pinner a is
// needed only by e 2.0, which needs an unsatisfiable w. The report must read
// in terms of real packages.
func TestNeedFailureReadsInRealPackages(t *testing.T) {
	idx := newYankIndex()
	// Three releases of c, so the solver takes e 2.0 and a (and learns a's
	// pin) before it looks at c.
	addRelease(t, idx, "c", "1.0", false, "foo>=1")
	addRelease(t, idx, "c", "2.0", false, "foo>=1")
	addRelease(t, idx, "c", "3.0", false, "foo>=1")
	addRelease(t, idx, "foo", "1.0", true)
	addRelease(t, idx, "e", "2.0", false, "a", "w")
	addRelease(t, idx, "e", "1.0", false)
	addRelease(t, idx, "a", "1.0", false, "foo==1.0")
	addRelease(t, idx, "w", "2.0", false, "z>=9")
	addRelease(t, idx, "w", "1.0", false, "z>=9")
	addRelease(t, idx, "z", "1.0", false)

	count, restore := resolver.CountSolves()
	defer restore()
	_, err := resolveYank(t, idx, true, "e", "c")
	if err == nil {
		t.Fatal("want failure")
	}
	msg := err.Error()
	t.Log(msg)
	if count() < 2 {
		t.Fatalf("solves = %d, want the failure to come from a solve with the pin", count())
	}
	for _, bad := range []string{"version of a requirement on", "version of an exact pin on", "\x00"} {
		if strings.Contains(msg, bad) {
			t.Errorf("error contains %q: %s", bad, msg)
		}
	}
}
