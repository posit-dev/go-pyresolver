// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/resolver"
)

// wantNoMatch checks that the resolve failed as a ResolutionError naming pkg,
// not with the old "no version-set equivalent" abort.
func wantNoMatch(t *testing.T, err error, pkg string) {
	t.Helper()
	var re *resolver.ResolutionError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *resolver.ResolutionError", err)
	}
	if !strings.Contains(err.Error(), pkg) {
		t.Errorf("error %q does not name %s", err, pkg)
	}
	if strings.Contains(err.Error(), "no version-set equivalent") {
		t.Errorf("error %q is the old unrepresentable abort", err)
	}
}

func TestRootArbitraryEqualityPinsExactVersion(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", false)

	res, err := resolveYank(t, idx, false, "foo===1.0")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pins(t, res), map[string]string{"foo": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pins = %v, want %v", got, want)
	}
}

func TestRootArbitraryEqualityKeepsYankedPin(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)

	res, err := resolveYank(t, idx, false, "foo===1.0")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pins(t, res), map[string]string{"foo": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pins = %v, want %v", got, want)
	}
	want := []pinView{{"foo", "1.0", "root"}}
	if got := pinViews(res); !reflect.DeepEqual(got, want) {
		t.Errorf("YankedPins = %v, want %v", got, want)
	}

	// Control: a range does not exempt the yanked version, so the filter is live.
	res, err = resolveYank(t, idx, false, "foo>=0.9")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pins(t, res), map[string]string{"foo": "0.9"}; !reflect.DeepEqual(got, want) {
		t.Errorf("control pins = %v, want %v", got, want)
	}
}

// `===1.0` compares strings, so it must not match 1.0.0; `==1.0` does.
func TestRootArbitraryEqualityIsStringEquality(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "1.0.0", false)

	_, err := resolveYank(t, idx, false, "foo===1.0")
	wantNoMatch(t, err, "foo")

	res, err := resolveYank(t, idx, false, "foo==1.0")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pins(t, res), map[string]string{"foo": "1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("== pins = %v, want %v", got, want)
	}
}

func TestRootArbitraryEqualityIsCaseInsensitive(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0rc1", false)

	res, err := resolveYank(t, idx, false, "foo===1.0RC1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pins(t, res), map[string]string{"foo": "1.0rc1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pins = %v, want %v", got, want)
	}
}

func TestRootArbitraryEqualityPinsPrerelease(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "1.0", false)
	addRelease(t, idx, "foo", "2.0b1", false)

	res, err := resolveYank(t, idx, false, "foo===2.0b1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pins(t, res), map[string]string{"foo": "2.0b1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pins = %v, want %v", got, want)
	}
}

func TestRootArbitraryEqualityFalseMarkerIsIgnored(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "1.0", false)
	addRelease(t, idx, "bar", "1.0", false)

	res, err := resolveYank(t, idx, false, `foo===9.9; python_version < "3"`, "bar")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pins(t, res), map[string]string{"bar": "1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pins = %v, want %v", got, want)
	}
}

func TestRootArbitraryEqualityNoMatch(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "foo", "1.0", false)

	_, err := resolveYank(t, idx, false, "foo===2.0")
	wantNoMatch(t, err, "foo")

	_, err = resolveYank(t, idx, false, "nosuch===1.0")
	wantNoMatch(t, err, "nosuch")
}
