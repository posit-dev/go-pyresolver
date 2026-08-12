// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
	"github.com/posit-dev/go-pyresolver/resolver"
	"github.com/posit-dev/go-python-packaging/marker"
	"github.com/posit-dev/go-python-packaging/requirement"
	"github.com/posit-dev/go-python-packaging/tags"
	"github.com/posit-dev/go-python-packaging/version"
)

// testEnv is the marker environment every test in this package resolves
// against: CPython 3.11.4 on linux/x86_64.
//
// Built through EnvironmentFromTarget rather than as a struct literal, because
// a literal zero-fills the ten fields it does not mention -- python_version
// becomes "" and a marker like python_version >= "3.8" silently flips its
// answer.
func testEnv(t *testing.T) marker.Environment {
	t.Helper()
	env, err := marker.EnvironmentFromTarget(
		tags.Target{Implementation: "cp", PyMajor: 3, PyMinor: 11, OS: "linux", Arch: "x86_64"},
		marker.InterpreterIdentity{
			ImplementationName:           "cpython",
			PlatformPythonImplementation: "CPython",
			PythonFullVersion:            "3.11.4",
			ImplementationVersion:        "3.11.4",
		},
	)
	if err != nil {
		t.Fatalf("build marker environment: %v", err)
	}
	return env
}

func testOptions(t *testing.T) resolver.Options {
	t.Helper()
	return resolver.Options{
		Environment:   testEnv(t),
		PythonVersion: version.MustParse("3.11.4"),
	}
}

func mustRequirements(t *testing.T, ss ...string) []requirement.Requirement {
	t.Helper()
	out := make([]requirement.Requirement, 0, len(ss))
	for _, s := range ss {
		r, err := requirement.Parse(s)
		if err != nil {
			t.Fatalf("parse requirement %q: %v", s, err)
		}
		out = append(out, r)
	}
	return out
}

func mustSpecifiers(t *testing.T, s string) version.Specifiers {
	t.Helper()
	specs, err := version.NewSpecifiers(s)
	if err != nil {
		t.Fatalf("parse specifiers %q: %v", s, err)
	}
	return specs
}

// resolve runs a resolution with the default test options.
func resolve(t *testing.T, idx index.MetadataIndex, reqs ...string) (*resolver.Resolution, error) {
	t.Helper()
	return resolver.Resolve(context.Background(), mustRequirements(t, reqs...), idx, testOptions(t))
}

// pins renders Pinned as plain strings so a test can compare it whole.
// version.Version holds slices and is not comparable.
func pins(t *testing.T, r *resolver.Resolution) map[string]string {
	t.Helper()
	out := make(map[string]string, len(r.Pinned))
	for name, v := range r.Pinned {
		out[name.String()] = v.String()
	}
	return out
}

func names(order []index.PackageName) []string {
	out := make([]string, 0, len(order))
	for _, n := range order {
		out = append(out, n.String())
	}
	return out
}

func TestResolvePinsTheNewestCompatibleVersions(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0", "werkzeug>=2.0").
		AddVersion("flask", "3.0", "werkzeug>=3.0").
		AddVersion("werkzeug", "2.1").
		AddVersion("werkzeug", "3.0.1")

	res, err := resolve(t, idx, "flask>=2.0")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := map[string]string{"flask": "3.0", "werkzeug": "3.0.1"}
	if got := pins(t, res); !reflect.DeepEqual(got, want) {
		t.Errorf("Pinned = %v, want %v", got, want)
	}
}

// The interpreter is an INPUT to a resolution, not a result of it, and the root
// is synthetic. Either one appearing in Pinned would be a package the caller
// then tries to install.
func TestResolveOmitsTheInterpreterAndTheRoot(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("flask", "3.0")

	res, err := resolve(t, idx, "flask")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, unwanted := range []string{"python", ""} {
		if _, ok := res.Pinned[index.PackageName(unwanted)]; ok {
			t.Errorf("Pinned contains %q: %v", unwanted, pins(t, res))
		}
	}
	for _, n := range res.Order {
		if n == "python" || n == "" {
			t.Errorf("Order contains %q: %v", n, names(res.Order))
		}
	}
}

// A caller wants "flask 3.0", not "flask 3.0 alongside flask[async] 3.0". The
// virtual package collapses into its base and the activated extra is reported
// separately, so nothing is lost and nothing is duplicated.
func TestResolveCollapsesExtrasIntoTheBasePackage(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresDist:  mustRequirements(t, "werkzeug>=3.0", `asgiref>=3.2; extra == "async"`),
			ProvidesExtra: []string{"async"},
		}).
		AddVersion("werkzeug", "3.0.1").
		AddVersion("asgiref", "3.7")

	res, err := resolve(t, idx, "flask[async]")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := map[string]string{"flask": "3.0", "werkzeug": "3.0.1", "asgiref": "3.7"}
	if got := pins(t, res); !reflect.DeepEqual(got, want) {
		t.Errorf("Pinned = %v, want %v", got, want)
	}
	if got, want := res.Extras["flask"], []string{"async"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Extras[flask] = %v, want %v", got, want)
	}
	// The extra must not also arrive as a package of its own.
	if _, ok := res.Pinned["flask[async]"]; ok {
		t.Errorf("Pinned holds the virtual extra package: %v", pins(t, res))
	}
	seen := 0
	for _, n := range res.Order {
		if n == "flask" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("flask appears %d times in Order (%v), want exactly once", seen, names(res.Order))
	}
}

