// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"testing"
)

func TestExactlyAndContains(t *testing.T) {
	one := Exactly(mustV(t, "1.0"))

	for _, in := range []string{"1.0", "1.0.0", "1.0.0.0", "1.0+local"} {
		if !one.Contains(mustV(t, in)) {
			t.Errorf("Exactly(1.0) should contain %s", in)
		}
	}
	for _, out := range []string{"1.0.post0", "1.0.post0.dev0", "1.0.1", "1.0rc1", "0.9"} {
		if one.Contains(mustV(t, out)) {
			t.Errorf("Exactly(1.0) should NOT contain %s", out)
		}
	}
}

func TestSingleton(t *testing.T) {
	v, ok := Exactly(mustV(t, "1.4")).Singleton()
	if !ok {
		t.Fatal("Exactly(1.4).Singleton() reported not-a-singleton")
	}
	if !v.Equal(mustV(t, "1.4")) {
		t.Errorf("Singleton() = %s, want 1.4", v)
	}

	if _, ok := All().Singleton(); ok {
		t.Error("All() must not report as a singleton")
	}
	if _, ok := Empty().Singleton(); ok {
		t.Error("Empty() must not report as a singleton")
	}
	two := Exactly(mustV(t, "1.0")).Union(Exactly(mustV(t, "2.0")))
	if _, ok := two.Singleton(); ok {
		t.Error("a two-version set must not report as a singleton")
	}
}
