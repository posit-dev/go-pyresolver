// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver

import (
	"reflect"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
	"github.com/posit-dev/go-python-packaging/version"
)

// fullResolution fills every map and slice field of a Resolution.
func fullResolution() *Resolution {
	a := index.NewPackageName("a")
	v := version.MustParse("1.0")
	root := Requester{Root: true}
	return &Resolution{
		Pinned:        map[index.PackageName]version.Version{a: v},
		Order:         []index.PackageName{a},
		Extras:        map[index.PackageName][]string{a: {"x"}},
		Unusable:      []provider.Unusable{{Package: provider.Project(a), Version: v, Reason: "r"}},
		MissingExtras: []MissingExtra{{Package: a, Version: v, Extra: "y", RequestedBy: root}},
		YankedPins:    []YankedPin{{Package: a, Version: v, RequestedBy: root}},
	}
}

func TestCloneResolutionSharesNothing(t *testing.T) {
	r, want := fullResolution(), fullResolution()
	c := cloneResolution(r)
	a := index.NewPackageName("a")
	c.Pinned[a] = version.MustParse("9.0")
	c.Order[0] = index.NewPackageName("z")
	c.Extras[a][0] = "changed"
	c.Unusable[0].Reason = "changed"
	c.MissingExtras[0].Extra = "changed"
	c.YankedPins[0].Package = index.NewPackageName("z")
	if !reflect.DeepEqual(r, want) {
		t.Errorf("changing the copy changed the original: %+v", r)
	}
}

// Fails when Resolution gains a map, slice or pointer field that
// cloneResolution does not copy.
func TestCloneResolutionCoversEveryField(t *testing.T) {
	r := fullResolution()
	c := cloneResolution(r)
	rv, cv := reflect.ValueOf(r).Elem(), reflect.ValueOf(c).Elem()
	for i := range rv.NumField() {
		name := rv.Type().Field(i).Name
		f, g := rv.Field(i), cv.Field(i)
		switch f.Kind() {
		case reflect.Map, reflect.Slice:
			if f.Len() == 0 {
				t.Errorf("%s: fill it in fullResolution", name)
				continue
			}
			if f.UnsafePointer() == g.UnsafePointer() {
				t.Errorf("%s: cloneResolution shares it", name)
			}
			if f.Kind() == reflect.Map && f.Type().Elem().Kind() == reflect.Slice {
				for _, k := range f.MapKeys() {
					if f.MapIndex(k).UnsafePointer() == g.MapIndex(k).UnsafePointer() {
						t.Errorf("%s[%v]: cloneResolution shares the inner slice", name, k)
					}
				}
			}
		case reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func:
			if name != "Upgrade" { // nil on the answer upgradeRoots copies
				t.Errorf("%s: decide whether cloneResolution must copy it", name)
			}
		}
	}
}