// ⚠️ Extras values come from a solution keyed by a map. Sorting them is what
// keeps two runs over the same inputs producing the same lockfile.
func TestResolveSortsExtras(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresDist: mustRequirements(t,
				`asgiref>=3.2; extra == "async"`,
				`python-dotenv>=1.0; extra == "dotenv"`,
			),
			ProvidesExtra: []string{"async", "dotenv"},
		}).
		AddVersion("asgiref", "3.7").
		AddVersion("python-dotenv", "1.0.1")

	res, err := resolve(t, idx, "flask[dotenv]", "flask[async]")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := res.Extras["flask"], []string{"async", "dotenv"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Extras[flask] = %v, want %v (sorted)", got, want)
	}
}

// Nondeterministic output is the kind of defect that passes every other test
// and then produces a lockfile that differs run to run. Map iteration order is
// randomized per range in Go, so repeating a resolve is a real check.
func TestResolveIsDeterministic(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresDist: mustRequirements(t,
				"werkzeug>=3.0", "jinja2>=3.1",
				`asgiref>=3.2; extra == "async"`,
				`python-dotenv>=1.0; extra == "dotenv"`,
			),
			ProvidesExtra: []string{"async", "dotenv"},
		}).
		AddVersion("werkzeug", "3.0.1").
		AddVersion("jinja2", "3.1.2").
		AddVersion("asgiref", "3.7").
		AddVersion("python-dotenv", "1.0.1")

	first, err := resolve(t, idx, "flask[async]", "flask[dotenv]")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for i := range 10 {
		got, err := resolve(t, idx, "flask[async]", "flask[dotenv]")
		if err != nil {
			t.Fatalf("Resolve (run %d): %v", i, err)
		}
		if !reflect.DeepEqual(names(got.Order), names(first.Order)) {
			t.Fatalf("run %d Order = %v, want %v", i, names(got.Order), names(first.Order))
		}
		if !reflect.DeepEqual(got.Extras, first.Extras) {
			t.Fatalf("run %d Extras = %v, want %v", i, got.Extras, first.Extras)
		}
		if !reflect.DeepEqual(pins(t, got), pins(t, first)) {
			t.Fatalf("run %d Pinned = %v, want %v", i, pins(t, got), pins(t, first))
		}
	}
}

// Order names each package exactly once and names every package that was
// pinned, so a caller can install in it.
func TestResolveOrderCoversEachPinnedPackageOnce(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "3.0", "werkzeug>=3.0", "jinja2>=3.1").
		AddVersion("werkzeug", "3.0.1").
		AddVersion("jinja2", "3.1.2")

	res, err := resolve(t, idx, "flask")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	seen := map[index.PackageName]int{}
	for _, n := range res.Order {
		seen[n]++
	}
	for n, count := range seen {
		if count != 1 {
			t.Errorf("Order names %s %d times: %v", n, count, names(res.Order))
		}
	}
	if len(seen) != len(res.Pinned) {
		t.Errorf("Order = %v, but Pinned = %v", names(res.Order), pins(t, res))
	}
	for n := range res.Pinned {
		if seen[n] == 0 {
			t.Errorf("Order omits pinned package %s: %v", n, names(res.Order))
		}
	}
}

// oldestFirst is the opposite of the default policy, so a test can tell that
// Options.Policy reached the solver rather than being dropped on the way.
type oldestFirst struct{}

func (oldestFirst) Less(_ index.PackageName, a, b version.Version) bool { return a.LessThan(b) }

// ⚠️ Every other test in this file passes whether or not Options.Policy is
// wired up, because they all want the default. The three tests below exist
// because an option that is accepted and then ignored is invisible: the
// resolution succeeds and quietly answers a different question.
func TestResolveHonoursThePolicy(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "1.0").
		AddVersion("flask", "2.0")

	opts := testOptions(t)
	opts.Policy = oldestFirst{}
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, "flask"), idx, opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := pins(t, res)["flask"]; got != "1.0" {
		t.Errorf("flask = %s, want 1.0 -- the default policy would have chosen 2.0, so the "+
			"Policy option was ignored", got)
	}
}

