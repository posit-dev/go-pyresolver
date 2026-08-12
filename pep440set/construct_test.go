// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"errors"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
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

	// A local-bearing Exactly is a singleton too, and it is the one whose
	// upper bound is edgeAboveExact rather than edgeAboveLocals.
	withLocal, ok := Exactly(mustV(t, "1.4+a")).Singleton()
	if !ok {
		t.Fatal("Exactly(1.4+a).Singleton() reported not-a-singleton")
	}
	if !withLocal.Equal(mustV(t, "1.4+a")) {
		t.Errorf("Singleton() = %s, want 1.4+a", withLocal)
	}
}

// TestExactlyWithLocal: an operand carrying a local label matches that label
// and no other, which is why Exactly cannot always stop at edgeAboveLocals.
func TestExactlyWithLocal(t *testing.T) {
	one := Exactly(mustV(t, "1.0+a"))

	if !one.Contains(mustV(t, "1.0+a")) {
		t.Error("Exactly(1.0+a) should contain 1.0+a")
	}
	for _, out := range []string{"1.0", "1.0+b", "1.0+zzz", "1.0.0"} {
		if one.Contains(mustV(t, out)) {
			t.Errorf("Exactly(1.0+a) should NOT contain %s", out)
		}
	}
}

func TestFromSpecifiers(t *testing.T) {
	cases := []struct {
		spec string
		in   []string
		out  []string
	}{
		{">=1.0", []string{"1.0", "1.5", "2.0rc1"}, []string{"0.9", "1.0rc1"}},
		// ⚠️ `<2.0` does NOT admit 2.0rc1. PEP 440's "<V MUST NOT allow a
		// pre-release of V" is a matching rule, and Specifiers.Check applies it
		// under every pre-release policy.
		{"<2.0", []string{"1.9", "1.9.9"}, []string{"2.0rc1", "2.0.dev0", "2.0", "2.1"}},
		{"<2.0rc1", []string{"1.9", "2.0.dev0"}, []string{"2.0rc1", "2.0"}},
		{">1.0", []string{"1.1", "1.0.1"}, []string{"1.0", "1.0.post1", "1.0+a"}},
		{">1.0rc1", []string{"1.0", "1.0.post1", "1.0rc2"}, []string{"1.0rc1", "1.0rc1+l", "1.0rc1.post1"}},
		{">1.0.post1", []string{"1.0.post2", "1.1"}, []string{"1.0.post1", "1.0.post1+l", "1.0"}},
		{"<=1.0", []string{"1.0", "1.0+l", "0.9"}, []string{"1.0.post1", "1.1"}},
		{"==1.0", []string{"1.0", "1.0.0", "1.0+a"}, []string{"1.0.post0", "1.0.1"}},
		{"==1.0+a", []string{"1.0+a"}, []string{"1.0", "1.0+b"}},
		{"!=1.0", []string{"1.0.post0", "1.0.1", "0.9"}, []string{"1.0", "1.0+a"}},
		{"==1.*", []string{"1.0", "1.9.9", "1.0rc1"}, []string{"0.9", "2.0"}},
		{"~=2.2", []string{"2.2", "2.2.1", "2.9"}, []string{"2.1", "3.0"}},
		{"~=2.2.3", []string{"2.2.3", "2.2.9"}, []string{"2.2", "2.3"}},
		// post1 is not a release segment, so the prefix is 1.*, not 1.0.*.
		{"~=1.0.post1", []string{"1.0.post1", "1.1", "1.9"}, []string{"1.0", "2.0"}},
		{">=1.0,<2.0", []string{"1.0", "1.9"}, []string{"0.9", "2.0"}},
	}

	for _, tc := range cases {
		ss, err := version.NewSpecifiers(tc.spec)
		if err != nil {
			t.Fatalf("%s: NewSpecifiers: %v", tc.spec, err)
		}
		set, err := FromSpecifiers(ss)
		if err != nil {
			t.Fatalf("%s: FromSpecifiers: %v", tc.spec, err)
		}
		for _, in := range tc.in {
			if !set.Contains(mustV(t, in)) {
				t.Errorf("%s should contain %s", tc.spec, in)
			}
		}
		for _, out := range tc.out {
			if set.Contains(mustV(t, out)) {
				t.Errorf("%s should NOT contain %s", tc.spec, out)
			}
		}
	}
}

func TestFromSpecifiersArbitraryEquality(t *testing.T) {
	ss, err := version.NewSpecifiers("===lolwat")
	if err != nil {
		t.Fatalf("NewSpecifiers: %v", err)
	}
	if _, err := FromSpecifiers(ss); !errors.Is(err, ErrUnrepresentable) {
		t.Errorf("FromSpecifiers(===lolwat) err = %v, want ErrUnrepresentable", err)
	}

	// Even a well-formed operand is unrepresentable: === is string equality,
	// so ===1.0 rejects 1.0.0 while any set containing 1.0 accepts it.
	ss, err = version.NewSpecifiers("===1.0")
	if err != nil {
		t.Fatalf("NewSpecifiers: %v", err)
	}
	if _, err := FromSpecifiers(ss); !errors.Is(err, ErrUnrepresentable) {
		t.Errorf("FromSpecifiers(===1.0) err = %v, want ErrUnrepresentable", err)
	}
}

// TestFromSpecifiersOrGroups: the `||` OR-of-ANDs form has no PEP 440 spelling
// and no exported accessor, and List flattens it, so refusing it is the only
// way not to answer the opposite of what it means.
func TestFromSpecifiersOrGroups(t *testing.T) {
	ss, err := version.NewRSpecifiers(">=1.0||<0.5", func(s string) string { return s })
	if err != nil {
		t.Fatalf("NewRSpecifiers: %v", err)
	}
	if _, err := FromSpecifiers(ss); !errors.Is(err, ErrUnrepresentable) {
		t.Errorf("FromSpecifiers(>=1.0||<0.5) err = %v, want ErrUnrepresentable", err)
	}
}

func TestFromSpecifiersEmptyIsAll(t *testing.T) {
	var zero version.Specifiers
	set, err := FromSpecifiers(zero)
	if err != nil {
		t.Fatalf("FromSpecifiers(zero): %v", err)
	}
	if !set.Equal(All()) {
		t.Error("an empty specifier set must admit every version")
	}
}
