// SPDX-License-Identifier: Apache-2.0 OR MIT

package provider_test

import (
	"context"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/provider"
)

// ExtraEdgeOnly drops the direct base dependency of a requirement with extras
// and keeps the extra ones; it is off by default and leaves plain requirements alone.
func TestExtraEdgeOnlyDropsBaseEdge(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("app", "1.0", "flask[async]>=2.0", "click>=8")

	for _, on := range []bool{false, true} {
		opts := testOptions(t)
		opts.ExtraEdgeOnly = on
		p := provider.New(context.Background(), idx, opts)
		byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Project("app"), "1.0"))
		_, base := byPkg[provider.Project("flask")]
		_, extra := byPkg[provider.WithExtra("flask", "async")]
		_, click := byPkg[provider.Project("click")]
		if base == on || !extra || !click {
			t.Errorf("ExtraEdgeOnly=%v: base=%v extra=%v click=%v", on, base, extra, click)
		}
	}
}

// The root's own requirements follow the same rule.
func TestExtraEdgeOnlyRoot(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("flask", "2.0")
	for _, on := range []bool{false, true} {
		opts := testOptions(t)
		opts.ExtraEdgeOnly = on
		opts.Requirements = mustRequirements(t, "flask[async]>=2.0")
		p := provider.New(context.Background(), idx, opts)
		byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Root(), "0"))
		_, base := byPkg[provider.Project("flask")]
		_, extra := byPkg[provider.WithExtra("flask", "async")]
		if base == on || !extra {
			t.Errorf("ExtraEdgeOnly=%v: base=%v extra=%v", on, base, extra)
		}
	}
}

// A root `===` requirement takes a separate path and follows the same rule.
func TestExtraEdgeOnlyRootArbitraryEquality(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("flask", "2.0")
	for _, on := range []bool{false, true} {
		opts := testOptions(t)
		opts.ExtraEdgeOnly = on
		opts.Requirements = mustRequirements(t, "flask[async]===2.0")
		p := provider.New(context.Background(), idx, opts)
		byPkg := depsByPackage(t, dependenciesOf(t, p, provider.Root(), "0"))
		_, base := byPkg[provider.Project("flask")]
		_, extra := byPkg[provider.WithExtra("flask", "async")]
		if base == on || !extra {
			t.Errorf("ExtraEdgeOnly=%v: base=%v extra=%v", on, base, extra)
		}
	}
}