func TestResolveHonoursAllowPrerelease(t *testing.T) {
	idx := index.NewMockIndex("test").AddVersion("flask", "2.0rc1")

	// A pre-release nobody asked for is not offered, so this cannot resolve.
	if _, err := resolve(t, idx, "flask"); err == nil {
		t.Fatal("a pre-release was offered without being asked for")
	}

	opts := testOptions(t)
	opts.AllowPrerelease = []index.PackageName{index.NewPackageName("flask")}
	res, err := resolver.Resolve(context.Background(), mustRequirements(t, "flask"), idx, opts)
	if err != nil {
		t.Fatalf("Resolve with AllowPrerelease: %v", err)
	}
	if got := pins(t, res)["flask"]; got != "2.0rc1" {
		t.Errorf("flask = %q, want 2.0rc1", got)
	}
}

// MaxRounds is a safety valve, not a tuning knob: go-pubgrub documents that
// termination of the outer loop is asserted rather than derived, and
// requires_dist is untrusted third-party text. Hitting the bound must fail
// loudly, and must NOT be reported as a conflict between the user's
// requirements -- it is not one.
func TestResolveHonoursMaxRounds(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "3.0", "werkzeug>=3.0", "jinja2>=3.1").
		AddVersion("werkzeug", "3.0.1", "markupsafe>=2.0").
		AddVersion("jinja2", "3.1.2", "markupsafe>=2.0").
		AddVersion("markupsafe", "2.1")

	opts := testOptions(t)
	opts.MaxRounds = 1
	_, err := resolver.Resolve(context.Background(), mustRequirements(t, "flask"), idx, opts)
	if err == nil {
		t.Fatal("Resolve completed a multi-package solve in one round, so MaxRounds was ignored")
	}
	var re *resolver.ResolutionError
	if errors.As(err, &re) {
		t.Errorf("hitting the round bound was reported as a conflict between requirements:\n%s", re.Error())
	}
}

// refusingIndex fails every call and counts how many it got. Two sources of
// truth for the interpreter is how a resolution silently targets one Python
// while evaluating markers for another, so the disagreement must be caught
// BEFORE any work is done -- a mismatch reported as an index error, or reported
// only after a long solve, is not the same protection.
type refusingIndex struct{ calls atomic.Int64 }

var errRefused = errors.New("refusingIndex: the resolution should never have reached the index")

func (r *refusingIndex) Versions(context.Context, index.PackageName) ([]version.Version, error) {
	r.calls.Add(1)
	return nil, errRefused
}

func (r *refusingIndex) Metadata(context.Context, index.PackageName, version.Version) (index.PackageMetadata, error) {
	r.calls.Add(1)
	return index.PackageMetadata{}, errRefused
}

func (r *refusingIndex) Files(context.Context, index.PackageName, version.Version) ([]index.DistFile, error) {
	r.calls.Add(1)
	return nil, errRefused
}

func TestResolveRejectsAPythonVersionThatDisagreesWithTheEnvironment(t *testing.T) {
	opts := testOptions(t)
	opts.PythonVersion = version.MustParse("3.9.18") // the environment says 3.11.4

	idx := &refusingIndex{}
	_, err := resolver.Resolve(context.Background(), mustRequirements(t, "flask"), idx, opts)
	if err == nil {
		t.Fatal("Resolve succeeded with a PythonVersion the environment contradicts")
	}
	if errors.Is(err, errRefused) {
		t.Fatalf("Resolve reached the index before validating its options: %v", err)
	}
	if n := idx.calls.Load(); n != 0 {
		t.Errorf("the index was called %d times before validation", n)
	}
	for _, want := range []string{"3.9.18", "3.11.4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q, so the caller cannot see which side is wrong", err, want)
		}
	}
}

// A zero-value Environment evaluates markers silently rather than failing, so a
// missing python_full_version must be refused rather than compared against.
func TestResolveRejectsAnEnvironmentWithNoPythonFullVersion(t *testing.T) {
	opts := resolver.Options{
		Environment:   marker.Environment{},
		PythonVersion: version.MustParse("3.11.4"),
	}
	idx := &refusingIndex{}
	_, err := resolver.Resolve(context.Background(), mustRequirements(t, "flask"), idx, opts)
	if err == nil {
		t.Fatal("Resolve succeeded against a zero-value Environment")
	}
	if n := idx.calls.Load(); n != 0 {
		t.Errorf("the index was called %d times before validation", n)
	}
	if !strings.Contains(err.Error(), "python_full_version") {
		t.Errorf("error %q does not name the variable that is missing", err)
	}
}

// PEP 440 equality, not string equality: 3.11.4 and 3.11.4.0 are the same
// version, and refusing that pair would reject a correct caller.
func TestResolveAcceptsAnEquivalentPythonVersionSpelling(t *testing.T) {
	opts := testOptions(t)
	opts.PythonVersion = version.MustParse("3.11.4.0")

	idx := index.NewMockIndex("test").AddVersion("flask", "3.0")
	if _, err := resolver.Resolve(context.Background(), mustRequirements(t, "flask"), idx, opts); err != nil {
		t.Fatalf("Resolve rejected an equivalent spelling of the same version: %v", err)
	}
}
