// SPDX-License-Identifier: Apache-2.0 OR MIT

package pep440set

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/posit-dev/go-python-packaging/version"
)

func resetSpecMemo(t *testing.T) {
	t.Helper()
	clear := func() {
		specMemo.Range(func(k, _ any) bool { specMemo.Delete(k); return true })
		specMemoN.Store(0)
	}
	clear()
	t.Cleanup(clear)
}

func mustSpecs(t testing.TB, s string) version.Specifiers {
	t.Helper()
	ss, err := version.NewSpecifiers(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ss
}

// A memo hit is equal to a fresh parse, and costs no allocations.
func TestFromSpecifiersMemoHitEqualsFreshParse(t *testing.T) {
	resetSpecMemo(t)
	for _, s := range []string{">=3.8", ">=3.8,<4", "!=1.5.*,>=1.0", "~=2.1", "==1.0.*", "<2,>1; "} {
		ss, err := version.NewSpecifiers(s)
		if err != nil {
			continue
		}
		fresh, ferr := fromSpecifiers(ss)
		first, err1 := FromSpecifiers(ss)
		hit, err2 := FromSpecifiers(ss)
		if (ferr == nil) != (err1 == nil) || (ferr == nil) != (err2 == nil) {
			t.Fatalf("%q: error mismatch %v / %v / %v", s, ferr, err1, err2)
		}
		if !first.Equal(fresh) || !hit.Equal(fresh) {
			t.Errorf("%q: memoized set differs from a fresh parse", s)
		}
	}
}

func TestFromSpecifiersMemoHitDoesNotReparse(t *testing.T) {
	resetSpecMemo(t)
	ss := mustSpecs(t, ">=3.8,<4,!=3.9.*")
	if _, err := FromSpecifiers(ss); err != nil {
		t.Fatal(err)
	}
	cold := testing.AllocsPerRun(20, func() { _, _ = fromSpecifiers(ss) })
	warm := testing.AllocsPerRun(20, func() { _, _ = FromSpecifiers(ss) })
	// A hit costs the key string at most; a parse costs dozens of allocations.
	if warm > cold/4 {
		t.Errorf("memo hit allocates %.0f, a fresh parse %.0f: the memo is not being read", warm, cold)
	}
}

// Errors are not cached: each call reports its own.
func TestFromSpecifiersMemoSkipsErrors(t *testing.T) {
	resetSpecMemo(t)
	ss := mustSpecs(t, "===1.0")
	for i := 0; i < 2; i++ {
		if _, err := FromSpecifiers(ss); !errors.Is(err, ErrUnrepresentable) {
			t.Fatalf("call %d: err = %v, want ErrUnrepresentable", i, err)
		}
	}
	if n := specMemoN.Load(); n != 0 {
		t.Errorf("memo holds %d entries after only failures", n)
	}
}

// Past specMemoMax the memo stops admitting: its size is bounded and answers
// stay correct, hit or miss.
func TestFromSpecifiersMemoIsCapped(t *testing.T) {
	resetSpecMemo(t)
	old := specMemoMax
	specMemoMax = 100
	t.Cleanup(func() { specMemoMax = old })
	for i := 0; i < 600; i++ {
		ss := mustSpecs(t, fmt.Sprintf(">=%d.%d", i/100, i%100))
		got, err := FromSpecifiers(ss)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(mustFresh(t, ss)) {
			t.Fatalf("spec %d: wrong set", i)
		}
	}
	if n := specMemoN.Load(); n != 100 {
		t.Errorf("memo count = %d, want exactly the cap 100", n)
	}
	count := 0
	specMemo.Range(func(_, _ any) bool { count++; return true })
	if count != 100 {
		t.Errorf("memo holds %d entries, want 100", count)
	}
}

func mustFresh(t testing.TB, ss version.Specifiers) Set {
	t.Helper()
	s, err := fromSpecifiers(ss)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Many goroutines hitting and filling the memo together (run with -race).
func TestFromSpecifiersMemoConcurrent(t *testing.T) {
	resetSpecMemo(t)
	var texts []string
	for i := 0; i < 50; i++ {
		texts = append(texts, fmt.Sprintf(">=%d.0,<%d", i, i+3))
	}
	want := make([]Set, len(texts))
	for i, s := range texts {
		want[i] = mustFresh(t, mustSpecs(t, s))
	}
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for r := 0; r < 20; r++ {
				for i := range texts {
					j := (i + g) % len(texts)
					ss, err := version.NewSpecifiers(texts[j])
					if err != nil {
						t.Error(err)
						return
					}
					got, err := FromSpecifiers(ss)
					if err != nil || !got.Equal(want[j]) {
						t.Errorf("%s: wrong result (%v)", texts[j], err)
						return
					}
					// Derived sets must not disturb the shared one.
					_ = got.Complement().Intersect(got)
				}
			}
		}(g)
	}
	wg.Wait()
	for i, s := range texts {
		got, _ := FromSpecifiers(mustSpecs(t, s))
		if !got.Equal(want[i]) {
			t.Errorf("%s: cached set changed after concurrent use", s)
		}
	}
}
