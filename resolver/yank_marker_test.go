// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import "testing"

// A root `==` whose marker is false for the target is not a requirement, so it
// must not exempt a yanked version, with the option off or on.
func TestInactiveRootPinDoesNotExempt(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "b", "1.0", false, "foo>=1.0")
	addRelease(t, idx, "foo", "1.0", true)

	for _, transitive := range []bool{false, true} {
		res, err := resolveYank(t, idx, transitive, "b", `foo==1.0; sys_platform == "nonexistent"`)
		if err == nil {
			t.Fatalf("transitive=%v: got %v, want a yanked failure", transitive, pins(t, res))
		}
		wantYankedKind(t, err, "foo", "1.0")
	}
}

// The same pin with a true marker still exempts.
func TestActiveRootPinExempts(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "b", "1.0", false, "foo>=1.0")
	addRelease(t, idx, "foo", "1.0", true)

	for _, transitive := range []bool{false, true} {
		res, err := resolveYank(t, idx, transitive, "b", `foo==1.0; sys_platform == "linux"`)
		if err != nil {
			t.Fatalf("transitive=%v: %v", transitive, err)
		}
		wantPins(t, res, map[string]string{"b": "1.0", "foo": "1.0"}, []pinView{{"foo", "1.0", "root"}})
	}
}

// A transitive `==` whose marker is false does not pin; a true one does.
func TestTransitivePinHonoursMarker(t *testing.T) {
	idx := newYankIndex()
	addRelease(t, idx, "app", "1.0", false, `foo==1.0; sys_platform == "nonexistent"`, "foo>=1.0")
	addRelease(t, idx, "foo", "1.0", true)
	_, err := resolveYank(t, idx, true, "app")
	wantYankedKind(t, err, "foo", "1.0")

	idx = newYankIndex()
	addRelease(t, idx, "app", "1.0", false, `foo==1.0; sys_platform == "linux"`)
	addRelease(t, idx, "foo", "0.9", false)
	addRelease(t, idx, "foo", "1.0", true)
	res, err := resolveYank(t, idx, true, "app")
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res, map[string]string{"app": "1.0", "foo": "1.0"}, []pinView{{"foo", "1.0", "app 1.0"}})
}
