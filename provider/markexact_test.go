// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/pep440set"
	"github.com/posit-dev/go-pyresolver/provider"
)

func markExactDeps(t *testing.T, on bool) map[string]bool {
	t.Helper()
	idx := index.NewMockIndex("test").
		AddVersion("app", "1.0", "a>=1", "b==2.0", "c==1.*", "d>=1,<=1", "e!=3")
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		idx.AddVersion(n, "1.0")
	}
	opts := testOptions(t)
	opts.MarkExact = on
	p := provider.New(context.Background(), idx, opts)
	deps, err := p.Dependencies(provider.Project("app"), pep440set.Exactly(mustVersion(t, "1.0")))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range deps {
		got[d.Package.Name.String()] = d.Exact
	}
	return got
}

func TestMarkExact(t *testing.T) {
	got := markExactDeps(t, true)
	// d is >=1,<=1: one version as declared, so the set test says exact.
	want := map[string]bool{"a": false, "b": true, "c": false, "d": true, "e": false}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("Exact[%s] = %v, want %v", k, got[k], w)
		}
	}
}

func TestMarkExactOffByDefault(t *testing.T) {
	for k, v := range markExactDeps(t, false) {
		if v {
			t.Errorf("Exact[%s] set with the option off", k)
		}
	}
}
