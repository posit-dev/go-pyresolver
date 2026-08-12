// SPDX-License-Identifier: Apache-2.0 OR MIT

package resolver_test

import (
	"strings"
	"testing"

	"github.com/posit-dev/go-pyresolver/index"
)

// THE conflict PubGrub exists for: the newest version of one package is
// incompatible with a constraint stated elsewhere, and the resolver has to back
// out of it rather than fail. A resolver that only ever tried the newest of
// everything -- which a naive walk over requires_dist is -- reports this as
// unsolvable.
func TestEndToEndTransitiveMultiVersionConflict(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("bar", "1.0", "foo>=1.0").
		AddVersion("bar", "2.0", "foo<1.0").
		AddVersion("foo", "0.9").
		AddVersion("foo", "1.0")

	res, err := resolve(t, idx, "foo>=1.0", "bar")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// bar 2.0 is newer, and it is the wrong answer: it requires foo<1.0 while
	// the caller asked for foo>=1.0.
	want := map[string]string{"bar": "1.0", "foo": "1.0"}
	if got := pins(t, res); got["bar"] != want["bar"] || got["foo"] != want["foo"] {
		t.Errorf("Pinned = %v, want %v", got, want)
	}
}

// A report is only worth building if it explains the chain of reasoning rather
// than announcing the outcome. This asserts the sentences, not just that some
// text came back.
func TestEndToEndUnsatisfiableRequestExplainsTheChain(t *testing.T) {
	// root-a needs shared>=2.0. root-b needs middle, which needs shared<2.0.
	// Nothing satisfies both, and no single package is at fault.
	idx := index.NewMockIndex("test").
		AddVersion("root-a", "1.0", "shared>=2.0").
		AddVersion("root-b", "1.0", "middle<1.0").
		AddVersion("middle", "0.9", "shared<2.0").
		AddVersion("shared", "1.0").
		AddVersion("shared", "2.0")

	re := resolutionError(t, idx, "root-a", "root-b")
	msg := re.Error()

	if len(re.Report.Lines) < 3 {
		t.Errorf("the report is %d lines; a chain this long should be explained step by step:\n%s",
			len(re.Report.Lines), msg)
	}
	// Each of these states a fact the FIXTURE declares, in the direction the
	// fixture declares it. A golden generated from the implementation would
	// freeze a reversed dependency direction just as happily.
	for _, want := range []string{
		"root-b 1.0 depends on middle <1.0",
		"middle 0.9 depends on shared <2.0",
		"root-a 1.0 depends on shared >=2.0",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the report never says %q:\n%s", want, msg)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(msg), "cannot be satisfied.") {
		t.Errorf("the report does not end by stating the conclusion:\n%s", msg)
	}
	// Nothing about the failure involves an sdist, and nothing recorded should
	// invent one.
	if strings.Contains(msg, "sdist") {
		t.Errorf("the report mentions an sdist that has nothing to do with the failure:\n%s", msg)
	}
}

// THE payoff for modeling the interpreter as a package. Filtering incompatible
// releases out of the candidate list instead would make this read "no versions
// of flask" -- true in a useless way. Because the interpreter is a package, the
// report says which Python was demanded and which one is being targeted.
//
// ⚠️ It must say "Python", not "python": "python" is a real PyPI project, and a
// formatter that rendered both alike would make the report's clause order
// arbitrary. See TestPackageIsInjectiveForTheInterpreter.
func TestEndToEndRequiresPythonFailureNamesTheInterpreter(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresPython:    mustSpecifiers(t, ">=3.12"),
			RequiresPythonRaw: ">=3.12",
		})

	re := resolutionError(t, idx, "flask")
	msg := re.Error()

	for _, want := range []string{
		"flask 3.0 depends on Python >=3.12", // what the release demands
		"Python 3.11.4",                      // what the resolution targets
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the report never says %q:\n%s", want, msg)
		}
	}
}

func TestEndToEndExtrasPullInTheirOwnRequirements(t *testing.T) {
	idx := index.NewMockIndex("test").
		SetMetadata("flask", "3.0", index.PackageMetadata{
			RequiresDist: mustRequirements(t,
				"werkzeug>=3.0",
				`asgiref>=3.2; extra == "async"`,
				`python-dotenv>=1.0; extra == "dotenv"`,
			),
			ProvidesExtra: []string{"async", "dotenv"},
		}).
		AddVersion("werkzeug", "3.0.1").
		AddVersion("asgiref", "3.7").
		AddVersion("python-dotenv", "1.0.1")

	res, err := resolve(t, idx, "flask[async]")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := pins(t, res)
	if got["asgiref"] != "3.7" {
		t.Errorf("the requested extra's requirement is not pinned: %v", got)
	}
	if _, ok := got["python-dotenv"]; ok {
		t.Errorf("an extra nobody asked for pulled its requirement in: %v", got)
	}
	if len(res.Extras["flask"]) != 1 || res.Extras["flask"][0] != "async" {
		t.Errorf("Extras[flask] = %v, want [async]", res.Extras["flask"])
	}
}

// An sdist-only newest version must not sink a resolve that has a readable
// version to fall back to. The record is still made -- it is only put in front
// of a user when the resolution actually fails.
func TestEndToEndSdistOnlyNewestFallsBackToAReadableVersion(t *testing.T) {
	idx := index.NewMockIndex("test").
		AddVersion("flask", "2.0", "werkzeug>=2.0").
		SetUnavailable("flask", "3.0").
		AddVersion("werkzeug", "2.1")

	res, err := resolve(t, idx, "flask")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := pins(t, res); got["flask"] != "2.0" {
		t.Errorf("Pinned = %v, want flask 2.0", got)
	}
}
