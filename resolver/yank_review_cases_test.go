// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/posit-dev/go-pyresolver/resolver"
)

func mustCase(t *testing.T, js string) yfCase {
	t.Helper()
	var c yfCase
	if err := json.Unmarshal([]byte(js), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// YP: yanked foo 1.0 and a 1.0 pin each other, and their other pinner, d 2.0,
// is abandoned (w is unsatisfiable). pip 26.2.1 installs exactly this.
func TestMutualYankedPinnersDoNotVouchForEachOther(t *testing.T) {
	c := mustCase(t, `{"root": ["b", "c", "d"], "packages": {
	 "b": {"1.0": {"requires": ["foo"]}},
	 "c": {"1.0": {"requires": ["a"]}},
	 "foo": {"0.9": {}, "1.0": {"yanked": true, "requires": ["a==1.0"]}},
	 "a": {"0.9": {}, "1.0": {"yanked": true, "requires": ["foo==1.0"]}},
	 "d": {"2.0": {"requires": ["foo==1.0", "a==1.0", "w"]}, "1.0": {}},
	 "w": {"2.0": {"requires": ["z>=9"]}, "1.0": {"requires": ["z>=9"]}},
	 "z": {"1.0": {}}}}`)
	res, err := resolveYank(t, indexFromCase(t, c), true, c.Root...)
	if err != nil {
		t.Fatal(err)
	}
	wantPins(t, res,
		map[string]string{"b": "1.0", "c": "1.0", "d": "1.0", "foo": "0.9", "a": "0.9"}, nil)
}

// DL: k 1.0 pins yanked m, m pins yanked n, n pins yanked t, t -> r -> a, and a
// pins yanked foo 1.0. All of it is justified through k's pins, and pip 26.2.1
// installs it (with b 1.0, c 1.0).
func TestDeepYankedPinChain(t *testing.T) {
	c := mustCase(t, `{"root": ["b", "c", "k"], "packages": {
	 "b": {"1.0": {"requires": ["foo"]}},
	 "foo": {"0.9": {}, "1.0": {"yanked": true, "requires": ["s"]}},
	 "s": {"1.0": {"requires": ["r"]}},
	 "r": {"1.0": {"requires": ["a"]}},
	 "a": {"1.0": {"requires": ["foo==1.0"]}},
	 "c": {"2.0": {"requires": ["a", "w"]}, "1.0": {}},
	 "w": {"2.0": {"requires": ["z>=9"]}, "1.0": {"requires": ["z>=9"]}},
	 "z": {"1.0": {}},
	 "k": {"1.0": {"requires": ["m==1.0"]}, "0.9": {}},
	 "m": {"1.0": {"yanked": true, "requires": ["n==1.0"]}},
	 "n": {"1.0": {"yanked": true, "requires": ["t==1.0"]}},
	 "t": {"1.0": {"yanked": true, "requires": ["r"]}}}}`)
	res, err := resolveYank(t, indexFromCase(t, c), true, c.Root...)
	if err != nil {
		t.Fatal(err)
	}
	if got := pins(t, res); !oracleValid(c, got, false) {
		t.Fatalf("DL result %v is not valid per the rule", got)
	}
	wantPins(t, res, map[string]string{
		"b": "1.0", "c": "1.0", "k": "1.0", "m": "1.0", "n": "1.0", "t": "1.0",
		"r": "1.0", "a": "1.0", "s": "1.0", "foo": "1.0",
	}, []pinView{
		{"foo", "1.0", "a 1.0"}, {"m", "1.0", "k 1.0"}, {"n", "1.0", "m 1.0"}, {"t", "1.0", "n 1.0"},
	})
}

// Each ri requires a, a pins yanked foo 1.0, and foo 1.0 requires every ri, so
// nothing reaches them from the root. One round denies all of them.
func TestManyUnjustifiedRequirersDeniedTogether(t *testing.T) {
	const n = 60
	idx := newYankIndex()
	addRelease(t, idx, "b", "1.0", false, "foo")
	addRelease(t, idx, "foo", "0.9", false)
	var rs []string
	for i := range n {
		name := fmt.Sprintf("r%d", i)
		rs = append(rs, name)
		addRelease(t, idx, name, "1.0", false, "a")
	}
	addRelease(t, idx, "foo", "1.0", true, rs...)
	addRelease(t, idx, "a", "1.0", false, "foo==1.0")
	addRelease(t, idx, "c", "2.0", false, "a", "w")
	addRelease(t, idx, "c", "1.0", false)
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
	if count() > 8 {
		t.Errorf("solves = %d, want the denials batched (<= 8)", count())
	}
}
